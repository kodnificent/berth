package compose

import (
	"path/filepath"
	"slices"
)

// Mount is one volume or bind mount of a service.
type Mount struct {
	Service string
	// Type is "volume", "bind" or another compose mount type.
	Type string
	// Source is the volume key for a named volume, or an absolute path for a bind mount.
	Source string
	Target string
}

// Mounts lists every service's mounts, under any profile.
func (p *Project) Mounts() []Mount {
	var out []Mount
	for _, name := range p.model.ServiceNames() {
		for _, v := range p.model.Services[name].Volumes {
			src := v.Source
			if v.Type == "bind" && src != "" && !filepath.IsAbs(src) {
				src = filepath.Join(p.model.WorkingDir, src)
			}
			out = append(out, Mount{Service: name, Type: v.Type, Source: src, Target: v.Target})
		}
	}
	return out
}

// Profiles maps each service that runs only under compose profiles to those profiles.
func (p *Project) Profiles() map[string][]string {
	out := map[string][]string{}
	for name, s := range p.model.Services {
		if len(s.Profiles) > 0 {
			out[name] = slices.Clone(s.Profiles)
		}
	}
	return out
}
