// Package initconf writes the starting config berth init detects from main: settings it
// can be certain of as config, and everything that needs the user's intent as commented
// suggestions with a reason. It knows ecosystems (compose, lockfiles, package-manager
// caches), never frameworks.
package initconf

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

type Port struct {
	Service string
	Target  int
	// Policy is the one berth gives the port with no config: assign or ephemeral.
	Policy string
	// HostPorts are the fixed host ports main publishes it on.
	HostPorts []int
}

func (p Port) Key() string { return p.Service + ":" + strconv.Itoa(p.Target) }

type Volume struct {
	Key      string
	External bool
	// Uncopyable says why berth can't copy the volume, when it can't.
	Uncopyable string
	// Mounts are the container paths it is mounted at.
	Mounts []string
}

// Input is what init found in main.
type Input struct {
	Private bool
	// ComposeFiles are the files main's compose project is read from; OtherCompose are
	// compose files in the repository main doesn't read.
	ComposeFiles []string
	OtherCompose []string
	EnvFiles     []string
	Ports        []Port
	Volumes      []Volume
	// Services are main's compose services. A port after one, as in http://db:3306, is
	// reached inside compose's network, where only the host side differs.
	Services []string
	// Profiles maps services that run only under compose profiles to the profiles.
	Profiles map[string][]string
	// Env is main's .env.
	Env map[string]string
	// ComposeVars are variables main's compose files read; they are the project's own,
	// and berth never changes them.
	ComposeVars map[string]bool
	// Lockfiles are the tracked lockfiles at main's top level.
	Lockfiles []string
	// IgnoredDirs are the gitignored directories at main's top level.
	IgnoredDirs map[string]bool
	// VolumeDirs maps a top-level directory to the compose volume mounted over it in the
	// container that mounts main's directory, so the host's copy is never used there.
	VolumeDirs map[string]string
	// SmallFiles are small gitignored files at main's top level.
	SmallFiles []string
	Presets    []Preset
}

const reference = "https://useberth.dev/docs/reference/configuration/"

// Render returns the config file for in.
func Render(in Input) string {
	var b strings.Builder
	if in.Private {
		b.WriteString("# Your personal berth config for this repository, written by berth init. It is never\n# committed. Keys here override berth.toml.\n")
	} else {
		b.WriteString("# berth config for this repository, written by berth init. Commit it so every new\n# worktree reads it.\n")
	}
	b.WriteString("# Lines starting with # are suggestions berth can't be sure of; uncomment the ones\n# you want. Reference: " + reference + "\n")

	if len(in.EnvFiles) > 0 {
		fmt.Fprintf(&b, "\n# Main's gitignored env files, which berth clones into every berth: %s.\n", strings.Join(in.EnvFiles, ", "))
	}

	ports := portNames(in.Ports)
	addPresetPorts(ports, in.Presets)
	envLines, urls, detected := envSuggestions(in, ports)

	if len(urls) == 1 {
		b.WriteString("\n[berth]\n# Main's .env uses this URL; the berth's {url} would be its own.\n")
		fmt.Fprintf(&b, "# url = %s\n", quote(urls[0]))
	}

	b.WriteString("\n[compose]\n")
	if len(in.ComposeFiles) > 0 {
		fmt.Fprintf(&b, "# Main reads %s, and so does every berth.\n", strings.Join(in.ComposeFiles, ", "))
	}
	if len(in.OtherCompose) > 0 {
		b.WriteString("# Compose files main doesn't read; list the ones berths apply after main's.\n")
		fmt.Fprintf(&b, "# files = [%s]\n", quoteList(in.OtherCompose))
	}
	profiled := sortedKeys(in.Profiles)
	if len(profiled) > 0 {
		var why []string
		for _, s := range profiled {
			why = append(why, fmt.Sprintf("%s (%s)", s, strings.Join(in.Profiles[s], ", ")))
		}
		fmt.Fprintf(&b, "# Run only under compose profiles: %s. Exclude those berths never need.\n", strings.Join(why, ", "))
		fmt.Fprintf(&b, "# exclude = [%s]\n", quoteList(profiled))
	}

	if len(in.Ports) > 0 {
		b.WriteString("\n[compose.ports]\n")
		for _, p := range sortedPorts(in.Ports) {
			why := "main lets Docker pick its host port"
			if len(p.HostPorts) > 0 {
				why = "main publishes it on " + hostPorts(p.HostPorts)
			}
			fmt.Fprintf(&b, "%s = %s  # %s\n", quote(p.Key()), quote(p.Policy), why)
		}
	}

	if len(in.Volumes) > 0 {
		b.WriteString("\n[compose.volumes]\n")
		vols := slices.Clone(in.Volumes)
		sort.Slice(vols, func(i, j int) bool { return vols[i].Key < vols[j].Key })
		for _, v := range vols {
			switch {
			case v.External:
				fmt.Fprintf(&b, "# %s is external, so main and every berth already use the one volume.\n", key(v.Key))
			case v.Uncopyable != "":
				fmt.Fprintf(&b, "%s = \"fresh\"  # %s\n", key(v.Key), v.Uncopyable)
			case isCache(v.Key, v.Mounts):
				fmt.Fprintf(&b, "%s = \"share\"  # a package-manager cache, safe to share with main\n", key(v.Key))
			default:
				fmt.Fprintf(&b, "%s = \"clone\"\n", key(v.Key))
			}
		}
	}

	writeFiles(&b, in)

	presetEnv := writePresetEnv(in.Presets, detected)
	if len(envLines)+len(presetEnv) > 0 {
		b.WriteString("\n[env.set]\n")
		b.WriteString(strings.Join(envLines, ""))
		if len(envLines) > 0 && len(presetEnv) > 0 {
			b.WriteString("\n")
		}
		b.WriteString(strings.Join(presetEnv, "\n"))
	}

	writePresetPorts(&b, in.Presets)
	writePresetMySQL(&b, in.Presets)

	writeHooks(&b, in)
	return b.String()
}

// writeFiles writes [files] clone: dependency directories next to a tracked lockfile
// are cloned, and small gitignored files are suggested.
func writeFiles(b *strings.Builder, in Input) {
	type entry struct{ path, why string }
	var active, suggested []entry
	var notes []string
	seen := map[string]bool{}
	for _, l := range lockfiles {
		if !l.clone || !slices.Contains(in.Lockfiles, l.name) || seen[l.dir] {
			continue
		}
		seen[l.dir] = true
		switch {
		case in.VolumeDirs[l.dir] != "":
			notes = append(notes, fmt.Sprintf("# %s is the compose volume %s in the container, which [compose.volumes] covers.\n", l.dir, in.VolumeDirs[l.dir]))
		case in.IgnoredDirs[l.dir]:
			active = append(active, entry{l.dir, "gitignored, next to " + l.name})
		}
	}
	for _, f := range in.SmallFiles {
		suggested = append(suggested, entry{f, "a small gitignored file in main"})
	}
	if len(active)+len(suggested)+len(notes) == 0 {
		return
	}
	b.WriteString("\n[files]\n")
	b.WriteString(strings.Join(notes, ""))
	if len(active)+len(suggested) == 0 {
		return
	}
	// With nothing certain to clone the whole key stays commented: an empty list in this
	// layer would replace a lower layer's list.
	lead := ""
	if len(active) == 0 {
		lead = "# "
	}
	fmt.Fprintf(b, "%sclone = [\n", lead)
	for _, e := range active {
		fmt.Fprintf(b, "  %s,  # %s\n", quote(e.path), e.why)
	}
	for _, e := range suggested {
		fmt.Fprintf(b, "#   %s,  # %s\n", quote(e.path), e.why)
	}
	fmt.Fprintf(b, "%s]\n", lead)
}

func writeHooks(b *strings.Builder, in Input) {
	var cmds, from []string
	for _, l := range lockfiles {
		if l.install == "" || !slices.Contains(in.Lockfiles, l.name) || in.VolumeDirs[l.dir] != "" || slices.Contains(cmds, l.install) {
			continue
		}
		cmds = append(cmds, l.install)
		from = append(from, l.name)
	}
	if len(cmds) == 0 {
		return
	}
	b.WriteString("\n[hooks]\n")
	fmt.Fprintf(b, "# %s: an install from the global cache is as fast as a clone. Run it in a container instead if dependencies live there.\n", strings.Join(from, ", "))
	fmt.Fprintf(b, "# post-add = %s\n", quote(strings.Join(cmds, " && ")))
}

// portName is how templates name a host port: the service alone when it has one
// assigned port, else "<service>:<container port>".
type portName struct {
	name  string
	port  Port
	about string
}

func portNames(ports []Port) map[int]portName {
	perService := map[string]int{}
	for _, p := range ports {
		if p.Policy == "assign" {
			perService[p.Service]++
		}
	}
	out := map[int]portName{}
	for _, p := range sortedPorts(ports) {
		if p.Policy != "assign" {
			continue
		}
		name := p.Key()
		if perService[p.Service] == 1 {
			name = p.Service
		}
		for _, hp := range p.HostPorts {
			if _, taken := out[hp]; !taken {
				out[hp] = portName{name: name, port: p}
			}
		}
	}
	return out
}

// addPresetPorts names the ports presets give a berth, so values holding main's port
// follow the berth's. Only active ones: a template can't name a commented-out port.
func addPresetPorts(ports map[int]portName, presets []Preset) {
	for _, p := range presets {
		for _, pt := range p.Ports {
			if _, taken := ports[pt.Main]; pt.Active && pt.Main > 0 && !taken {
				ports[pt.Main] = portName{name: pt.Name, about: fmt.Sprintf("the %s port of the %s preset", pt.Name, p.Name)}
			}
		}
	}
}

var (
	envName     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	credentials = regexp.MustCompile(`://[^/@\s]*@`)
	berthURL    = regexp.MustCompile(`^https?://\{host\}(:\{port\.[^}]+\})?$`)
)

// envSuggestions are commented [env.set] lines for keys whose values in main's .env hold
// main's host or a port main publishes, and the berth URLs those values imply. A key a
// preset also suggests gets no line here; its template goes in detected instead, so the
// key is suggested once, in the preset's block.
func envSuggestions(in Input, ports map[int]portName) (lines []string, urls []string, detected map[string]string) {
	taken := presetEnvKeys(in.Presets)
	detected = map[string]string{}
	for _, k := range sortedKeys(in.Env) {
		if !envName.MatchString(k) || strings.HasPrefix(k, "COMPOSE_") || in.ComposeVars[k] {
			continue
		}
		tmpl, why := template(in.Env[k], ports, in.Services)
		if why == nil {
			continue
		}
		if credentials.MatchString(in.Env[k]) {
			if taken[k] {
				continue
			}
			lines = append(lines, fmt.Sprintf("# %s holds %s, and credentials, so it isn't copied here; template it yourself.\n\n", k, strings.Join(why, " and ")))
			continue
		}
		if taken[k] {
			detected[k] = tmpl
		} else {
			lines = append(lines, fmt.Sprintf("# %s holds %s.\n# %s = %s\n\n", k, strings.Join(why, " and "), k, quote(tmpl)))
		}
		if u := strings.TrimSuffix(tmpl, "/"); berthURL.MatchString(u) && !slices.Contains(urls, u) {
			urls = append(urls, u)
		}
	}
	if n := len(lines); n > 0 {
		lines[n-1] = strings.TrimSuffix(lines[n-1], "\n")
	}
	return lines, urls, detected
}

// template replaces main's host and published ports in value with berth's templates,
// and says what it replaced; why is nil when value holds neither.
func template(value string, ports map[int]portName, services []string) (string, []string) {
	var why []string
	escaped := strings.NewReplacer("{", "{{", "}", "}}").Replace(value)
	out, n := replaceHost(escaped)
	if n > 0 {
		why = append(why, "main's host localhost")
	}
	out, named := replacePorts(out, ports, services)
	why = append(why, named...)
	if len(why) == 0 {
		return value, nil
	}
	return out, why
}

// replacePorts swaps each port main publishes, written as the whole value or after a
// colon as in a URL or host:port, for its {port.<name>} template.
func replacePorts(s string, ports map[int]portName, services []string) (string, []string) {
	var b strings.Builder
	var named []string
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j == i {
			b.WriteByte(s[i])
			i++
			continue
		}
		n, err := strconv.Atoi(s[i:j])
		p, ok := ports[n]
		if err != nil || !ok || i > 0 && (s[i-1] != ':' || slices.Contains(services, hostBefore(s, i-1))) {
			b.WriteString(s[i:j])
			i = j
			continue
		}
		desc := fmt.Sprintf("port %d, which main publishes for %s", n, p.port.Key())
		if n == p.port.Target {
			desc += " and containers use as its container port"
		}
		if p.about != "" {
			desc = fmt.Sprintf("port %d, %s", n, p.about)
		}
		if !slices.Contains(named, desc) {
			named = append(named, desc)
		}
		b.WriteString("{port." + p.name + "}")
		i = j
	}
	return b.String(), named
}

// hostBefore is the hostname that ends at s[end], just before a colon.
func hostBefore(s string, end int) string {
	start := end
	for start > 0 && hostChar(s[start-1]) {
		start--
	}
	return s[start:end]
}

// replaceHost swaps every localhost hostname in s, such as localhost or app.localhost,
// for {host}, leaving longer names like localhost.example.com or mylocalhost alone.
func replaceHost(s string) (string, int) {
	const host = "localhost"
	var b strings.Builder
	n := 0
	for i := 0; i < len(s); {
		j := strings.Index(s[i:], host)
		if j < 0 {
			b.WriteString(s[i:])
			break
		}
		at, end := i+j, i+j+len(host)
		before := at == 0 || !hostChar(s[at-1]) || s[at-1] == '.'
		after := end == len(s) || !hostChar(s[end]) && s[end] != '.'
		b.WriteString(s[i:at])
		if before && after {
			b.WriteString("{host}")
			n++
		} else {
			b.WriteString(host)
		}
		i = end
	}
	return b.String(), n
}

func hostChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.'
}

func hostPorts(ports []int) string {
	if len(ports) == 1 {
		return strconv.Itoa(ports[0])
	}
	return fmt.Sprintf("%d-%d", ports[0], ports[len(ports)-1])
}

func sortedPorts(ports []Port) []Port {
	out := slices.Clone(ports)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Target < out[j].Target
	})
	return out
}

var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func key(k string) string {
	if bareKey.MatchString(k) {
		return k
	}
	return quote(k)
}

// quote writes s as a TOML basic string; JSON's escapes are all valid there.
func quote(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(buf.String(), "\n")
}

func quoteList(list []string) string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = quote(s)
	}
	return strings.Join(out, ", ")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
