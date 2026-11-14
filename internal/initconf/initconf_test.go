package initconf

import (
	"strings"
	"testing"

	"github.com/kodnificent/berth/internal/config"
)

var fixturePorts = []Port{
	{Service: "web", Target: 80, Policy: "assign", HostPorts: []int{8080}},
	{Service: "app", Target: 3000, Policy: "assign", HostPorts: []int{3000}},
	{Service: "app", Target: 9229, Policy: "assign", HostPorts: []int{9229}},
	{Service: "db", Target: 3306, Policy: "assign", HostPorts: []int{3306}},
	{Service: "mail", Target: 8025, Policy: "ephemeral"},
}

func TestTemplate(t *testing.T) {
	ports := portNames(fixturePorts)
	services := []string{"web", "app", "db", "mail"}
	cases := []struct {
		value, want string
		suggested   bool
	}{
		{"http://localhost:8080", "http://{host}:{port.web}", true},
		{"http://localhost:8080/", "http://{host}:{port.web}/", true},
		{"localhost:8080,app.localhost:8080", "{host}:{port.web},app.{host}:{port.web}", true},
		{"127.0.0.1:3306", "127.0.0.1:{port.db}", true},
		{"8080", "{port.web}", true},
		{"localhost", "{host}", true},
		{"http://localhost:3000", "http://{host}:{port.app:3000}", true},
		{"http://localhost:5555", "http://{host}:5555", true},
		{"{json}localhost", "{{json}}{host}", true},
		{"http://db:3306", "", false},
		{"8080:8080", "{port.web}:{port.web}", true},
		{"0.8080", "", false},
		{"mylocalhost", "", false},
		{"localhost.example.com", "", false},
		{"127.0.0.1", "", false},
		{"hello", "", false},
	}
	for _, c := range cases {
		got, why := template(c.value, ports, services)
		if (why != nil) != c.suggested || c.suggested && got != c.want {
			t.Errorf("template(%q) = %q, %v; want %q, suggested %v", c.value, got, why, c.want, c.suggested)
		}
	}
}

func TestIsCache(t *testing.T) {
	for _, c := range []struct {
		key    string
		mounts []string
		want   bool
	}{
		{"composer_cache", nil, true},
		{"bun-cache", nil, true},
		{"pnpm_store", nil, true},
		{"deps", []string{"/home/node/.npm"}, true},
		{"gomod", []string{"/go/pkg/mod/"}, true},
		{"cache", []string{"/cache"}, false},
		{"node_modules", []string{"/app/node_modules"}, false},
		{"mysql", []string{"/var/lib/mysql"}, false},
		{"npm", nil, false},
	} {
		if got := isCache(c.key, c.mounts); got != c.want {
			t.Errorf("isCache(%q, %v) = %v, want %v", c.key, c.mounts, got, c.want)
		}
	}
}

// Render's output must load with berth's own config loader, and stay valid with every
// suggestion uncommented.
func TestRenderLoads(t *testing.T) {
	in := Input{
		ComposeFiles: []string{"compose.yml"},
		OtherCompose: []string{".worktree/compose.yml"},
		EnvFiles:     []string{".env"},
		Ports:        fixturePorts,
		Volumes: []Volume{
			{Key: "db-data", Mounts: []string{"/var/lib/mysql"}},
			{Key: "composer_cache"},
			{Key: "shared", External: true},
			{Key: "nfs", Uncopyable: "uses the nfs driver, which berth can't copy"},
			{Key: "odd.key"},
		},
		Services:    []string{"web", "app", "db", "mail"},
		Profiles:    map[string][]string{"mail": {"dev"}},
		Env:         map[string]string{"APP_URL": "http://localhost:8080", "WEB_PORT": "8080", "REDIS_URL": "redis://:pw@localhost:6379", "COMPOSE_PROJECT_NAME": "localhost"},
		ComposeVars: map[string]bool{"WEB_PORT": true},
		Lockfiles:   []string{"composer.lock", "pnpm-lock.yaml", "yarn.lock"},
		IgnoredDirs: map[string]bool{"vendor": true},
		VolumeDirs:  map[string]string{"node_modules": "node_modules"},
		SmallFiles:  []string{"phpunit.xml"},
	}
	out := Render(in)
	for _, want := range []string{
		`"web:80" = "assign"`,
		`"mail:8025" = "ephemeral"`,
		`db-data = "clone"`,
		`composer_cache = "share"`,
		`nfs = "fresh"`,
		`"odd.key" = "clone"`,
		"# shared is external",
		"\n  \"vendor\",",
		"\n#   \"phpunit.xml\",",
		`# APP_URL = "http://{host}:{port.web}"`,
		`# url = "http://{host}:{port.web}"`,
		`# exclude = ["mail"]`,
		`# files = [".worktree/compose.yml"]`,
		"# node_modules is the compose volume node_modules",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Render lacks %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"WEB_PORT =", "pw@", "COMPOSE_PROJECT_NAME", "post-add"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("Render has %q:\n%s", unwanted, out)
		}
	}
	if _, err := config.Parse("berth.toml", []byte(out)); err != nil {
		t.Fatalf("Render's output doesn't load: %v\n%s", err, out)
	}

	var uncommented []string
	for _, line := range strings.Split(out, "\n") {
		if s, ok := strings.CutPrefix(line, "# "); ok && strings.Contains(s, " = ") {
			line = s
		}
		if s, ok := strings.CutPrefix(line, "#   "); ok {
			line = "  " + s
		}
		uncommented = append(uncommented, line)
	}
	if _, err := config.Parse("berth.toml", []byte(strings.Join(uncommented, "\n"))); err != nil {
		t.Errorf("Render's output with suggestions uncommented doesn't load: %v\n%s", err, strings.Join(uncommented, "\n"))
	}
}

func TestRenderSuggestsInstallsAndCommentsAnEmptyCloneList(t *testing.T) {
	out := Render(Input{Lockfiles: []string{"pnpm-lock.yaml", "uv.lock"}, SmallFiles: []string{"auth.json"}})
	if !strings.Contains(out, `# post-add = "pnpm install --frozen-lockfile --prefer-offline && uv sync --frozen"`) {
		t.Errorf("Render doesn't suggest the installs:\n%s", out)
	}
	if !strings.Contains(out, "# clone = [\n#   \"auth.json\",") || strings.Contains(out, "\nclone = [") {
		t.Errorf("with nothing certain to clone, [files] clone must stay commented:\n%s", out)
	}
	c, err := config.Parse("berth.local.toml", []byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if c.Files.Clone != nil {
		t.Errorf("[files] clone = %v, want unset so berth.toml's list isn't replaced", c.Files.Clone)
	}
}
