package config

import (
	"reflect"
	"strings"
	"testing"
)

const probePorts = "[berth]\nhost = \"{name}.myapp.localhost\"\nurl = \"http://{host}:{port.web}\"\n[compose.ports]\n\"web:80\" = \"assign\"\n"

func TestProbeURLsOutsideTheBerthAreAConfigError(t *testing.T) {
	for _, u := range []string{
		"http://localhost:{port.web}/",
		"http://myapp.localhost:{port.web}/",
		"http://x{host}:{port.web}/",
		"ftp://{host}/",
		"http://{host}@other.example/",
		"{host}/login",
	} {
		paths := layers(t, probePorts+"[doctor]\nprobe = [\""+u+"\"]\n")
		msg := loadErr(t, paths...)
		if !strings.Contains(msg, paths[0]+": doctor.probe: ") || !strings.Contains(msg, u) {
			t.Errorf("probe %s: error %q does not name the file, the key and the URL", u, msg)
		}
	}
}

func TestProbeURLsNamingAnUndefinedPortAreAConfigError(t *testing.T) {
	paths := layers(t, probePorts+"[doctor]\nprobe = [\"http://{host}:{port.vite}/\"]\n")
	if msg := loadErr(t, paths...); !strings.Contains(msg, "doctor.probe: {port.vite} names no defined port") {
		t.Errorf("error %q does not name the undefined port under doctor.probe", msg)
	}
}

func TestProbeURLsExpandForTheBerth(t *testing.T) {
	c := mustLoad(t, layers(t, probePorts+"[doctor]\nprobe = [\"http://app.{host}:{port.web}/login\", \"{url}/health\", \"https://{host}/\"]\n")...)
	ports := c.PortValues(map[string]int{"web:80": 20001})

	urls, unresolved := c.ProbeURLs(c.Resolve("feat-x", ports), ports)

	want := []string{"http://app.feat-x.myapp.localhost:20001/login", "http://feat-x.myapp.localhost:20001/health", "https://feat-x.myapp.localhost/"}
	if !reflect.DeepEqual(urls, want) || len(unresolved) != 0 {
		t.Errorf("probe URLs %v, unresolved %v; want %v", urls, unresolved, want)
	}
}

func TestProbeURLsWithAPortNotAssignedYetAreUnresolved(t *testing.T) {
	c := mustLoad(t, layers(t, probePorts+"[doctor]\nprobe = [\"http://app.{host}:{port.web}/login\"]\n")...)

	urls, unresolved := c.ProbeURLs(c.Resolve("feat-x", nil), nil)

	if len(urls) != 0 || !reflect.DeepEqual(unresolved, []string{"http://app.{host}:{port.web}/login"}) {
		t.Errorf("urls %v, unresolved %v; want the template unresolved", urls, unresolved)
	}
}
