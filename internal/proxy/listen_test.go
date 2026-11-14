package proxy

import (
	"net"
	"strings"
	"testing"
)

func TestAReservedPortNamesHowEachSystemAllowsIt(t *testing.T) {
	for goos, want := range map[string][]string{
		"linux":   {"sysctl -w net.ipv4.ip_unprivileged_port_start=80", "setcap cap_net_bind_service=+ep /usr/local/bin/berth"},
		"darwin":  {"sudo env HOME=/Users/me /usr/local/bin/berth proxy"},
		"windows": {"netsh interface ipv4 show excludedportrange protocol=tcp", "netstat -ano -p tcp | findstr :80"},
	} {
		got := strings.Join(allowLowPort(goos, 80, "/usr/local/bin/berth", []string{"HOME=/Users/me"}), "\n")
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("on %s the hint does not say %q:\n%s", goos, w, got)
			}
		}
	}
}

// sudo resets the environment, so a proxy run with it would read another registry
// unless the hint carries what points at this one.
func TestTheSudoHintCarriesTheVariablesThatFindTheRegistry(t *testing.T) {
	t.Setenv("HOME", "/Users/me")
	t.Setenv("XDG_STATE_HOME", "/Users/me/my state")
	t.Setenv("XDG_CONFIG_HOME", "/Users/me/.cfg")
	t.Setenv("BERTH_STATE_DIR", "/Users/me/berth")

	got := strings.Join(allowLowPort("darwin", 80, "/opt/berth", sudoEnv()), "\n")

	want := "sudo env HOME=/Users/me XDG_STATE_HOME='/Users/me/my state' XDG_CONFIG_HOME=/Users/me/.cfg BERTH_STATE_DIR=/Users/me/berth /opt/berth proxy"
	if !strings.Contains(got, want) {
		t.Errorf("hint %q does not say %q", got, want)
	}
}

func TestAPortHeldOnTheWildcardAddressIsTaken(t *testing.T) {
	held, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	port := held.Addr().(*net.TCPAddr).Port

	ls, err := Listen(port, "config.toml")

	for _, l := range ls {
		l.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "taken") {
		t.Errorf("Listen on port %d, held on 0.0.0.0: error %v, want the port reported taken", port, err)
	}
}

func TestListenBindsOnlyTheLoopbackAddresses(t *testing.T) {
	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	ls, err := Listen(port, "config.toml")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, l := range ls {
			l.Close()
		}
	}()

	for _, l := range ls {
		if ip := l.Addr().(*net.TCPAddr).IP.String(); ip != "127.0.0.1" && ip != "::1" {
			t.Errorf("Listen bound %s, want only 127.0.0.1 and ::1", l.Addr())
		}
	}
}
