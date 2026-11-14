package preset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/initconf"
)

func repoWith(t *testing.T, files map[string]string, env map[string]string) Repo {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Repo{Root: root, Env: env}
}

func render(t *testing.T, name string, r Repo, in initconf.Input) string {
	t.Helper()
	presets, err := Apply(name, r)
	if err != nil {
		t.Fatal(err)
	}
	in.Presets = presets
	in.Env = r.Env
	out := initconf.Render(in)
	if _, err := config.Parse("berth.toml", []byte(out)); err != nil {
		t.Fatalf("Render's output doesn't load: %v\n%s", err, out)
	}
	return out
}

var serveFiles = map[string]string{
	"artisan":       "",
	"composer.json": `{"require": {"laravel/framework": "^11.0"}, "scripts": {"dev": "php artisan serve"}}`,
}

func TestApplyRefusesAnUnknownPresetNamingTheOnesShipped(t *testing.T) {
	_, err := Apply("nope", Repo{Root: t.TempDir()})
	if apperr.CodeOf(err) != apperr.Usage || !strings.Contains(err.Error(), "laravel, vite") {
		t.Fatalf("Apply(nope) = %v, want a usage error naming the presets", err)
	}
}

func TestAPresetNamedExplicitlyAppliesWithoutItsFiles(t *testing.T) {
	presets, err := Apply("vite", Repo{Root: t.TempDir()})
	if err != nil || len(presets) != 1 || presets[0].Name != "vite" {
		t.Fatalf("Apply(vite) = %v, %v", presets, err)
	}
}

func TestPresetPortIsCommentedWhenAComposeServiceHasItsName(t *testing.T) {
	r := repoWith(t, map[string]string{"vite.config.js": "process.env.VITE_PORT"}, nil)
	r.Services = []string{"vite"}
	out := render(t, "", r, initconf.Input{Ports: []initconf.Port{{Service: "vite", Target: 5173, Policy: "assign", HostPorts: []int{5173}}}})
	if !strings.Contains(out, `# vite = { env = "VITE_PORT" }`) {
		t.Errorf("a port named after a compose service must stay commented, or the config is refused:\n%s", out)
	}
}

func TestASettingComposeReadsIsCommentedWithThatReason(t *testing.T) {
	r := repoWith(t, map[string]string{"vite.config.js": "process.env.VITE_PORT"}, nil)
	r.ComposeVars = map[string]bool{"VITE_PORT": true}
	out := render(t, "", r, initconf.Input{})
	if !strings.Contains(out, "# "+composeReads+"\n# vite = ") {
		t.Errorf("a key compose reads must stay commented with its reason:\n%s", out)
	}
}

func TestMySQLStaysCommentedWhenTheServerIsInCompose(t *testing.T) {
	r := repoWith(t, serveFiles, map[string]string{"DB_CONNECTION": "mysql", "DB_HOST": "db", "DB_USERNAME": "app", "DB_DATABASE": "shop"})
	out := render(t, "", r, initconf.Input{})
	if !strings.Contains(out, "# [state.mysql]\n# from =") || !strings.Contains(out, "# shop = \"dump\"") {
		t.Errorf("a database in compose must leave [state.mysql] commented:\n%s", out)
	}
	if strings.Contains(out, "\n[state.mysql]") {
		t.Errorf("[state.mysql] is active for a database in compose:\n%s", out)
	}
}

func TestMySQLStaysCommentedWhenDBPortIsPublishedByCompose(t *testing.T) {
	r := repoWith(t, serveFiles, map[string]string{"DB_CONNECTION": "mysql", "DB_HOST": "127.0.0.1", "DB_PORT": "3306", "DB_USERNAME": "app"})
	r.Published = []int{3306}
	out := render(t, "", r, initconf.Input{})
	if !strings.Contains(out, "# [state.mysql]") || strings.Contains(out, "\n[state.mysql]") {
		t.Errorf("a server compose publishes isn't one on this machine:\n%s", out)
	}
}

func TestMySQLWithoutAPrefixListsTheDatabaseByName(t *testing.T) {
	r := repoWith(t, serveFiles, map[string]string{"DB_CONNECTION": "mysql", "DB_HOST": "127.0.0.1", "DB_USERNAME": "app", "DB_DATABASE": "shop", "DB_DATABASE_PREFIX": "app_"})
	out := render(t, "", r, initconf.Input{})
	if !strings.Contains(out, "\n# prefix = ") || !strings.Contains(out, "[state.mysql.databases]\nshop = \"dump\"") {
		t.Errorf("without a confirmed prefix the app's database must be listed by name:\n%s", out)
	}
}

func TestPrefixSetButUnreadStaysCommented(t *testing.T) {
	files := map[string]string{"artisan": "", "composer.json": serveFiles["composer.json"], "config/database.php": "env('DB_HOST')"}
	r := repoWith(t, files, map[string]string{"DB_CONNECTION": "mysql", "DB_HOST": "127.0.0.1", "DB_USERNAME": "app", "DB_DATABASE_PREFIX": "app_"})
	if out := render(t, "", r, initconf.Input{}); !strings.Contains(out, "\n# prefix = ") {
		t.Errorf("a prefix no config file reads must stay commented:\n%s", out)
	}
}

func TestAConfirmedTemplateNeverNamesACommentedPort(t *testing.T) {
	files := map[string]string{"artisan": "", "composer.json": `{"require": {"laravel/framework": "^11.0"}}`, "config/app.php": "env('APP_URL')"}
	r := repoWith(t, files, map[string]string{"APP_URL": "http://localhost:8000"})
	out := render(t, "", r, initconf.Input{})
	if !strings.Contains(out, `APP_URL = "http://{host}:8000"`) {
		t.Errorf("APP_URL must keep main's port while the serve port is commented:\n%s", out)
	}
}

func TestDetectionNeverSuggestsAKeyAPresetOwns(t *testing.T) {
	r := repoWith(t, map[string]string{"vite.config.js": "process.env.VITE_PORT"}, map[string]string{"VITE_PORT": "5173"})
	out := render(t, "", r, initconf.Input{Ports: []initconf.Port{{Service: "web", Target: 5173, Policy: "assign", HostPorts: []int{5173}}}})
	if strings.Count(out, "VITE_PORT") != 1 {
		t.Errorf("VITE_PORT is suggested more than once:\n%s", out)
	}
}

func TestEverySuggestionUncommentedStillLoads(t *testing.T) {
	r := repoWith(t, map[string]string{"artisan": "", "composer.json": `{"require": {"laravel/framework": "^11.0"}}`, "vite.config.js": ""}, map[string]string{"DB_CONNECTION": "sqlite", "DB_DATABASE": "shop"})
	out := render(t, "", r, initconf.Input{})
	var uncommented []string
	for _, line := range strings.Split(out, "\n") {
		if s, ok := strings.CutPrefix(line, "# "); ok && (strings.Contains(s, " = ") || strings.HasPrefix(s, "[")) {
			line = s
		}
		uncommented = append(uncommented, line)
	}
	if _, err := config.Parse("berth.toml", []byte(strings.Join(uncommented, "\n"))); err != nil {
		t.Errorf("uncommented, the file doesn't load: %v\n%s", err, strings.Join(uncommented, "\n"))
	}
}

func prefixFiles() map[string]string {
	return map[string]string{
		"artisan":             "",
		"composer.json":       serveFiles["composer.json"],
		"config/database.php": "env('DB_DATABASE_PREFIX'), env('REDIS_PREFIX')",
	}
}

func TestMySQLStaysCommentedWhenTheDatabaseIsOutsideThePrefix(t *testing.T) {
	r := repoWith(t, prefixFiles(), map[string]string{"DB_CONNECTION": "mysql", "DB_HOST": "127.0.0.1", "DB_USERNAME": "app", "DB_DATABASE": "central", "DB_DATABASE_PREFIX": "tenant_"})
	out := render(t, "", r, initconf.Input{})
	if strings.Contains(out, "\n[state.mysql]") {
		t.Errorf("the table looks as if it isolates a database it leaves shared:\n%s", out)
	}
	if !strings.Contains(out, "# DB_DATABASE names a database outside DB_DATABASE_PREFIX") {
		t.Errorf("the commented table doesn't say which keys disagree:\n%s", out)
	}
}

func TestMySQLStaysCommentedWhenDBPortIsUnsetAndComposePublishesTheDefault(t *testing.T) {
	r := repoWith(t, serveFiles, map[string]string{"DB_CONNECTION": "mysql", "DB_HOST": "127.0.0.1", "DB_USERNAME": "app"})
	r.Published = []int{3306}
	out := render(t, "", r, initconf.Input{})
	if !strings.Contains(out, "# [state.mysql]") || strings.Contains(out, "\n[state.mysql]") {
		t.Errorf("an unset DB_PORT means 3306, which compose publishes here:\n%s", out)
	}
}

func TestAPresetKeyHoldingACopiedDatabasesNameIsCommented(t *testing.T) {
	r := repoWith(t, prefixFiles(), map[string]string{"DB_CONNECTION": "mysql", "DB_HOST": "127.0.0.1", "DB_USERNAME": "app", "DB_DATABASE": "app_central", "DB_DATABASE_PREFIX": "app_", "REDIS_PREFIX": "app_central"})
	out := render(t, "", r, initconf.Input{})
	if strings.Contains(out, "\nREDIS_PREFIX = ") || !strings.Contains(out, "# "+copiedDatabase+"\n# REDIS_PREFIX = ") {
		t.Errorf("berth writes this key with the berth's database name, so a second writer fails add:\n%s", out)
	}
}

func TestViteIsNotAppliedForAnOverride(t *testing.T) {
	r := repoWith(t, map[string]string{"package.json": `{"pnpm": {"overrides": {"vite": "^5.0"}}}`}, nil)
	if out := render(t, "", r, initconf.Input{}); strings.Contains(out, "vite preset") {
		t.Errorf("an override alone applied the preset:\n%s", out)
	}
}

func TestViteIsAppliedForADependency(t *testing.T) {
	r := repoWith(t, map[string]string{"package.json": `{"dependencies": {"vite": "^5.0"}}`}, nil)
	if out := render(t, "", r, initconf.Input{}); !strings.Contains(out, "vite preset") {
		t.Errorf("a dependency on vite didn't apply the preset:\n%s", out)
	}
}
