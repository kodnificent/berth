// Package proxy serves every berth's host, and the names under it, from one loopback
// listener, sending each request to a port the berth was assigned.
package proxy

import (
	"net"
	"strings"

	"github.com/kodnificent/berth/internal/names"
)

// Target is where requests for a name go: Port on the loopback address, or, when
// Refused is set, nowhere, for the reason it gives.
type Target struct {
	Port    int
	Refused string
}

// Berth is one berth as the proxy serves it.
type Berth struct {
	ID   string
	Name string
	Host string
	// RewriteCookies replaces a cookie domain wider than Host with Host.
	RewriteCookies bool
	// Port takes the berth's host and every name under it that Routes doesn't name.
	Port   Target
	Routes map[string]Target
	// Gone is a berth whose worktree git no longer has. It keeps its host, so its names
	// get the 404 rather than going to a berth whose host encloses it.
	Gone bool
}

type Table []Berth

// Lookup finds the berth host belongs to and where its request goes. host is a Host
// header, port and all. When berths' hosts nest, the most specific one owns the name.
func (t Table) Lookup(host string) (Berth, Target, bool) {
	name := Hostname(host)
	best := -1
	for i, b := range t {
		if names.Within(b.Host, name) && (best < 0 || len(b.Host) > len(t[best].Host)) {
			best = i
		}
	}
	if best < 0 {
		return Berth{}, Target{}, false
	}
	b := t[best]
	if target, ok := b.Routes[name]; ok {
		return b, target, true
	}
	return b, b.Port, true
}

// Hostname is the name a Host header asks for: lowercase, without a port or a trailing
// dot, as browsers compare hosts.
func Hostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return strings.TrimSuffix(strings.ToLower(host), ".")
}
