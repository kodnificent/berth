package config

import (
	"fmt"
	"net/url"

	"github.com/kodnificent/berth/internal/names"
)

// probeSample stands in for {name} when a probe URL is checked before any berth exists,
// chosen so a host written out literally won't pass for the berth's. One that does is
// still never requested: the probe refuses every host outside the berth it runs in.
const probeSample = "berth-probe-sample"

// probeOutside says why the [doctor] probe template tmpl is not a URL within the berth,
// empty when it is or when the template itself is broken, which the template check
// reports.
func (c *Config) probeOutside(tmpl string) string {
	parts, err := parseTemplate(tmpl)
	if err != nil {
		return ""
	}
	hostParts, err := parseTemplate(c.Host())
	if err != nil {
		return ""
	}
	v := Values{Name: probeSample}
	v.Host, _ = expand(hostParts, v)
	if urlParts, err := parseTemplate(c.URL()); err == nil {
		v.URL, _ = expand(urlParts, v)
	}
	sample, _ := expand(parts, v)
	u, err := url.Parse(sample)
	switch {
	case err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Hostname() == "":
		return fmt.Sprintf("%q is not an http or https URL", tmpl)
	case !names.Within(v.Host, u.Hostname()):
		return fmt.Sprintf("%q requests a host outside the berth, and berth doctor --probe only requests the berth's own; write its host as {host} or a name under it, such as app.{host}", tmpl)
	}
	return ""
}

// ProbeURLs expands [doctor] probe for the berth e describes. A URL naming a port with
// no value yet is returned in unresolved as written, since guessing one could request
// another app.
func (c *Config) ProbeURLs(e Effective, ports map[string]int) (urls, unresolved []string) {
	v := Values{Name: e.Name, Host: e.Host, URL: e.URL, Ports: ports}
	for _, tmpl := range c.Doctor.Probe {
		parts, _ := parseTemplate(tmpl)
		if out, ok := expand(parts, v); ok && (e.URL != "" || !uses(parts, "url")) {
			urls = append(urls, out)
		} else {
			unresolved = append(unresolved, tmpl)
		}
	}
	return urls, unresolved
}

// ValidEnvName reports whether name is a key [env.set] accepts.
func ValidEnvName(name string) bool { return envName.MatchString(name) }
