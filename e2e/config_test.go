//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type configStatus struct {
	ID   string            `json:"id"`
	Name string            `json:"name"`
	Host string            `json:"host"`
	URL  string            `json:"url"`
	Env  map[string]string `json:"env"`
}

func configStatusOf(t *testing.T, repo *Repo, dir string) configStatus {
	t.Helper()
	res := mustBerth(t, repo, dir, "status", "--json")
	var s configStatus
	if err := json.Unmarshal([]byte(res.Stdout), &s); err != nil {
		t.Fatalf("berth status --json: %v\n%s", err, res.Stdout)
	}
	return s
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (r *Repo) privateConfig() string { return filepath.Join(r.Dir, ".git", "berth.toml") }
func (r *Repo) userConfig() string    { return filepath.Join(r.ConfigHome, "berth", "config.toml") }
func repoConfig(worktree string) string {
	return filepath.Join(worktree, "berth.toml")
}

// A key set in .git/berth.toml wins over the same key in berth.toml, which
// wins over the user config, which wins over the built-in default.
func TestConfigPrecedence(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add")

	builtin := configStatusOf(t, repo, wt).Host
	if builtin == "" {
		t.Fatalf("status reports no host with no config")
	}

	steps := []struct {
		layer, path, host string
		env               map[string]string
	}{
		{"user", repo.userConfig(), "{name}.user.localhost", map[string]string{"FROM_USER": "user", "FROM_REPO": "user", "FROM_PRIVATE": "user"}},
		{"repo", repoConfig(wt), "{name}.repo.localhost", map[string]string{"FROM_REPO": "repo", "FROM_PRIVATE": "repo"}},
		{"private", repo.privateConfig(), "{name}.private.localhost", map[string]string{"FROM_PRIVATE": "private"}},
	}
	for _, step := range steps {
		var b strings.Builder
		b.WriteString("[berth]\nhost = \"" + step.host + "\"\n\n[env.set]\n")
		for k, v := range step.env {
			b.WriteString(k + " = \"" + v + "\"\n")
		}
		writeFile(t, step.path, b.String())

		s := configStatusOf(t, repo, wt)
		want := strings.ReplaceAll(step.host, "{name}", s.Name)
		if s.Host != want {
			t.Errorf("after writing the %s config, host = %q, want %q", step.layer, s.Host, want)
		}
		if s.Host == builtin {
			t.Errorf("after writing the %s config, host is still the built-in %q", step.layer, builtin)
		}
	}

	s := configStatusOf(t, repo, wt)
	for key, want := range map[string]string{"FROM_USER": "user", "FROM_REPO": "repo", "FROM_PRIVATE": "private"} {
		if got := s.Env[key]; got != want {
			t.Errorf("env %s = %q, want %q (env: %v)", key, got, want, s.Env)
		}
	}

	if err := os.Remove(repo.privateConfig()); err != nil {
		t.Fatal(err)
	}
	if s := configStatusOf(t, repo, wt); s.Host != s.Name+".repo.localhost" {
		t.Errorf("without the private config, host = %q, want the repo config's", s.Host)
	}
	if err := os.Remove(repoConfig(wt)); err != nil {
		t.Fatal(err)
	}
	if s := configStatusOf(t, repo, wt); s.Host != s.Name+".user.localhost" {
		t.Errorf("without the repo config, host = %q, want the user config's", s.Host)
	}
	if err := os.Remove(repo.userConfig()); err != nil {
		t.Fatal(err)
	}
	if s := configStatusOf(t, repo, wt); s.Host != builtin {
		t.Errorf("with no config, host = %q, want the built-in %q", s.Host, builtin)
	}
}

// .git/berth.toml written in the main worktree is read when berth runs in
// any linked worktree of that repository.
func TestPrivateConfigIsReadFromLinkedWorktrees(t *testing.T) {
	repo := NewRepo(t)
	writeFile(t, repo.privateConfig(), "[berth]\nhost = \"{name}.private.localhost\"\n\n[env.set]\nQUEUE_CONNECTION = \"sync\"\n")
	one := repo.AddWorktree(t, "one")
	two := repo.AddWorktree(t, "two")

	for _, wt := range []string{one, two} {
		mustBerth(t, repo, wt, "add")
		s := configStatusOf(t, repo, filepath.Join(wt, "web"))
		if s.Host != s.Name+".private.localhost" {
			t.Errorf("%s: host = %q, want the private config's", wt, s.Host)
		}
		if s.Env["QUEUE_CONNECTION"] != "sync" {
			t.Errorf("%s: env QUEUE_CONNECTION = %q, want sync", wt, s.Env["QUEUE_CONNECTION"])
		}
	}

	writeFile(t, repo.privateConfig(), "[berth]\nhots = \"typo\"\n")
	if res := repo.Berth(t, two, "status"); res.Exit != 5 {
		t.Errorf("invalid private config: status in a linked worktree exit = %d, want 5\nstderr:\n%s", res.Exit, res.Stderr)
	}
}

// With no config file at all, berth add succeeds on the fixture project.
func TestAddWithNoConfig(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-x")
	for _, p := range []string{repo.privateConfig(), repoConfig(repo.Dir), repoConfig(wt), repo.userConfig()} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s exists; this test needs no config at all", p)
		}
	}

	res := mustBerth(t, repo, wt, "add", "--json")
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &added); err != nil || added.ID == "" {
		t.Fatalf("berth add --json: %v\n%s", err, res.Stdout)
	}
	if s := configStatusOf(t, repo, wt); s.ID != added.ID {
		t.Errorf("status reports berth %q, add made %q", s.ID, added.ID)
	}
}

// An unknown key, a wrong type or a template naming an undefined port makes
// the command exit 5, naming the file and the key.
func TestInvalidConfigExitsFive(t *testing.T) {
	cases := []struct {
		name, content, key string
	}{
		{"unknown key", "[berth]\nhots = \"x.localhost\"\n", "hots"},
		{"unknown table", "[berht]\nhost = \"x.localhost\"\n", "berht"},
		{"wrong type", "[berth]\nhost = 3\n", "host"},
		{"wrong type in a list", "[compose]\nexclude = \"mailpit\"\n", "exclude"},
		{"undefined port", "[env.set]\nVITE_REVERB_PORT = \"{port.web}\"\n", "VITE_REVERB_PORT"},
		{"undefined port with others defined", "[compose.ports]\n\"web:80\" = \"assign\"\n\n[env.set]\nDB_PORT = \"{port.db}\"\n", "DB_PORT"},
	}
	layers := []struct {
		name string
		path func(r *Repo, wt string) string
	}{
		{"private", func(r *Repo, _ string) string { return r.privateConfig() }},
		{"repo", func(_ *Repo, wt string) string { return repoConfig(wt) }},
		{"user", func(r *Repo, _ string) string { return r.userConfig() }},
	}

	repo := NewRepo(t)
	withBerth := repo.AddWorktree(t, "with-berth")
	mustBerth(t, repo, withBerth, "add")
	fresh := repo.AddWorktree(t, "fresh")

	for _, layer := range layers {
		for _, c := range cases {
			t.Run(layer.name+"/"+c.name, func(t *testing.T) {
				for _, wt := range []string{withBerth, fresh} {
					path := layer.path(repo, wt)
					writeFile(t, path, c.content)
					defer os.Remove(path)
				}

				for _, run := range []struct {
					dir  string
					args []string
				}{
					{fresh, []string{"add"}},
					{withBerth, []string{"add"}},
					{withBerth, []string{"status"}},
				} {
					path := layer.path(repo, run.dir)
					res := repo.Berth(t, run.dir, run.args...)
					if res.Exit != 5 {
						t.Errorf("berth %v: exit = %d, want 5\nstdout:\n%s\nstderr:\n%s", run.args, res.Exit, res.Stdout, res.Stderr)
					}
					if !strings.Contains(res.Stderr, path) || !strings.Contains(res.Stderr, c.key) {
						t.Errorf("berth %v: message %q does not name %s and key %s", run.args, res.Stderr, path, c.key)
					}

					res = repo.Berth(t, run.dir, append(run.args, "--json")...)
					var body struct {
						Error struct {
							Code    string `json:"code"`
							Message string `json:"message"`
						} `json:"error"`
					}
					if err := json.Unmarshal([]byte(res.Stdout), &body); err != nil {
						t.Fatalf("berth %v --json: %v\n%s", run.args, err, res.Stdout)
					}
					if res.Exit != 5 || body.Error.Code != "config_invalid" {
						t.Errorf("berth %v --json: exit %d code %q, want 5 config_invalid", run.args, res.Exit, body.Error.Code)
					}
					if !strings.Contains(body.Error.Message, path) || !strings.Contains(body.Error.Message, c.key) {
						t.Errorf("berth %v --json: message %q does not name %s and key %s", run.args, body.Error.Message, path, c.key)
					}
				}
				if got := berthsAt(list(t, repo), fresh); len(got) != 0 {
					t.Errorf("add with invalid config recorded a berth: %+v", got)
				}
			})
		}
	}
}

// berth status --json shows the effective value of each templated key, with
// templates expanded.
func TestStatusShowsExpandedTemplates(t *testing.T) {
	repo := NewRepo(t)
	writeFile(t, repo.privateConfig(), `[berth]
host = "{name}.myapp.localhost"

[env]
url  = ["ORIGIN_URL"]
host = ["SANCTUM_STATEFUL_DOMAINS"]

[env.set]
APP_URL = "{url}"
VITE_REVERB_HOST = "{host}"
ALLOWED_ORIGINS = "{host},app.{host},console.{host}"
WORKTREE_NAME = "{name}"
QUEUE_CONNECTION = "sync"
`)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add")
	s := configStatusOf(t, repo, wt)

	if s.Name == "" || strings.ContainsAny(s.Name, "{}") {
		t.Fatalf("name = %q, want the worktree's expanded name", s.Name)
	}
	if want := s.Name + ".myapp.localhost"; s.Host != want {
		t.Errorf("host = %q, want %q", s.Host, want)
	}
	if !strings.Contains(s.URL, s.Host) || strings.ContainsAny(s.URL, "{}") {
		t.Errorf("url = %q, want an expanded URL on host %q", s.URL, s.Host)
	}
	want := map[string]string{
		"APP_URL":                  s.URL,
		"VITE_REVERB_HOST":         s.Host,
		"ALLOWED_ORIGINS":          s.Host + ",app." + s.Host + ",console." + s.Host,
		"WORKTREE_NAME":            s.Name,
		"QUEUE_CONNECTION":         "sync",
		"ORIGIN_URL":               s.URL,
		"SANCTUM_STATEFUL_DOMAINS": s.Host,
	}
	for key, v := range want {
		if got, ok := s.Env[key]; !ok || got != v {
			t.Errorf("env %s = %q (present %v), want %q", key, got, ok, v)
		}
	}
	for key, v := range s.Env {
		if strings.ContainsAny(v, "{}") {
			t.Errorf("env %s = %q still holds a template", key, v)
		}
	}
}
