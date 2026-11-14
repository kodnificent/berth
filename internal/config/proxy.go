package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/names"
)

const (
	// DefaultProxyHTTP is the port berth proxy serves plain HTTP on without [proxy] http.
	DefaultProxyHTTP = 80
	// DefaultProxyHTTPS is the port berth proxy serves HTTPS on without [proxy] https.
	DefaultProxyHTTPS = 443
)

// Values [proxy] cookies takes: report cookies wider than the berth's host, or report
// them and replace their domain with the berth's host.
const (
	CookiesWarn    = "warn"
	CookiesRewrite = "rewrite"
)

// Proxy is what berth proxy listens on, from the user config, and, per project, which of
// a berth's ports its names are sent to.
type Proxy struct {
	HTTP    *int              `toml:"http"`
	HTTPS   *int              `toml:"https"`
	Port    string            `toml:"port"`
	Cookies string            `toml:"cookies"`
	Routes  map[string]string `toml:"routes"`
}

// RewritesCookies reports whether the proxy replaces a cookie domain wider than the
// berth's host with the host, rather than only reporting it.
func (c *Config) RewritesCookies() bool { return c.Proxy.Cookies == CookiesRewrite }

// ProxyListenPorts are the ports berth proxy serves HTTP and HTTPS on, read from the user
// config at path alone. The rest of that file is the lowest layer of each project's
// config, and is checked there, where the ports it names can be found.
func ProxyListenPorts(path string) (http, https int, err error) {
	http, https = DefaultProxyHTTP, DefaultProxyHTTPS
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return http, https, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("reading config %s: %w", path, err)
	}
	if _, err := decodeLayer(path, data); err != nil {
		return 0, 0, err
	}
	c := &Config{}
	if _, err := toml.Decode(string(data), c); err != nil {
		return 0, 0, invalid(path, "%s", strings.TrimPrefix(err.Error(), "toml: "))
	}
	if c.Proxy.HTTP != nil {
		http = *c.Proxy.HTTP
	}
	if c.Proxy.HTTPS != nil {
		https = *c.Proxy.HTTPS
	}
	var problems []string
	c.validateListenPorts(func(key toml.Key, format string, args ...any) {
		problems = append(problems, fmt.Sprintf("%s: %s: %s", path, key, fmt.Sprintf(format, args...)))
	})
	if http == https && len(problems) == 0 {
		problems = append(problems, fmt.Sprintf("%s: proxy.http: %d is the port berth proxy serves HTTPS on; give each its own", path, http))
	}
	if len(problems) > 0 {
		return 0, 0, apperr.New(apperr.ConfigInvalid, "%s", strings.Join(problems, "\n"))
	}
	return http, https, nil
}

// ProxyRoutes expands [proxy.routes] for the berth with name and host: each name, in
// lowercase, to the port name it is sent to.
func (c *Config) ProxyRoutes(name, host string) map[string]string {
	out := map[string]string{}
	for k, port := range c.Proxy.Routes {
		parts, err := parseTemplate(k)
		if err != nil {
			continue
		}
		if expanded, ok := expand(parts, Values{Name: name, Host: host}); ok {
			out[strings.TrimSuffix(strings.ToLower(expanded), ".")] = port
		}
	}
	return out
}

// proxyOnlyInUserConfig refuses [proxy] http and https from any file but the user config:
// one proxy serves every berth on the machine, so a project can't choose its ports.
func (c *Config) proxyOnlyInUserConfig(user string) error {
	for _, k := range []string{"proxy.http", "proxy.https"} {
		if f, ok := c.origin[k]; ok && f != user {
			return invalid(f, "%s: one berth proxy serves every berth on the machine, so it reads its ports from the user config only; move this to %s", k, user)
		}
	}
	return nil
}

func (c *Config) validateListenPorts(bad func(toml.Key, string, ...any)) {
	for _, p := range []struct {
		key   string
		value *int
	}{{"http", c.Proxy.HTTP}, {"https", c.Proxy.HTTPS}} {
		if p.value != nil && (*p.value < 1 || *p.value > 65535) {
			bad(toml.Key{"proxy", p.key}, "%d is not a port; want 1-65535", *p.value)
		}
	}
	if c.Proxy.HTTP != nil && c.Proxy.HTTPS != nil && *c.Proxy.HTTP == *c.Proxy.HTTPS {
		bad(toml.Key{"proxy", "https"}, "is the same port as [proxy] http; give each its own")
	}
}

func (c *Config) validateProxy(ports portTable, bad func(toml.Key, string, ...any)) {
	c.validateListenPorts(bad)
	switch c.Proxy.Cookies {
	case "", CookiesWarn, CookiesRewrite:
	default:
		bad(toml.Key{"proxy", "cookies"}, "%q is not a choice; want %q, which reports cookies wider than the berth's host, or %q, which also replaces their domain with the berth's host", c.Proxy.Cookies, CookiesWarn, CookiesRewrite)
	}
	if p := c.Proxy.Port; p != "" {
		if why := ports.undefined(p); why != "" {
			bad(toml.Key{"proxy", "port"}, "%q names no defined port: %s", p, why)
		}
	}
	hostParts, err := parseTemplate(c.Host())
	if err != nil {
		return
	}
	v := Values{Name: probeSample}
	v.Host, _ = expand(hostParts, v)
	for _, k := range sortedKeys(c.Proxy.Routes) {
		key := toml.Key{"proxy", "routes", k}
		if why := ports.undefined(c.Proxy.Routes[k]); why != "" {
			bad(key, "%q names no defined port: %s", c.Proxy.Routes[k], why)
		}
		parts, err := parseTemplate(k)
		if err != nil {
			bad(key, "%v", err)
			continue
		}
		unknown := false
		for _, p := range parts {
			if p.token != "" && p.token != "name" && p.token != "host" {
				bad(key, "unknown template value {%s}; here you can use {name}, {host}", p.token)
				unknown = true
			}
		}
		if unknown {
			continue
		}
		sample, _ := expand(parts, v)
		switch {
		case !names.ValidHost(sample):
			bad(key, "%q is not a hostname: use lowercase letters, digits, hyphens and dots, with {host}", k)
		case sample == v.Host:
			bad(key, "the berth's host itself goes to [proxy] port; route a name under it, such as assets.{host}")
		case !names.Within(v.Host, sample):
			bad(key, "%q is not a name under the berth's host, and the proxy only sends a berth its own names; write it as a name under {host}, such as assets.{host}", k)
		}
	}
}
