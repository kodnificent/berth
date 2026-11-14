package compose

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
)

const base = `name: app
services:
  web:
    image: busybox
    container_name: app-web
    ports:
      - "127.0.0.1:8000:80"
      - "9000"
      - "5000-5001:5000-5001"
    depends_on:
      mail:
        condition: service_healthy
        restart: true
      db:
        condition: service_started
  db:
    image: busybox
    ports: ["3306:3306"]
  mail:
    image: busybox
    profiles: [extras]
    ports: ["8025:8025"]
volumes:
  data: {}
  fixed:
    name: fixed-name
  outside:
    external: true
networks:
  pinned:
    name: pinned-net
`

// berthProject loads base with the override h produces, as docker compose would read
// the berth's COMPOSE_FILE.
func berthProject(t *testing.T, h HostSide) (*types.Project, string) {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "compose.yml"), base)
	p, err := Load(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	out := p.Override(h)
	write(t, filepath.Join(dir, "compose.berth.yml"), string(out))
	t.Setenv("COMPOSE_PROJECT_NAME", h.Project)
	b, err := Load(dir, Options{Files: []string{"compose.yml", "compose.berth.yml"}})
	if err != nil {
		t.Fatalf("loading the override: %v\n%s", err, out)
	}
	return b.model, string(out)
}

func TestOverrideChangesOnlyTheHostSide(t *testing.T) {
	m, out := berthProject(t, HostSide{
		Project: "app-feat",
		Ports:   map[string]int{"web:80": 20001, "web:9000": 0, "web:5000": Unpublished, "db:3306": 20002},
		Exclude: []string{"mail", "no-such-service"},
		Share:   map[string]string{"data": "app_data"},
	})

	web := m.Services["web"]
	ports := map[uint32]types.ServicePortConfig{}
	for _, p := range web.Ports {
		ports[p.Target] = p
	}
	if len(web.Ports) != 3 || ports[80].Published != "20001" || ports[80].HostIP != "127.0.0.1" || ports[9000].Published != "" || ports[5001].Published != "5001" {
		t.Errorf("web ports = %+v, want 80 on 127.0.0.1:20001, 9000 on a port Docker picks, 5000 unpublished and 5001 unchanged\n%s", web.Ports, out)
	}
	if web.ContainerName != "" {
		t.Errorf("web container_name = %q, want it reset", web.ContainerName)
	}
	if d := web.DependsOn["mail"]; d.Required || d.Condition != "service_healthy" || !d.Restart {
		t.Errorf("web depends_on mail = %+v, want the same condition and restart, not required", d)
	}
	if d := web.DependsOn["db"]; !d.Required {
		t.Errorf("web depends_on db = %+v, want it still required", d)
	}
	if m.Services["db"].Ports[0].Published != "20002" {
		t.Errorf("db ports = %+v, want 3306 on 20002", m.Services["db"].Ports)
	}
	// Load enables every profile, so mail is still loaded here; only its profiles show
	// that nothing a user enables brings it back.
	if got := m.Services["mail"].Profiles; !reflect.DeepEqual(got, []string{ExcludedProfile}) {
		t.Errorf("mail profiles = %v, want only %s", got, ExcludedProfile)
	}

	for key, want := range map[string]struct {
		name     string
		external bool
	}{
		"data":    {"app_data", true},
		"fixed":   {"app-feat_fixed", false},
		"outside": {"outside", true},
	} {
		v := m.Volumes[key]
		if v.Name != want.name || bool(v.External) != want.external {
			t.Errorf("volume %s = %s external %v, want %s external %v", key, v.Name, v.External, want.name, want.external)
		}
	}
	if n := m.Networks["pinned"].Name; n != "app-feat_pinned" {
		t.Errorf("network pinned is named %s, want app-feat_pinned", n)
	}
	if n := m.Networks["default"].Name; n != "app-feat_default" {
		t.Errorf("default network is named %s, want app-feat_default", n)
	}
}

func TestOverrideWithNothingToChangeIsValid(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "compose.yml"), "services:\n  worker:\n    image: busybox\n")
	p, err := Load(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	out := string(p.Override(HostSide{Project: "x-y"}))
	if !strings.Contains(out, "services: {}\n") {
		t.Errorf("override = %q, want an empty services map", out)
	}
	write(t, filepath.Join(dir, "compose.berth.yml"), out)
	if _, err := Load(dir, Options{Files: []string{"compose.yml", "compose.berth.yml"}}); err != nil {
		t.Errorf("an empty override does not load: %v", err)
	}
}

func TestOverrideQuotesLiterally(t *testing.T) {
	if got := quote(`a$b"c`); got != `"a$$b\"c"` {
		t.Errorf("quote = %s", got)
	}
}

func TestLoadReportsFilesFromEnv(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.yml"), "services:\n  web:\n    image: busybox\n")
	write(t, filepath.Join(dir, "b.yml"), "services:\n  web:\n    environment: {X: b}\n")
	write(t, filepath.Join(dir, ".env"), "COMPOSE_FILE=a.yml;b.yml\nCOMPOSE_PATH_SEPARATOR=;\n")
	os.Unsetenv("COMPOSE_FILE")
	p, err := Load(dir, Options{ComposeFileEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "a.yml"), filepath.Join(dir, "b.yml")}
	if !p.FilesFromEnv || p.PathSeparator != ";" || !reflect.DeepEqual(p.Files, want) {
		t.Errorf("Load = from env %v, separator %q, files %v; want true, \";\", %v", p.FilesFromEnv, p.PathSeparator, p.Files, want)
	}
}

func TestOverrideDropsEntriesThePolicyMakesIdentical(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "compose.yml"), "name: app\nservices:\n  mail:\n    image: busybox\n    ports: [\"8026:8025\"]\n")
	write(t, filepath.Join(dir, "extra.yml"), "services:\n  mail:\n    ports: [\"8025\"]\n")
	p, err := Load(dir, Options{Files: []string{"compose.yml", "extra.yml"}})
	if err != nil {
		t.Fatal(err)
	}
	out := string(p.Override(HostSide{Project: "app-feat", Ports: map[string]int{"mail:8025": 0}}))
	if n := strings.Count(out, "target: 8025"); n != 1 {
		t.Errorf("override has %d entries for 8025, want 1:\n%s", n, out)
	}
	write(t, filepath.Join(dir, "compose.berth.yml"), out)
	b, err := Load(dir, Options{Files: []string{"compose.yml", "extra.yml", "compose.berth.yml"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := b.model.Services["mail"].Ports; len(got) != 1 || got[0].Published != "" {
		t.Errorf("mail ports = %+v, want one entry for 8025 that Docker picks", got)
	}
}
