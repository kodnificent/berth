package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/worktree"
)

func layers(t *testing.T, contents ...string) []string {
	t.Helper()
	dir := t.TempDir()
	var paths []string
	for i, c := range contents {
		p := filepath.Join(dir, string(rune('a'+i))+".toml")
		if c != "" {
			if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		paths = append(paths, p)
	}
	return paths
}

func mustLoad(t *testing.T, paths ...string) *Config {
	t.Helper()
	c, err := LoadFiles(paths...)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	return c
}

func loadErr(t *testing.T, paths ...string) string {
	t.Helper()
	_, err := LoadFiles(paths...)
	if err == nil {
		t.Fatalf("LoadFiles succeeded, want an error")
	}
	if code := apperr.CodeOf(err); code != apperr.ConfigInvalid {
		t.Fatalf("error code = %s, want config_invalid: %v", code, err)
	}
	return err.Error()
}

func TestTablesMergeAndArraysReplace(t *testing.T) {
	paths := layers(t,
		"[compose]\nexclude = [\"a\"]\n[env.set]\nB = \"high\"\n",
		"[compose]\nexclude = [\"b\", \"c\"]\nfiles = [\"x.yml\"]\n[env.set]\nA = \"low\"\nB = \"low\"\n",
	)
	c := mustLoad(t, paths...)
	if !reflect.DeepEqual(c.Compose.Exclude, []string{"a"}) {
		t.Errorf("exclude = %v, want the higher layer's array whole", c.Compose.Exclude)
	}
	if !reflect.DeepEqual(c.Compose.Files, []string{"x.yml"}) {
		t.Errorf("files = %v, want the lower layer's", c.Compose.Files)
	}
	if want := map[string]string{"A": "low", "B": "high"}; !reflect.DeepEqual(c.Env.Set, want) {
		t.Errorf("env.set = %v, want %v", c.Env.Set, want)
	}
}

func TestMissingFilesUseDefaults(t *testing.T) {
	c := mustLoad(t, layers(t, "", "", "")...)
	if c.Host() != DefaultHost || c.URL() != DefaultURL {
		t.Errorf("host %q url %q, want the defaults", c.Host(), c.URL())
	}
}

func TestPortDefinedInAnotherLayer(t *testing.T) {
	mustLoad(t, layers(t,
		"[compose.ports]\n\"web:80\" = \"assign\"\n",
		"[env.set]\nP = \"{port.web}\"\n",
	)...)
}

func TestErrorNamesTheFileThatSetTheKey(t *testing.T) {
	paths := layers(t,
		"[compose.ports]\n\"web:80\" = \"none\"\n",
		"[env.set]\nP = \"{port.web}\"\n",
	)
	msg := loadErr(t, paths...)
	if !strings.Contains(msg, paths[1]+": env.set.P:") {
		t.Errorf("message %q does not blame %s for env.set.P", msg, paths[1])
	}
}

func TestInvalidConfig(t *testing.T) {
	cases := []struct{ name, content, want string }{
		{"syntax", "[berth\n", "line"},
		{"unknown key", "[berth]\nhots = \"x\"\n", "unknown key berth.hots"},
		{"unknown table reported once", "[nope]\na = 1\n", "unknown key nope\n"},
		{"wrong type", "[berth]\nhost = 3\n", "berth.host"},
		{"unknown nested key", "[ports.vite]\nenv = \"VITE_PORT\"\nport = 1\n", "ports.vite.port"},
		{"port key shape", "[compose.ports]\nweb = \"assign\"\n", "compose.ports.web"},
		{"port policy", "[compose.ports]\n\"web:80\" = \"sometimes\"\n", "unknown port policy"},
		{"volume role", "[compose.volumes]\nmysql = \"copy\"\n", "unknown volume role"},
		{"env name", "[env.set]\n\"1BAD\" = \"x\"\n", "env.set.1BAD"},
		{"env list name", "[env]\nurl = [\"APP URL\"]\n", "env.url"},
		{"clone outside", "[files]\nclone = [\"../x\"]\n", "files.clone"},
		{"unknown token", "[env.set]\nA = \"{nope}\"\n", "{nope}"},
		{"unclosed brace", "[env.set]\nA = \"{name\"\n", "unclosed"},
		{"stray brace", "[env.set]\nA = \"x}\"\n", "unmatched"},
		{"host may not use url", "[berth]\nhost = \"{url}\"\n", "berth.host"},
		{"host may not use a port", "[compose.ports]\n\"web:80\" = \"assign\"\n[berth]\nhost = \"{port.web}\"\n", "berth.host"},
		{"undefined port", "[env.set]\nA = \"{port.web}\"\n", "define web"},
		{"ephemeral port", "[compose.ports]\n\"mail:8025\" = \"ephemeral\"\n[env.set]\nA = \"{port.mail}\"\n", "policy \"ephemeral\""},
		{"ambiguous service", "[compose.ports]\n\"web:80\" = \"assign\"\n\"web:443\" = \"assign\"\n[env.set]\nA = \"{port.web}\"\n", "more than one assigned port"},
		{"port name clash", "[compose.ports]\n\"web:80\" = \"assign\"\n[ports.web]\nenv = \"WEB_PORT\"\n", "ports.web"},
		{"pool shape", "[pool]\nports = \"20000\"\n", "pool.ports"},
		{"pool order", "[pool]\nports = \"30000-20000\"\n", "pool.ports"},
		{"pool bounds", "[pool]\nports = \"0-70000\"\n", "pool.ports"},
		{"min free unit", "[clone]\nmin_free = \"10\"\n", "clone.min_free"},
		{"min free type", "[clone]\nmin_free = 10\n", "min_free"},
		{"host without name", "[berth]\nhost = \"myapp.localhost\"\n", "must contain {name}"},
		{"host not a hostname", "[berth]\nhost = \"{name}_x.localhost\"\n", "not a valid hostname"},
		{"compose project key", "[env.set]\nCOMPOSE_PROJECT_NAME = \"x\"\n", "env.set.COMPOSE_PROJECT_NAME"},
		{"compose file key", "[env]\nhost = [\"COMPOSE_FILE\"]\n", "env.host"},
		{"compose key as port env", "[ports.vite]\nenv = \"COMPOSE_FILE\"\n", "ports.vite.env"},
		{"host label too long", "[berth]\nhost = \"{name}" + strings.Repeat("x", 63) + ".localhost\"\n", "not a valid hostname"},
		{"mysql without from", "[state.mysql]\nprefix = \"env:P\"\n", "state.mysql: needs from"},
		{"mysql from shape", "[state.mysql]\nfrom = \"DB_*\"\n", "state.mysql.from"},
		{"mysql prefix shape", "[state.mysql]\nfrom = \"env:DB_*\"\nprefix = \"DB_DATABASE_PREFIX\"\n", "state.mysql.prefix"},
		{"mysql unknown strategy", "[state.mysql]\nfrom = \"env:DB_*\"\n[state.mysql.databases]\nmyapp = \"clone\"\n", "unknown strategy \"clone\""},
		{"mysql default", "[state.mysql]\nfrom = \"env:DB_*\"\ndefault = \"copy\"\n", "state.mysql.default"},
		{"sqlite outside", "[state.sqlite]\nfiles = [\"../shared.sqlite\"]\n", "state.sqlite.files: \"../shared.sqlite\" is not a database file inside the worktree"},
		{"sqlite absolute", "[state.sqlite]\nfiles = [\"/var/db/app.sqlite\"]\n", "state.sqlite.files"},
		{"sqlite also under files clone", "[files]\nclone = [\"database\"]\n[state.sqlite]\nfiles = [\"database/app.sqlite\"]\n", "state.sqlite.files: \"database/app.sqlite\" is also cloned by [files] clone"},
		{"mysql pattern without prefix", "[state.mysql]\nfrom = \"env:DB_*\"\n[state.mysql.databases]\n\"*\" = \"fresh\"\n", "needs [state.mysql] prefix"},
		{"postgres dump", "[state.postgres]\nfrom = \"env:DB_*\"\n[state.postgres.databases]\ncentral = \"dump\"\n", "state.postgres.databases.central: the dump strategy is MySQL's"},
		{"postgres snapshot default", "[state.postgres]\nfrom = \"env:DB_*\"\ndefault = \"snapshot\"\n", "state.postgres.default: the snapshot strategy is MySQL's"},
		{"postgres seed file", "[state.postgres]\nfrom = \"env:DB_*\"\n[state.postgres.databases]\nseeded = { from = \"file:seed.sql\" }\n", "state.postgres.databases.seeded: a seed file is MySQL's"},
		{"mysql template", "[state.mysql]\nfrom = \"env:DB_*\"\n[state.mysql.databases]\ncentral = \"template\"\n", "state.mysql.databases.central: unknown strategy \"template\""},
		{"mysql disconnect", "[state.mysql]\nfrom = \"env:DB_*\"\ndisconnect = true\n", "state.mysql.disconnect: unknown key"},
		{"postgres disconnect type", "[state.postgres]\nfrom = \"env:DB_*\"\ndisconnect = \"yes\"\n", "disconnect"},
		{"mysql and postgres", "[state.mysql]\nfrom = \"env:DB_*\"\n[state.postgres]\nfrom = \"env:PG_*\"\n", "state.postgres: a project can name one host database server"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			paths := layers(t, c.content)
			msg := loadErr(t, paths...) + "\n"
			if !strings.Contains(msg, paths[0]) {
				t.Errorf("message %q does not name %s", msg, paths[0])
			}
			if !strings.Contains(msg, c.want) {
				t.Errorf("message %q does not contain %q", msg, c.want)
			}
		})
	}
}

func TestPostgresTemplateDatabasesMayDisconnectMainsSessions(t *testing.T) {
	c := mustLoad(t, layers(t, "[state.postgres]\nfrom = \"env:DB_*\"\ndisconnect = true\n[state.postgres.databases]\ncentral = \"template\"\n")...)
	host, _ := c.HostDB()
	if host.Strategy("central") != Template || !host.Disconnects() {
		t.Errorf("central is %q with disconnect %v, want template with disconnect", host.Strategy("central"), host.Disconnects())
	}
}

func TestValidConfig(t *testing.T) {
	mustLoad(t, layers(t, `[berth]
host = "{name}.myapp.localhost"
url = "https://{host}:{port.web:443}"

[compose]
files = [".worktree/compose.yml"]
exclude = ["mailpit"]

[compose.ports]
"web:80" = "assign"
"web:443" = "assign"
"mailpit:8025" = "ephemeral"
"db:3306" = "none"

[compose.volumes]
mysql = "clone"
composer_cache = "share"
redis = "fresh"

[files]
clone = ["vendor", "storage/app"]

[env]
url = ["APP_URL"]
host = ["SANCTUM_STATEFUL_DOMAINS"]

[env.set]
VITE_REVERB_PORT = "{port.web:80}"
VITE_PORT = "{port.vite}"
JSON = "{{\"a\": 1}}"

[ports]
vite = { env = "VITE_PORT" }

[hooks]
post-add = "composer install"
pre-remove = "true"
`)...)
}

func TestResolve(t *testing.T) {
	c := mustLoad(t, layers(t, `[berth]
host = "{name}.myapp.localhost"
url = "http://{host}:{port.web}"

[compose.ports]
"web:80" = "assign"

[env]
url = ["APP_URL", "ORIGIN"]
host = ["SANCTUM"]

[env.set]
ORIGIN = "https://{host}"
ORIGINS = "{host},app.{host}"
PORT = "{port.web}"
LITERAL = "{{x}}"
`)...)

	e := c.Resolve("feat-x", map[string]int{"web": 40001, "web:80": 40001})
	want := map[string]string{
		"APP_URL": "http://feat-x.myapp.localhost:40001",
		"ORIGIN":  "https://feat-x.myapp.localhost",
		"SANCTUM": "feat-x.myapp.localhost",
		"ORIGINS": "feat-x.myapp.localhost,app.feat-x.myapp.localhost",
		"PORT":    "40001",
		"LITERAL": "{x}",
	}
	if !reflect.DeepEqual(e.Env, want) {
		t.Errorf("env = %v, want %v", e.Env, want)
	}
	if e.URL != "http://feat-x.myapp.localhost:40001" || len(e.Unresolved) != 0 {
		t.Errorf("url %q unresolved %v", e.URL, e.Unresolved)
	}

	e = c.Resolve("feat-x", nil)
	if e.URL != "" {
		t.Errorf("url = %q with no ports assigned, want none", e.URL)
	}
	wantUnresolved := map[string]string{
		"url":         "http://{host}:{port.web}",
		"env.APP_URL": "{url}",
		"env.PORT":    "{port.web}",
	}
	if !reflect.DeepEqual(e.Unresolved, wantUnresolved) {
		t.Errorf("unresolved = %v, want %v", e.Unresolved, wantUnresolved)
	}
	if e.Env["ORIGIN"] != "https://feat-x.myapp.localhost" || e.Env["SANCTUM"] != "feat-x.myapp.localhost" {
		t.Errorf("values that need no port should still resolve: %v", e.Env)
	}
}

func TestUserPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	if p, _ := UserPath(); p != filepath.Join(home, ".config", "berth", "config.toml") {
		t.Errorf("UserPath = %q, want under ~/.config", p)
	}
	t.Setenv("XDG_CONFIG_HOME", "relative")
	if p, _ := UserPath(); p != filepath.Join(home, ".config", "berth", "config.toml") {
		t.Errorf("UserPath = %q with a relative XDG_CONFIG_HOME, want ~/.config", p)
	}
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if p, _ := UserPath(); p != filepath.Join(xdg, "berth", "config.toml") {
		t.Errorf("UserPath = %q, want under XDG_CONFIG_HOME", p)
	}
}

func TestPoolRange(t *testing.T) {
	if lo, hi := mustLoad(t, layers(t, "")...).PoolRange(); lo != 20000 || hi != 29999 {
		t.Errorf("default pool = %d-%d, want 20000-29999", lo, hi)
	}
	if lo, hi := mustLoad(t, layers(t, "[pool]\nports = \"41000-41010\"\n")...).PoolRange(); lo != 41000 || hi != 41010 {
		t.Errorf("pool = %d-%d, want 41000-41010", lo, hi)
	}
}

func TestMinFree(t *testing.T) {
	if got := mustLoad(t, layers(t, "")...).MinFree(); got != 10e9 {
		t.Errorf("default min_free = %d, want 10GB", got)
	}
	if got := mustLoad(t, layers(t, "[clone]\nmin_free = \"512MiB\"\n")...).MinFree(); got != 512<<20 {
		t.Errorf("min_free = %d, want 512MiB", got)
	}
}

func TestNameMaxKeepsLabelsValid(t *testing.T) {
	for host, want := range map[string]int{
		"{name}.localhost": 40,
		"app-" + strings.Repeat("x", 30) + "-{name}.localhost": 63 - 35,
		"{name}-{name}.localhost":                              31,
	} {
		c := mustLoad(t, layers(t, "[berth]\nhost = \""+host+"\"\n")...)
		if got := c.NameMax(); got != want {
			t.Errorf("NameMax for %q = %d, want %d", host, got, want)
		}
	}
}

func TestPortValuesAddServiceAliases(t *testing.T) {
	c := mustLoad(t, layers(t, "[compose.ports]\n\"web:80\" = \"assign\"\n\"api:80\" = \"assign\"\n\"api:443\" = \"assign\"\n")...)
	got := c.PortValues(map[string]int{"web:80": 20001, "api:80": 20002, "api:443": 20003, "vite": 20004})
	want := map[string]int{"web:80": 20001, "web": 20001, "api:80": 20002, "api:443": 20003, "vite": 20004}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PortValues = %v, want %v", got, want)
	}
}

func TestPersonalPath(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "app")
	common := filepath.Join(main, ".git")
	if err := os.MkdirAll(common, 0o755); err != nil {
		t.Fatal(err)
	}
	linked := &worktree.Worktree{Path: filepath.Join(root, "feat"), AdminDir: filepath.Join(common, "worktrees", "feat"), CommonDir: common, Repository: main}
	local, hidden := filepath.Join(main, LocalName), filepath.Join(common, "berth.toml")

	if p, ok, err := Personal(linked); err != nil || ok || p != local {
		t.Errorf("with no personal config, Personal = %q, %v, %v; want %q, false", p, ok, err, local)
	}
	if err := os.WriteFile(hidden, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if p, ok, err := Personal(linked); err != nil || !ok || p != hidden {
		t.Errorf("with .git/berth.toml, Personal = %q, %v, %v; want %q, true", p, ok, err, hidden)
	}
	if err := os.Rename(hidden, local); err != nil {
		t.Fatal(err)
	}
	if p, ok, err := Personal(linked); err != nil || !ok || p != local {
		t.Errorf("with berth.local.toml, Personal = %q, %v, %v; want %q, true", p, ok, err, local)
	}
	paths, err := Paths(linked)
	if err != nil || paths[0] != local || paths[1] != filepath.Join(linked.Path, "berth.toml") {
		t.Errorf("Paths = %v, %v; want main's berth.local.toml, then the worktree's berth.toml", paths, err)
	}

	bare := filepath.Join(root, "bare.git")
	inBare := &worktree.Worktree{Path: filepath.Join(root, "wt"), AdminDir: filepath.Join(bare, "worktrees", "wt"), CommonDir: bare, Repository: bare}
	if p, ok, err := Personal(inBare); err != nil || ok || p != filepath.Join(bare, "berth.toml") {
		t.Errorf("in a bare layout, Personal = %q, %v, %v; want the hidden file", p, ok, err)
	}
}

func TestBothPersonalConfigsAreInvalid(t *testing.T) {
	main := t.TempDir()
	common := filepath.Join(main, ".git")
	w := &worktree.Worktree{Path: main, AdminDir: common, CommonDir: common, Repository: main}
	local, hidden := filepath.Join(main, LocalName), filepath.Join(common, "berth.toml")
	for _, p := range []string{local, hidden} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("[berth]\nhost = \"{name}.localhost\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, err := range map[string]error{
		"Personal": func() error { _, _, err := Personal(w); return err }(),
		"Load":     func() error { _, err := Load(w); return err }(),
	} {
		if apperr.CodeOf(err) != apperr.ConfigInvalid {
			t.Fatalf("%s with both personal configs: %v, want config_invalid", name, err)
		}
		if msg := err.Error(); !strings.HasPrefix(msg, local+": ") || !strings.Contains(msg, hidden) {
			t.Errorf("%s error = %q, want it to start with %s and name %s", name, msg, local, hidden)
		}
	}
}

func TestDatabaseStrategyPrefersTheClosestEntry(t *testing.T) {
	m := mustLoad(t, layers(t, `[state.mysql]
from = "env:DB_*"
prefix = "env:DB_DATABASE_PREFIX"

[state.mysql.databases]
central = "none"
"tenant*" = "skip"
"*" = "fresh"
`)...).State.MySQL
	for key, want := range map[string]string{"central": "none", "tenant7": "skip", "1": "fresh"} {
		if got := m.Strategy(key); got != want {
			t.Errorf("Strategy(%q) = %q, want %q", key, got, want)
		}
	}
	m.Databases = map[string]any{"central": "fresh"}
	if got := m.Strategy("other"); got != "skip" {
		t.Errorf("Strategy of an unlisted database with no default = %q, want skip", got)
	}
}

func TestDatabaseEntryNamesASeedFileOrACommand(t *testing.T) {
	m := mustLoad(t, layers(t, `[state.mysql]
from = "env:DB_*"

[state.mysql.databases]
tenant = { from = "file:database/seeds/tenant.sql" }
reports = { command = "./scripts/copy-reports.sh" }
`)...).State.MySQL
	if e := m.Entry("tenant"); e.Strategy != File || e.File != "database/seeds/tenant.sql" {
		t.Errorf("tenant = %+v", e)
	}
	if e := m.Entry("reports"); e.Strategy != Command || e.Command != "./scripts/copy-reports.sh" {
		t.Errorf("reports = %+v", e)
	}
}

func TestDatabaseEntryFromAHigherLayerReplacesTheTableWhole(t *testing.T) {
	m := mustLoad(t, layers(t, `[state.mysql.databases]
tenant = { from = "file:higher.sql" }
`, `[state.mysql]
from = "env:DB_*"

[state.mysql.databases]
tenant = { command = "./lower.sh" }
`)...).State.MySQL
	if e := m.Entry("tenant"); e.Strategy != File || e.File != "higher.sql" {
		t.Errorf("tenant = %+v, want the higher layer's seed file alone", e)
	}
}

func TestInvalidDatabaseEntriesAreRefused(t *testing.T) {
	for name, entry := range map[string]string{
		"from without file:":    `x = { from = "database/seeds/a.sql" }`,
		"from empty path":       `x = { from = "file:" }`,
		"both from and command": `x = { from = "file:a.sql", command = "b.sh" }`,
		"empty command":         `x = { command = "" }`,
		"bare command word":     `x = "command"`,
		"unknown table key":     `x = { script = "b.sh" }`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "berth.toml")
			config := "[state.mysql]\nfrom = \"env:DB_*\"\n\n[state.mysql.databases]\n" + entry + "\n"
			if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFiles(path); err == nil {
				t.Errorf("%s was accepted", entry)
			}
		})
	}
}
