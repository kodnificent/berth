// Package ports decides which ports a berth needs and assigns host ports to them from
// the machine-wide pool.
package ports

import (
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/compose"
	"github.com/kodnificent/berth/internal/config"
)

const (
	Assign    = "assign"
	Ephemeral = "ephemeral"
	None      = "none"
)

// Port is one port a berth knows about. Name is what {port.<name>} uses:
// "<service>:<container port>" for a compose port, the [ports] key otherwise.
type Port struct {
	Name          string `json:"name"`
	Service       string `json:"service,omitempty"`
	ContainerPort int    `json:"container_port,omitempty"`
	HostPort      int    `json:"host_port,omitempty"`
	Policy        string `json:"policy"`
}

// Wanted lists the ports a berth needs: every port the berth's compose project
// publishes, with the policy [compose.ports] gives it, plus [compose.ports] and [ports]
// entries compose doesn't publish. A port main publishes on a fixed host port is
// assigned by default, since publishing main's port again would collide; one Docker
// already picks for main stays ephemeral.
func Wanted(cfg *config.Config, project *compose.Project) []Port {
	excluded := map[string]bool{}
	for _, s := range cfg.Compose.Exclude {
		excluded[s] = true
	}
	byName := map[string]Port{}
	if project != nil {
		for _, p := range project.Ports {
			if excluded[p.Service] {
				continue
			}
			name := p.Service + ":" + strconv.Itoa(p.Target)
			policy := Ephemeral
			if p.Published != "" {
				policy = Assign
			}
			if prev, seen := byName[name]; !seen || prev.Policy == Ephemeral {
				byName[name] = Port{Name: name, Service: p.Service, ContainerPort: p.Target, Policy: policy}
			}
		}
	}
	for key, policy := range cfg.Compose.Ports {
		svc, target, _ := strings.Cut(key, ":")
		if excluded[svc] {
			continue
		}
		n, _ := strconv.Atoi(target)
		byName[key] = Port{Name: key, Service: svc, ContainerPort: n, Policy: policy}
	}
	for name := range cfg.Ports {
		byName[name] = Port{Name: name, Policy: Assign}
	}

	out := make([]Port, 0, len(byName))
	for _, p := range byName {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Service == "") != (b.Service == "") {
			return a.Service != ""
		}
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		if a.ContainerPort != b.ContainerPort {
			return a.ContainerPort < b.ContainerPort
		}
		return a.Name < b.Name
	})
	return out
}

// Allocate gives every assign port in want a host port. A port the berth already holds
// is kept, even while something listens on it, because after a Docker restart that
// something is the berth's own container. New ports come from the lowest free port in
// lo-hi that is not reserved and nothing listens on.
func Allocate(want, held []Port, reserved map[int]bool, lo, hi int, free func(int) bool) ([]Port, error) {
	wanted := map[string]bool{}
	for _, p := range want {
		wanted[p.Name] = p.Policy == Assign
	}
	had := map[string]int{}
	taken := map[int]bool{}
	for _, p := range held {
		if p.Policy == Assign && p.HostPort > 0 && wanted[p.Name] {
			had[p.Name] = p.HostPort
			taken[p.HostPort] = true
		}
	}
	next := lo
	out := make([]Port, len(want))
	for i, p := range want {
		p.HostPort = 0
		if p.Policy == Assign {
			if hp, ok := had[p.Name]; ok {
				p.HostPort = hp
			} else {
				for ; next <= hi && (taken[next] || reserved[next] || !free(next)); next++ {
				}
				if next > hi {
					return nil, apperr.New(apperr.NoFreePorts, "no free port for %s in the pool %d-%d: every port is assigned to a berth, published by a main worktree or in use; widen [pool] ports in the user config", p.Name, lo, hi)
				}
				p.HostPort = next
				taken[next] = true
			}
		}
		out[i] = p
	}
	return out, nil
}

// Free reports whether nothing listens on port on any loopback or wildcard address. Each
// address is tried on its own because a listener on one does not always stop a bind on
// another, depending on the platform and socket options.
func Free(port int) bool {
	for _, a := range []struct{ network, host string }{
		{"tcp4", "0.0.0.0"}, {"tcp4", "127.0.0.1"}, {"tcp6", "::"}, {"tcp6", "::1"},
	} {
		l, err := net.Listen(a.network, net.JoinHostPort(a.host, strconv.Itoa(port)))
		if err == nil {
			l.Close()
			continue
		}
		// Any other error means the address doesn't exist here, such as IPv6 turned off.
		if inUse(err) {
			return false
		}
	}
	return true
}
