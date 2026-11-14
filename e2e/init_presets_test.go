//go:build e2e

package e2e

import (
	"maps"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

const laravelEnv = `APP_NAME=Shop
APP_URL=http://localhost:8000
SERVER_PORT=8000
SESSION_DOMAIN=null
DB_CONNECTION=mysql
DB_HOST=127.0.0.1
DB_PORT=3306
DB_DATABASE=app_central
DB_DATABASE_PREFIX=app_
DB_USERNAME=root
DB_PASSWORD=secret
REDIS_HOST=127.0.0.1
REDIS_PORT=6379
REDIS_PREFIX=app_database_
CACHE_PREFIX=app_cache_
`

const viteEnv = "VITE_PORT=5173\n"

const databaseConfig = `<?php
return [
    'connections' => ['mysql' => [
        'host' => env('DB_HOST'),
        'database' => env('DB_DATABASE'),
    ]],
    'redis' => ['options' => ['prefix' => env('REDIS_PREFIX')]],
    'tenant_prefix' => env('DB_DATABASE_PREFIX'),
];
`

func laravelFiles() map[string]string {
	return map[string]string{
		"composer.json":       `{"require": {"php": "^8.3", "laravel/framework": "^11.0"}, "scripts": {"dev": "php artisan serve"}}`,
		"artisan":             "#!/usr/bin/env php\n<?php\n",
		"bootstrap/app.php":   "<?php\n",
		"config/app.php":      "<?php\nreturn ['url' => env('APP_URL')];\n",
		"config/cache.php":    "<?php\nreturn ['prefix' => env('CACHE_PREFIX')];\n",
		"config/session.php":  "<?php\nreturn ['domain' => env('SESSION_DOMAIN')];\n",
		"config/database.php": databaseConfig,
		"config/tenancy.php":  "<?php\nreturn ['prefix' => env('DB_DATABASE_PREFIX')];\n",
	}
}

func viteFiles() map[string]string {
	return map[string]string{
		"package.json":   `{"private": true, "scripts": {"dev": "vite"}, "devDependencies": {"vite": "^5.0"}}`,
		"vite.config.js": "export default { server: { port: Number(process.env.VITE_PORT) } }\n",
	}
}

func laravelAndViteFiles() map[string]string {
	files := laravelFiles()
	maps.Copy(files, viteFiles())
	return files
}

// presetRepo is a repository with no compose project holding files, committed, and env
// as main's gitignored .env, so nothing in it varies between runs.
func presetRepo(t *testing.T, files map[string]string, env string) *Repo {
	t.Helper()
	repo := NewRepo(t)
	repo.Git(t, "rm", "-rq", "compose.yml", "web")
	for path, content := range files {
		writeFile(t, filepath.Join(repo.Dir, path), content)
	}
	writeFile(t, filepath.Join(repo.Dir, ".env"), env)
	repo.Git(t, "add", "-A")
	repo.Git(t, "commit", "-q", "-m", "app")
	return repo
}

// plainRepo keeps the compose fixture, fixed ports and manifests that name neither
// framework, so a preset that fires on the mere presence of a manifest shows up.
func plainRepo(t *testing.T) *Repo {
	t.Helper()
	repo := NewRepo(t)
	writeFile(t, filepath.Join(repo.Dir, "composer.json"), `{"require": {"monolog/monolog": "^3.0"}}`)
	writeFile(t, filepath.Join(repo.Dir, "package.json"), `{"private": true, "dependencies": {"express": "^4.0"}}`)
	writeFile(t, filepath.Join(repo.Dir, ".env"), "WEB_PORT=18080\nSEARCH_PORT=17700\nDB_PORT=13306\nAPP_URL=http://localhost:18080\n")
	repo.Git(t, "add", "-A")
	repo.Git(t, "commit", "-q", "-m", "manifests")
	return repo
}

func golden(t *testing.T, name string) string {
	t.Helper()
	return readString(t, filepath.Join("testdata", "init-golden", name))
}

type presetConfig struct {
	Env struct {
		URL  []string          `toml:"url"`
		Host []string          `toml:"host"`
		Set  map[string]string `toml:"set"`
	} `toml:"env"`
	Ports map[string]struct {
		Env string `toml:"env"`
	} `toml:"ports"`
	State struct {
		MySQL *struct {
			From   string `toml:"from"`
			Prefix string `toml:"prefix"`
		} `toml:"mysql"`
	} `toml:"state"`
}

func decodePresets(t *testing.T, content string) presetConfig {
	t.Helper()
	var c presetConfig
	if _, err := toml.Decode(content, &c); err != nil {
		t.Fatalf("init's config is not TOML: %v\n%s", err, content)
	}
	return c
}

func (c presetConfig) viteEnvKey() string {
	for _, p := range c.Ports {
		if strings.Contains(p.Env, "VITE") {
			return p.Env
		}
	}
	return ""
}

func presetComment(content, preset string) int {
	offset := 0
	for _, line := range strings.SplitAfter(content, "\n") {
		if strings.HasPrefix(line, "#") && strings.Contains(strings.ToLower(line), preset) {
			return offset
		}
		offset += len(line)
	}
	return -1
}

func requirePreset(t *testing.T, content, preset string) int {
	t.Helper()
	at := presetComment(content, preset)
	if at < 0 {
		t.Fatalf("no comment names the %s preset:\n%s", preset, content)
	}
	return at
}

// In a Laravel repository the laravel preset's suggestions come under a
// comment naming it.
func TestInitWritesLaravelSuggestionsUnderACommentNamingThePreset(t *testing.T) {
	repo := presetRepo(t, laravelFiles(), laravelEnv)
	content := initRun(t, repo, repo.Dir).Config
	at := requirePreset(t, content, "laravel")
	for _, token := range []string{"SERVER_PORT", "[state.mysql]", "REDIS_PREFIX", "CACHE_PREFIX"} {
		i := strings.Index(content, token)
		if i < 0 {
			t.Errorf("init doesn't write %s:\n%s", token, content)
		} else if i < at {
			t.Errorf("%s comes before the comment naming the preset:\n%s", token, content)
		}
	}
	if !strings.Contains(content, "APP_URL") {
		t.Errorf("init doesn't write APP_URL:\n%s", content)
	}
}

// The file the preset produced passes doctor in main as written.
func TestInitLaravelConfigPassesDoctorInMain(t *testing.T) {
	repo := presetRepo(t, laravelFiles(), laravelEnv)
	requirePreset(t, initRun(t, repo, repo.Dir).Config, "laravel")
	expectClean(t, doctor(t, repo, nil, repo.Dir), "in main after init with the laravel preset")
}

// A host-only session cookie means no domain, because a shared one would log a
// browser into every berth at once.
func TestInitLaravelLeavesSessionCookiesHostOnly(t *testing.T) {
	repo := presetRepo(t, laravelFiles(), laravelEnv)
	content := initRun(t, repo, repo.Dir).Config
	requirePreset(t, content, "laravel")
	c := decodePresets(t, content)
	if v, ok := c.Env.Set["SESSION_DOMAIN"]; ok && v != "" && v != "null" {
		t.Errorf("[env.set] SESSION_DOMAIN = %q, want the cookie left host-only:\n%s", v, content)
	}
	for _, list := range [][]string{c.Env.URL, c.Env.Host} {
		for _, key := range list {
			if key == "SESSION_DOMAIN" {
				t.Errorf("[env] maps SESSION_DOMAIN to the berth's host, want the cookie left host-only:\n%s", content)
			}
		}
	}
}

// A repository with only Vite gets the vite preset, and it passes doctor.
func TestInitAppliesTheVitePresetInAViteRepository(t *testing.T) {
	repo := presetRepo(t, viteFiles(), viteEnv)
	content := initRun(t, repo, repo.Dir).Config
	requirePreset(t, content, "vite")
	if at := presetComment(content, "laravel"); at >= 0 {
		t.Errorf("a repository with only Vite got the laravel preset:\n%s", content)
	}
	if decodePresets(t, content).viteEnvKey() == "" {
		t.Errorf("init gives the Vite dev server no port with an env key:\n%s", content)
	}
	expectClean(t, doctor(t, repo, nil, repo.Dir), "in main after init with the vite preset")
}

// A repository that is neither gets exactly what init wrote before presets.
// The golden was captured from the code before presets existed.
func TestInitInARepositoryThatIsNeitherMatchesTheOutputBeforePresets(t *testing.T) {
	repo := plainRepo(t)
	if got, want := initRun(t, repo, repo.Dir).Config, golden(t, "plain.toml"); got != want {
		t.Errorf("init in a repository that is neither wrote:\n%s\nwant, byte for byte:\n%s", got, want)
	}
}

// --no-presets holds where presets would otherwise apply, the case that
// tells the flag from one parsed and ignored.
func TestInitNoPresetsAppliesNoneInARepositoryThePresetsMatch(t *testing.T) {
	repo := presetRepo(t, laravelAndViteFiles(), laravelEnv+viteEnv)
	if got, want := initRun(t, repo, repo.Dir, "--no-presets").Config, golden(t, "laravel-vite.toml"); got != want {
		t.Errorf("init --no-presets in a Laravel and Vite repository wrote:\n%s\nwant, byte for byte:\n%s", got, want)
	}
}

// --preset vite applies vite and leaves out laravel, whose files are there.
func TestInitPresetViteAppliesOnlyVite(t *testing.T) {
	repo := presetRepo(t, laravelAndViteFiles(), laravelEnv+viteEnv)
	content := initRun(t, repo, repo.Dir, "--preset", "vite").Config
	requirePreset(t, content, "vite")
	if decodePresets(t, content).viteEnvKey() == "" {
		t.Errorf("--preset vite gives the dev server no port with an env key:\n%s", content)
	}
	if presetComment(content, "laravel") >= 0 {
		t.Errorf("--preset vite also applied laravel:\n%s", content)
	}
	for _, token := range []string{"SERVER_PORT", "state.mysql", "REDIS_PREFIX", "CACHE_PREFIX"} {
		if strings.Contains(content, token) {
			t.Errorf("--preset vite wrote %s, which is laravel's:\n%s", token, content)
		}
	}
}

// On a repository with central and tenant databases under a prefix, Redis
// and a Vite dev server, the config init wrote is all a berth needs to be separate.
func TestInitLaravelConfigGivesABerthItsOwnDatabasesPrefixesAndPorts(t *testing.T) {
	repo := presetRepo(t, laravelAndViteFiles(), laravelEnv+viteEnv)
	m := startHostMySQL(t, repo, "")
	m.create(t, "app_central", "app_1", "shop")
	m.sql(t, "CREATE TABLE app_central.users (id INT PRIMARY KEY); INSERT INTO app_central.users VALUES (1)")
	pointMainAt(t, repo, m)
	mainEnv := readDotEnv(t, filepath.Join(repo.Dir, ".env"))

	content := initRun(t, repo, repo.Dir).Config
	repo.Git(t, "add", "berth.toml")
	repo.Git(t, "commit", "-q", "-m", "berth init")

	wtX := repo.AddWorktree(t, "feat-x")
	wtY := repo.AddWorktree(t, "feat-y")
	mustBerth(t, repo, wtX, "add", "--wait")
	mustBerth(t, repo, wtY, "add", "--wait")
	x, y := blockEnv(t, filepath.Join(wtX, ".env")), blockEnv(t, filepath.Join(wtY, ".env"))

	t.Run("databases under the prefix", func(t *testing.T) {
		if x["DB_DATABASE_PREFIX"] == "" || x["DB_DATABASE_PREFIX"] == mainEnv["DB_DATABASE_PREFIX"] {
			t.Fatalf("the berth's DB_DATABASE_PREFIX = %q, want its own; config:\n%s", x["DB_DATABASE_PREFIX"], content)
		}
		p := x["DB_DATABASE_PREFIX"]
		mustHave(t, m, "after add", p+"central", p+"1")
		if x["DB_DATABASE"] != p+"central" {
			t.Errorf("the berth's DB_DATABASE = %q, want %scentral", x["DB_DATABASE"], p)
		}
		mustHave(t, m, "after add", "app_central", "app_1", "shop")
		if got := m.sql(t, "SELECT COUNT(*) FROM app_central.users"); got != "1" {
			t.Errorf("main's app_central.users has %s rows after add, want 1", got)
		}
	})

	for _, key := range []string{"REDIS_PREFIX", "CACHE_PREFIX"} {
		t.Run(key, func(t *testing.T) {
			if x[key] == "" || x[key] == mainEnv[key] || x[key] == y[key] {
				t.Errorf("%s: berth feat-x %q, berth feat-y %q, main %q; want each berth its own; config:\n%s", key, x[key], y[key], mainEnv[key], content)
			}
		})
	}

	viteKey := decodePresets(t, content).viteEnvKey()
	if viteKey == "" {
		t.Fatalf("init gives the Vite dev server no port with an env key:\n%s", content)
	}
	for _, key := range []string{"SERVER_PORT", viteKey} {
		t.Run(key, func(t *testing.T) {
			if _, err := strconv.Atoi(x[key]); err != nil {
				t.Fatalf("berth feat-x's %s = %q, want a port; config:\n%s", key, x[key], content)
			}
			if x[key] == mainEnv[key] || x[key] == y[key] {
				t.Errorf("%s: berth feat-x %q, berth feat-y %q, main %q; want each berth its own", key, x[key], y[key], mainEnv[key])
			}
		})
	}
}

// A suggestion a file confirms is active. config/database.php reads
// DB_DATABASE_PREFIX.
func TestInitWritesASuggestionAFileConfirmsActive(t *testing.T) {
	repo := presetRepo(t, laravelFiles(), laravelEnv)
	content := initRun(t, repo, repo.Dir).Config
	requirePreset(t, content, "laravel")
	mysql := decodePresets(t, content).State.MySQL
	if mysql == nil || mysql.Prefix != "env:DB_DATABASE_PREFIX" {
		t.Errorf("[state.mysql] prefix isn't active as env:DB_DATABASE_PREFIX:\n%s", content)
	}
}

// The same suggestion is commented with its reason when no config file
// reads the key, even with the key in .env.
func TestInitCommentsAnUnconfirmedSuggestionWithItsReason(t *testing.T) {
	files := laravelFiles()
	delete(files, "config/tenancy.php")
	files["config/database.php"] = strings.Replace(databaseConfig, "    'tenant_prefix' => env('DB_DATABASE_PREFIX'),\n", "", 1)
	repo := presetRepo(t, files, laravelEnv)
	content := initRun(t, repo, repo.Dir).Config
	requirePreset(t, content, "laravel")

	if mysql := decodePresets(t, content).State.MySQL; mysql != nil && mysql.Prefix != "" {
		t.Errorf("[state.mysql] prefix is active as %q with no config file reading the key:\n%s", mysql.Prefix, content)
	}
	const suggestion = `prefix = "env:DB_DATABASE_PREFIX"`
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		rest, ok := strings.CutPrefix(line, "# "+suggestion)
		if !ok {
			continue
		}
		trailing := strings.HasPrefix(strings.TrimSpace(rest), "#")
		above := i > 0 && strings.HasPrefix(lines[i-1], "# ") && !strings.Contains(lines[i-1], " = ")
		if !trailing && !above {
			t.Errorf("the commented suggestion has no reason beside or above it:\n%s", content)
		}
		return
	}
	t.Errorf("init doesn't write the commented line %q:\n%s", "# "+suggestion, content)
}

// APP_URL=http://localhost:8000 is suggested by detection, which sees the
// host, and by the preset; the key is written once.
func TestInitWritesAKeyDetectionAndAPresetBothSuggestOnce(t *testing.T) {
	repo := presetRepo(t, laravelFiles(), laravelEnv)
	content := initRun(t, repo, repo.Dir).Config
	requirePreset(t, content, "laravel")
	written := regexp.MustCompile(`(?m)^(# )?(APP_URL = |.*"APP_URL")`).FindAllString(content, -1)
	if len(written) != 1 {
		t.Errorf("APP_URL is written %d times, want once: %q\n%s", len(written), written, content)
	}
}
