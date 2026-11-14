//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

type initJSON struct {
	Path     string    `json:"path"`
	Written  bool      `json:"written"`
	Config   string    `json:"config"`
	Findings []finding `json:"findings"`
}

type initConfig struct {
	Compose struct {
		Ports   map[string]string `toml:"ports"`
		Volumes map[string]string `toml:"volumes"`
	} `toml:"compose"`
	Env struct {
		Set map[string]string `toml:"set"`
	} `toml:"env"`
}

func initRun(t *testing.T, repo *Repo, dir string, args ...string) initJSON {
	t.Helper()
	res := mustBerth(t, repo, dir, append([]string{"init", "--json"}, args...)...)
	var out initJSON
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		t.Fatalf("berth init --json: %v\n%s", err, res.Stdout)
	}
	return out
}

func decodeInit(t *testing.T, content string) initConfig {
	t.Helper()
	var c initConfig
	if _, err := toml.Decode(content, &c); err != nil {
		t.Fatalf("init's config is not TOML: %v\n%s", err, content)
	}
	return c
}

func gitClean(t *testing.T, repo *Repo) {
	t.Helper()
	if out := repo.Git(t, "status", "--porcelain", "--untracked-files=all"); out != "" {
		t.Errorf("git status in main is not clean:\n%s", out)
	}
}

func excludeFile(repo *Repo) string { return filepath.Join(repo.Dir, ".git", "info", "exclude") }

func readOrEmpty(path string) string {
	data, _ := os.ReadFile(path)
	return string(data)
}

// publishedInMain lists "<service>:<container port>" for every port main's compose
// project publishes on a fixed host port, as compose itself resolves it.
func publishedInMain(t *testing.T, repo *Repo) []string {
	t.Helper()
	var keys []string
	for name, svc := range services(t, composeConfig(t, repo, repo.Dir)) {
		ports, _ := svc["ports"].([]any)
		for _, p := range ports {
			m := p.(map[string]any)
			if pub, _ := m["published"].(string); pub != "" {
				keys = append(keys, fmt.Sprintf("%s:%v", name, m["target"]))
			}
		}
	}
	if len(keys) == 0 {
		t.Fatal("main's compose project publishes no port; the fixture changed")
	}
	return keys
}

// On the fixture, init writes a berth.toml that loads, and with only that
// config a new worktree's berth comes up with no further config.
func TestInitConfigProvisionsABerth(t *testing.T) {
	repo := NewRepo(t)
	out := initRun(t, repo, repo.Dir)
	path := repoConfig(repo.Dir)
	if out.Path != path || !out.Written {
		t.Fatalf("init --json = path %q written %v, want %s written", out.Path, out.Written, path)
	}
	if got := readOrEmpty(path); got != out.Config || got == "" {
		t.Fatalf("berth.toml holds %q, want init's config %q", got, out.Config)
	}
	if out.Findings == nil {
		t.Errorf("init --json has no findings from doctor in main:\n%+v", out)
	}
	expectClean(t, doctor(t, repo, nil, repo.Dir), "in main after init")

	repo.Git(t, "add", "berth.toml")
	repo.Git(t, "commit", "-q", "-m", "berth init")
	wt := repo.AddWorktree(t, "feat-init")
	mustBerth(t, repo, wt, "add", "--wait")
	repo.Compose(t, wt, "up", "-d", "--wait")
	if up := running(t, portsStatusOf(t, repo, wt).ComposeProject); len(up) == 0 {
		t.Errorf("no service of the berth is running after docker compose up")
	}
}

// Every published port is an assign entry and every named volume has a
// role; a package-manager cache volume is share.
func TestInitDetectsPortsAndVolumes(t *testing.T) {
	repo := NewRepo(t)
	compose := filepath.Join(repo.Dir, "compose.yml")
	data := readString(t, compose)
	data = strings.Replace(data, "      - cache:/cache\n", "      - cache:/cache\n      - npm_cache:/root/.npm\n", 1)
	data = strings.Replace(data, "\n  cache:\n", "\n  cache:\n  npm_cache:\n", 1)
	writeFile(t, compose, data)
	repo.Git(t, "commit", "-q", "-am", "npm cache")

	c := decodeInit(t, initRun(t, repo, repo.Dir).Config)
	for _, key := range publishedInMain(t, repo) {
		if c.Compose.Ports[key] != "assign" {
			t.Errorf("[compose.ports] %q = %q, want assign (ports: %v)", key, c.Compose.Ports[key], c.Compose.Ports)
		}
	}
	want := map[string]string{"db-data": "clone", "cache": "clone", "npm_cache": "share"}
	for key := range composeConfig(t, repo, repo.Dir)["volumes"].(map[string]any) {
		if c.Compose.Volumes[key] == "" {
			t.Errorf("volume %s has no role in [compose.volumes]: %v", key, c.Compose.Volumes)
		}
	}
	for key, role := range want {
		if c.Compose.Volumes[key] != role {
			t.Errorf("[compose.volumes] %s = %q, want %q", key, c.Compose.Volumes[key], role)
		}
	}
}

// Env keys whose values in main's .env hold main's host or a published port
// are commented suggestions naming the template that would replace the value.
func TestInitSuggestsEnvTemplates(t *testing.T) {
	repo := NewRepo(t)
	env := readDotEnv(t, filepath.Join(repo.Dir, ".env"))
	web, db := env["WEB_PORT"], env["DB_PORT"]
	appendFile(t, filepath.Join(repo.Dir, ".env"), fmt.Sprintf(
		"APP_URL=http://localhost:%s\nORIGINS=localhost:%s,app.localhost:%s\nDB_ADDR=127.0.0.1:%s\nCOOKIE_HOST=localhost\nPLAIN=hello\n", web, web, web, db))

	out := initRun(t, repo, repo.Dir)
	suggestions := map[string]string{
		"APP_URL":     `APP_URL = "http://{host}:{port.web}"`,
		"ORIGINS":     `ORIGINS = "{host}:{port.web},app.{host}:{port.web}"`,
		"DB_ADDR":     `DB_ADDR = "127.0.0.1:{port.db}"`,
		"COOKIE_HOST": `COOKIE_HOST = "{host}"`,
	}
	text := out.Config
	for key, line := range suggestions {
		if !strings.Contains(text, "\n# "+line+"\n") {
			t.Errorf("init doesn't suggest %s as the commented line %q:\n%s", key, "# "+line, text)
		}
		if strings.Contains(text, "\n"+line+"\n") {
			t.Errorf("init sets %s instead of suggesting it in a comment", key)
		}
		text = strings.Replace(text, "\n# "+line+"\n", "\n"+line+"\n", 1)
	}
	if strings.Contains(out.Config, "PLAIN") {
		t.Errorf("init mentions PLAIN, whose value holds neither main's host nor a port:\n%s", out.Config)
	}
	if c := decodeInit(t, out.Config); len(c.Env.Set) != 0 {
		t.Errorf("init sets [env.set] keys %v, want only commented suggestions", c.Env.Set)
	}

	writeFile(t, repoConfig(repo.Dir), text)
	expectClean(t, doctor(t, repo, nil, repo.Dir), "in main with init's env suggestions uncommented")
}

// Init never overwrites a config, and --private refuses when the hidden
// personal config exists.
func TestInitRefusesToOverwrite(t *testing.T) {
	repo := NewRepo(t)
	const mine = "# mine\n"
	cases := []struct {
		name, file string
		args       []string
	}{
		{"berth.toml", repoConfig(repo.Dir), nil},
		{"berth.local.toml", repo.localConfig(), []string{"--private"}},
		{".git/berth.toml", repo.privateConfig(), []string{"--private"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			writeFile(t, c.file, mine)
			defer os.Remove(c.file)
			excluded := readOrEmpty(excludeFile(repo))
			for _, extra := range [][]string{nil, {"--json"}} {
				res := repo.Berth(t, repo.Dir, append(append([]string{"init"}, c.args...), extra...)...)
				named := strings.Contains(res.Stderr, c.file)
				if extra != nil {
					var e struct {
						Error struct{ Message string } `json:"error"`
					}
					_ = json.Unmarshal([]byte(res.Stdout), &e)
					named = strings.Contains(e.Error.Message, c.file)
				}
				if res.Exit != 1 || !named {
					t.Errorf("init %v with %s present: exit %d, stdout %q, stderr %q; want exit 1 naming the file", append(c.args, extra...), c.name, res.Exit, res.Stdout, res.Stderr)
				}
			}
			if got := readOrEmpty(c.file); got != mine {
				t.Errorf("init changed %s to %q", c.file, got)
			}
			for _, other := range cases {
				if other.file != c.file && fileExistsAt(other.file) {
					t.Errorf("init refused but created %s", other.file)
				}
			}
			if got := readOrEmpty(excludeFile(repo)); got != excluded {
				t.Errorf("init refused but changed info/exclude to %q", got)
			}
		})
	}
}

// --private writes berth.local.toml and main's git status stays clean.
func TestInitPrivate(t *testing.T) {
	repo := NewRepo(t)
	gitignore := readString(t, filepath.Join(repo.Dir, ".gitignore"))
	out := initRun(t, repo, repo.Dir, "--private")
	if out.Path != repo.localConfig() || !out.Written || readOrEmpty(repo.localConfig()) != out.Config {
		t.Fatalf("init --private = %+v, want berth.local.toml written with its config", out)
	}
	if fileExistsAt(repoConfig(repo.Dir)) || fileExistsAt(repo.privateConfig()) {
		t.Errorf("init --private wrote another config file too")
	}
	gitClean(t, repo)
	if got := readString(t, filepath.Join(repo.Dir, ".gitignore")); got != gitignore {
		t.Errorf("init --private changed .gitignore to %q", got)
	}
	if p := configPath(t, repo, repo.Dir); p != (configPathJSON{"personal", repo.localConfig(), true}) {
		t.Errorf("config path after init --private = %+v, want berth.local.toml in use", p)
	}
	expectClean(t, doctor(t, repo, nil, repo.Dir), "in main after init --private")
}

// --dry-run writes nothing and prints exactly what a real run writes.
func TestInitDryRun(t *testing.T) {
	repo := NewRepo(t)
	for _, c := range []struct {
		args []string
		file string
	}{
		{nil, repoConfig(repo.Dir)},
		{[]string{"--private"}, repo.localConfig()},
	} {
		excluded := readOrEmpty(excludeFile(repo))
		for _, flag := range []string{"--dry-run", "-n"} {
			printed := mustBerth(t, repo, repo.Dir, append(append([]string{"init"}, c.args...), flag)...).Stdout
			j := initRun(t, repo, repo.Dir, append(c.args, flag)...)
			if j.Written || j.Path != c.file || j.Config != printed {
				t.Errorf("init %v %s --json = %+v, want path %s, not written, config as printed", c.args, flag, j, c.file)
			}
			if fileExistsAt(c.file) {
				t.Fatalf("init %v %s wrote %s", c.args, flag, c.file)
			}
			gitClean(t, repo)
			if got := readOrEmpty(excludeFile(repo)); got != excluded {
				t.Errorf("init %v %s changed info/exclude", c.args, flag)
			}
			if flag == "-n" {
				mustBerth(t, repo, repo.Dir, append([]string{"init"}, c.args...)...)
				if got := readOrEmpty(c.file); got != printed {
					t.Errorf("init %v wrote:\n%s\nbut --dry-run printed:\n%s", c.args, got, printed)
				}
				os.Remove(c.file)
			}
		}
	}
}

// Init in a linked worktree or outside a git repository fails with the
// existing error codes.
func TestInitOnlyInMain(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-init")
	res := repo.Berth(t, wt, "init", "--json")
	var e struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	_ = json.Unmarshal([]byte(res.Stdout), &e)
	if res.Exit != 1 || e.Error.Code == "" || !strings.Contains(e.Error.Message, repo.Dir) {
		t.Errorf("init in a linked worktree: exit %d, %s; want exit 1 pointing at main", res.Exit, res.Stdout)
	}
	if fileExistsAt(repoConfig(wt)) || fileExistsAt(repoConfig(repo.Dir)) {
		t.Errorf("init in a linked worktree wrote a config")
	}

	outside := t.TempDir()
	res = repo.Berth(t, outside, "init", "--json")
	_ = json.Unmarshal([]byte(res.Stdout), &e)
	if res.Exit != 3 || e.Error.Code != "worktree_not_found" {
		t.Errorf("init outside a git repository: exit %d, %s; want exit 3, worktree_not_found", res.Exit, res.Stdout)
	}
	if fileExistsAt(filepath.Join(outside, "berth.toml")) {
		t.Errorf("init outside a git repository wrote a config")
	}
}

// Init never reads stdin and never starts a service.
func TestInitReadsNoInputAndStartsNothing(t *testing.T) {
	repo := NewRepo(t)
	for _, args := range [][]string{{"init", "--dry-run"}, {"init"}} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		cmd := exec.CommandContext(ctx, berthBin, args...)
		cmd.Dir, cmd.Env, cmd.Stdin = repo.Dir, repo.Env(), r
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		timedOut := ctx.Err() != nil
		cancel()
		r.Close()
		w.Close()
		if timedOut {
			t.Fatalf("berth %v was still running after 60s with stdin open", args)
		}
		if runErr != nil {
			t.Fatalf("berth %v: %v\n%s", args, runErr, stderr.String())
		}
	}
	if left := dockerLeftovers(t, repo.Project); len(left) > 0 {
		t.Errorf("init created Docker resources:\n%s", strings.Join(left, "\n"))
	}
}
