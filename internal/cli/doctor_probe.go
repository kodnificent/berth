package cli

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"

	"github.com/kodnificent/berth/internal/compose"
	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/cookie"
	"github.com/kodnificent/berth/internal/probe"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/worktree"
)

// probeFindings requests the berth's running app at its URL and each [doctor] probe URL,
// and reports what it sends back that would undo the berth's own host.
func probeFindings(w *worktree.Worktree, cfg *config.Config, rec registry.Record) []finding {
	assigned := map[string]int{}
	// Only assigned ports are the berth's: a port its URL names otherwise may be main's,
	// or any other app's.
	var own []int
	for _, p := range rec.Ports {
		if p.HostPort > 0 {
			assigned[p.Name] = p.HostPort
			own = append(own, p.HostPort)
		}
	}
	values := cfg.PortValues(assigned)
	eff := cfg.Resolve(rec.Name, values)

	var found []finding
	var urls []string
	if eff.URL != "" {
		urls = append(urls, eff.URL)
	} else {
		found = append(found, finding{"probe", notVerified, cfg.URL(), "not requested: the berth's URL names a port the berth has no value for; run berth add", nil})
	}
	listed, unresolved := cfg.ProbeURLs(eff, values)
	urls = append(urls, listed...)
	for _, u := range unresolved {
		found = append(found, finding{"probe", notVerified, u, "not requested: this [doctor] probe URL names a port the berth has no value for; run berth add", nil})
	}

	mainHosts, mainPorts := mainAddresses(w, cfg)
	report := probe.Scope{Host: eff.Host, Ports: own, MainHosts: mainHosts, MainPorts: mainPorts}.Probe(urls)

	env, _ := dotenv.Read(filepath.Join(w.Path, ".env"))
	for _, p := range report.Pages {
		for _, c := range p.Cookies {
			if f := cookieDomainFinding(p.URL, eff.Host, c, env); f != nil {
				found = append(found, *f)
			}
		}
	}
	for _, r := range report.ToMain {
		found = append(found, finding{"redirect", problem, r.From, fmt.Sprintf("redirects to %s, on %s, so a browser following it leaves this berth for main; look for main's URL or port in the berth's env or the app's config", r.To, r.Why), nil})
	}
	for _, u := range report.Unchecked {
		found = append(found, finding{"probe", notVerified, u.URL, u.Why, nil})
	}
	return found
}

// cookieDomainFinding reports c, set by the page at location, when its domain reaches
// more than host. The app most likely reads its cookie domain from .env, so when exactly
// one key there holds the domain the fix sets it to the berth's host. With several, such
// as a host name shared by a database key, berth can't tell which is the cookie's, and
// names them for a person to choose.
func cookieDomainFinding(location, host string, c *http.Cookie, env map[string]string) *finding {
	if !cookie.Wider(host, c) {
		return nil
	}
	f := &finding{"cookie_domain", problem, location,
		fmt.Sprintf("cookie %s sets Domain=%s, so a browser sends it to every berth under %s, not only this one; scope it to %s or leave Domain out", c.Name, c.Domain, cookie.Domain(c), host), nil}
	var keys []string
	for k, v := range env {
		if strings.ToLower(strings.TrimPrefix(v, ".")) == cookie.Domain(c) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	switch {
	case len(keys) == 1 && config.ValidEnvName(keys[0]):
		f.Message += fmt.Sprintf("; the berth's .env sets %s to it", keys[0])
		f.Fix = &fix{Config: fmt.Sprintf("[env.set]\n%s = \"{host}\"\n", keys[0])}
	case len(keys) == 1:
		f.Message += fmt.Sprintf("; the berth's .env sets %s to it", keys[0])
	case len(keys) > 1:
		f.Message += fmt.Sprintf("; the berth's .env sets %s to it, and berth can't tell which of them, if any, the app reads as its cookie domain", strings.Join(keys, ", "))
	}
	return f
}

// mainAddresses are the hosts and ports main is reached at: the loopback names, the
// ports main's compose project publishes, and what main's .env sets in the keys the
// config maps to a berth's URL, host and ports. Ports a URL only implies are left out,
// or any site on port 80 or 443 would count as main.
func mainAddresses(w *worktree.Worktree, cfg *config.Config) (hosts []string, ports []int) {
	hosts, ports = mainEnvAddresses(w.Repository, cfg)
	// A main whose compose project can't be read publishes nothing the probe can check.
	published, _ := mainPublished(w.Repository)
	return hosts, append(published, ports...)
}

// mainPublished is every host port the compose project of the main worktree at repo
// publishes, none when it has no compose file.
func mainPublished(repo string) ([]int, error) {
	p, err := compose.Load(repo, compose.Options{ComposeFileEnv: true})
	if err != nil || p == nil {
		return nil, err
	}
	var ports []int
	for _, port := range p.Ports {
		ports = append(ports, port.HostPorts()...)
	}
	return ports, nil
}

// mainEnvAddresses is the loopback names, and the hosts and ports the .env of the main
// worktree at repo sets in the keys cfg maps to a berth's URL, host and ports.
func mainEnvAddresses(repo string, cfg *config.Config) (hosts []string, ports []int) {
	hosts = []string{"localhost", "127.0.0.1", "::1"}
	env, err := dotenv.Read(filepath.Join(repo, ".env"))
	if err != nil {
		return hosts, nil
	}
	addPort := func(s string) {
		if n, err := strconv.Atoi(s); err == nil {
			ports = append(ports, n)
		}
	}
	for _, k := range cfg.Env.URL {
		if u, err := url.Parse(env[k]); err == nil && u.Hostname() != "" {
			hosts = append(hosts, u.Hostname())
			addPort(u.Port())
		}
	}
	for _, k := range cfg.Env.Host {
		if h, p, err := net.SplitHostPort(env[k]); err == nil {
			hosts = append(hosts, h)
			addPort(p)
		} else if env[k] != "" {
			hosts = append(hosts, env[k])
		}
	}
	for _, p := range cfg.Ports {
		if p.Env != "" {
			addPort(env[p.Env])
		}
	}
	return hosts, ports
}
