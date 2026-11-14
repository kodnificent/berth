package compose

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Unpublished, as a host port in HostSide.Ports, keeps a port off the host entirely.
const Unpublished = -1

// ExcludedProfile is the profile excluded services are moved into. Nothing enables it,
// so they never start.
const ExcludedProfile = "berth-excluded"

// HostSide is everything a berth changes about a compose project.
type HostSide struct {
	// Project is the berth's compose project name.
	Project string
	// Ports maps "<service>:<container port>" to the host port to publish it on: 0 lets
	// Docker pick, Unpublished keeps it off the host. Ports not listed are unchanged.
	Ports map[string]int
	// Exclude lists services that never run in the berth.
	Exclude []string
	// Share maps a volume key to the name of the source's volume the berth uses.
	Share map[string]string
}

// Volume reports the Docker name of the volume p declares under key, and whether it is
// external.
func (p *Project) Volume(key string) (name string, external, ok bool) {
	v, ok := p.model.Volumes[key]
	return v.Name, bool(v.External), ok
}

// VolumeSpec is what creating volume key the way compose would needs beyond its name.
func (p *Project) VolumeSpec(key string) (driver string, opts, labels map[string]string) {
	v := p.model.Volumes[key]
	return v.Driver, v.DriverOpts, v.Labels
}

// Override is a compose file that, applied after p's own files, gives p the host side h
// describes and leaves everything containers see unchanged.
func (p *Project) Override(h HostSide) []byte {
	var b strings.Builder
	b.WriteString("# Written by berth add for this worktree's berth. Edits are overwritten.\n")

	excluded := map[string]bool{}
	for _, s := range h.Exclude {
		if _, ok := p.model.Services[s]; ok {
			excluded[s] = true
		}
	}

	// A built image named in the compose file is the same tag in every berth, so a build
	// in one would replace main's image. Each berth names it after its own project, and so
	// does every service that runs it.
	renamed := map[string]string{}
	for _, name := range p.model.ServiceNames() {
		s := p.model.Services[name]
		if s.Build != nil && s.Image != "" && renamed[s.Image] == "" {
			renamed[s.Image] = ImageName(h.Project, name, "")
		}
	}

	var svcs strings.Builder
	for _, name := range p.model.ServiceNames() {
		s := p.model.Services[name]
		var body strings.Builder
		if excluded[name] {
			// !override, because a profile list merges, and a profile the user enables
			// would bring the service back.
			fmt.Fprintf(&body, "    profiles: !override [%s]\n", quote(ExcludedProfile))
		} else {
			if image := renamed[s.Image]; image != "" {
				fmt.Fprintf(&body, "    image: %s\n", quote(image))
			}
			if s.ContainerName != "" {
				// A fixed name would collide with main's container; compose then names it
				// after the berth's project.
				body.WriteString("    container_name: !reset null\n")
			}
			if len(s.Ports) > 0 {
				// !override, because compose merges port lists by their host side and
				// would keep main's published port beside the berth's.
				body.WriteString("    ports: !override")
				var entries strings.Builder
				seen := map[string]bool{}
				for _, port := range s.Ports {
					published := port.Published
					if hp, ok := h.Ports[name+":"+strconv.Itoa(int(port.Target))]; ok {
						switch {
						case hp == Unpublished:
							continue
						case hp == 0:
							published = ""
						default:
							published = strconv.Itoa(hp)
						}
					}
					var entry strings.Builder
					fmt.Fprintf(&entry, "      - target: %d\n", port.Target)
					for _, f := range []struct{ key, value string }{
						{"published", published},
						{"host_ip", port.HostIP},
						{"protocol", port.Protocol},
						{"mode", port.Mode},
						{"app_protocol", port.AppProtocol},
						{"name", port.Name},
					} {
						if f.value != "" {
							fmt.Fprintf(&entry, "        %s: %s\n", f.key, quote(f.value))
						}
					}
					// Two of the project's entries can become one once the policy rewrites
					// their host side, such as main's 8026:8025 and an override's 8025 both
					// left to Docker; compose would publish the pair twice.
					if !seen[entry.String()] {
						seen[entry.String()] = true
						entries.WriteString(entry.String())
					}
				}
				if entries.Len() == 0 {
					body.WriteString(" []\n")
				} else {
					body.WriteString("\n" + entries.String())
				}
			}
			var deps []string
			for dep := range s.DependsOn {
				if excluded[dep] {
					deps = append(deps, dep)
				}
			}
			if len(deps) > 0 {
				// required: false lets the service start without a dependency that is
				// excluded, where compose would otherwise call the project invalid.
				sort.Strings(deps)
				body.WriteString("    depends_on:\n")
				for _, dep := range deps {
					d := s.DependsOn[dep]
					fmt.Fprintf(&body, "      %s:\n", quote(dep))
					if d.Condition != "" {
						fmt.Fprintf(&body, "        condition: %s\n", quote(d.Condition))
					}
					if d.Restart {
						body.WriteString("        restart: true\n")
					}
					body.WriteString("        required: false\n")
				}
			}
		}
		if body.Len() > 0 {
			fmt.Fprintf(&svcs, "  %s:\n%s", quote(name), body.String())
		}
	}
	if svcs.Len() == 0 {
		b.WriteString("services: {}\n")
	} else {
		b.WriteString("services:\n" + svcs.String())
	}

	var vols strings.Builder
	for _, key := range sortedKeys(p.model.Volumes) {
		v := p.model.Volumes[key]
		switch {
		case bool(v.External):
		case h.Share[key] != "":
			// external, so a down --volumes in the berth never deletes main's volume.
			fmt.Fprintf(&vols, "  %s:\n    name: %s\n    external: true\n", quote(key), quote(h.Share[key]))
		case v.Name != p.Name+"_"+key:
			// A name fixed in the compose file would be the same volume in every berth.
			fmt.Fprintf(&vols, "  %s:\n    name: %s\n", quote(key), quote(h.Project+"_"+key))
		}
	}
	if vols.Len() > 0 {
		b.WriteString("volumes:\n" + vols.String())
	}

	var nets strings.Builder
	for _, key := range sortedKeys(p.model.Networks) {
		n := p.model.Networks[key]
		if !n.External && n.Name != p.Name+"_"+key {
			fmt.Fprintf(&nets, "  %s:\n    name: %s\n", quote(key), quote(h.Project+"_"+key))
		}
	}
	if nets.Len() > 0 {
		b.WriteString("networks:\n" + nets.String())
	}
	return []byte(b.String())
}

// quote writes s in JSON's string syntax, which YAML reads as a double-quoted scalar,
// with $ doubled so compose takes it literally instead of interpolating it.
func quote(s string) string {
	q, _ := json.Marshal(strings.ReplaceAll(s, "$", "$$"))
	return string(q)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// ServiceNames lists the services p declares, under any profile.
func (p *Project) ServiceNames() []string { return p.model.ServiceNames() }

// VolumeKeys lists the keys of the volumes p declares.
func (p *Project) VolumeKeys() []string { return sortedKeys(p.model.Volumes) }

// ContainerNames maps each service that fixes its container name to that name.
func (p *Project) ContainerNames() map[string]string {
	out := map[string]string{}
	for name, s := range p.model.Services {
		if s.ContainerName != "" {
			out[name] = s.ContainerName
		}
	}
	return out
}
