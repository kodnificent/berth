package ports

import (
	"net"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/compose"
	"github.com/kodnificent/berth/internal/config"
)

func allFree(int) bool { return true }

func TestWantedDefaultsAndOverrides(t *testing.T) {
	cfg := &config.Config{
		Compose: config.Compose{
			Exclude: []string{"mail"},
			Ports:   map[string]string{"db:3306": "none", "web:443": "assign"},
		},
		Ports: map[string]config.Port{"vite": {Env: "VITE_PORT"}},
	}
	project := &compose.Project{Ports: []compose.Port{
		{Service: "web", Target: 80, Published: "8000"},
		{Service: "db", Target: 3306, Published: "3306"},
		{Service: "search", Target: 7700},
		{Service: "mail", Target: 8025, Published: "8025"},
	}}
	want := []Port{
		{Name: "db:3306", Service: "db", ContainerPort: 3306, Policy: None},
		{Name: "search:7700", Service: "search", ContainerPort: 7700, Policy: Ephemeral},
		{Name: "web:80", Service: "web", ContainerPort: 80, Policy: Assign},
		{Name: "web:443", Service: "web", ContainerPort: 443, Policy: Assign},
		{Name: "vite", Policy: Assign},
	}
	if got := Wanted(cfg, project); !reflect.DeepEqual(got, want) {
		t.Errorf("Wanted =\n%+v\nwant\n%+v", got, want)
	}
}

func TestAllocateKeepsHeldPortsAndSkipsTakenOnes(t *testing.T) {
	want := []Port{{Name: "a", Policy: Assign}, {Name: "b", Policy: Assign}, {Name: "c", Policy: None}}
	held := []Port{{Name: "b", Policy: Assign, HostPort: 100}, {Name: "gone", Policy: Assign, HostPort: 101}}
	busy := map[int]bool{102: true}
	got, err := Allocate(want, held, map[int]bool{103: true}, 100, 110, func(p int) bool { return !busy[p] })
	if err != nil {
		t.Fatal(err)
	}
	if got[0].HostPort != 101 || got[1].HostPort != 100 || got[2].HostPort != 0 {
		t.Errorf("Allocate = %+v, want a on 101 (released by gone), b kept on 100, c unassigned", got)
	}

	// A held port stays even when something now listens on it.
	got, err = Allocate(want[:2], got, nil, 100, 110, func(int) bool { return false })
	if err != nil || got[0].HostPort != 101 || got[1].HostPort != 100 {
		t.Errorf("Allocate with held ports busy = %+v, %v; want them kept", got, err)
	}
}

func TestAllocateFullPool(t *testing.T) {
	_, err := Allocate([]Port{{Name: "a", Policy: Assign}, {Name: "b", Policy: Assign}}, nil, nil, 200, 200, allFree)
	if apperr.CodeOf(err) != apperr.NoFreePorts || apperr.CodeOf(err).Exit() != 2 {
		t.Fatalf("err = %v, want no_free_ports", err)
	}
	if want := "200-200"; !strings.Contains(err.Error(), want) {
		t.Errorf("message %q does not name the pool %s", err, want)
	}
}

func TestFreeSeesListeners(t *testing.T) {
	for _, addr := range []struct{ network, host string }{{"tcp4", "0.0.0.0"}, {"tcp4", "127.0.0.1"}, {"tcp6", "::"}, {"tcp6", "::1"}} {
		l, err := net.Listen(addr.network, net.JoinHostPort(addr.host, "0"))
		if err != nil {
			t.Logf("skipping %s: %v", addr.host, err)
			continue
		}
		port := l.Addr().(*net.TCPAddr).Port
		if Free(port) {
			t.Errorf("Free(%d) with a listener on %s = true", port, addr.host)
		}
		l.Close()
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	if !Free(port) {
		t.Errorf("Free(%s) after the listener closed = false", strconv.Itoa(port))
	}
}
