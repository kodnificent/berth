//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (r *Repo) localConfig() string { return filepath.Join(r.Dir, "berth.local.toml") }

// scriptedEditor writes an editor that insists on --wait before the path, as code --wait
// does, then appends the contents of $BERTH_TEST_EDIT to the file it was given.
func scriptedEditor(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "editor")
	script := "#!/bin/sh\n[ \"$1\" = --wait ] || { echo \"editor: want --wait first, got $1\" >&2; exit 9; }\ncat \"$BERTH_TEST_EDIT\" >> \"$2\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// editorEnv runs berth with only $EDITOR set, appending edit to the file it opens.
func editorEnv(t *testing.T, editor, edit string) []string {
	t.Helper()
	return []string{"VISUAL=", "EDITOR=" + shellQuote(editor) + " --wait", "BERTH_TEST_EDIT=" + writeTemp(t, edit)}
}

type configPathJSON struct {
	Layer  string `json:"layer"`
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
}

func configPath(t *testing.T, repo *Repo, dir string, args ...string) configPathJSON {
	t.Helper()
	res := mustBerth(t, repo, dir, append([]string{"config", "path", "--json"}, args...)...)
	var p configPathJSON
	if err := json.Unmarshal([]byte(res.Stdout), &p); err != nil {
		t.Fatalf("berth config path --json: %v\n%s", err, res.Stdout)
	}
	return p
}

// berth.local.toml in the main worktree is the personal layer for every worktree of the
// repository, above the berth's own berth.toml.
func TestLocalConfigIsReadFromLinkedWorktrees(t *testing.T) {
	repo := NewRepo(t)
	writeFile(t, repo.localConfig(), "[berth]\nhost = \"{name}.local.localhost\"\n\n[env.set]\nFROM_LOCAL = \"local\"\n")
	wt := repo.AddWorktree(t, "feat-local")
	writeFile(t, repoConfig(wt), "[berth]\nhost = \"{name}.repo.localhost\"\n")
	mustBerth(t, repo, wt, "add")

	s := configStatusOf(t, repo, wt)
	if s.Host != s.Name+".local.localhost" {
		t.Errorf("host = %q, want berth.local.toml's over berth.toml's", s.Host)
	}
	if s.Env["FROM_LOCAL"] != "local" {
		t.Errorf("env FROM_LOCAL = %q, want it from main's berth.local.toml (env: %v)", s.Env["FROM_LOCAL"], s.Env)
	}
}

// With both berth.local.toml and .git/berth.toml, the config is invalid: add exits 5
// naming both and creates nothing, and doctor in main reports it.
func TestBothPersonalConfigsExitFive(t *testing.T) {
	repo := NewRepo(t)
	writeFile(t, repo.localConfig(), "[berth]\nhost = \"{name}.local.localhost\"\n")
	writeFile(t, repo.privateConfig(), "[berth]\nhost = \"{name}.private.localhost\"\n")
	wt := repo.AddWorktree(t, "feat-both")

	res := repo.Berth(t, wt, "add")
	if res.Exit != 5 || !strings.Contains(res.Stderr, repo.localConfig()) || !strings.Contains(res.Stderr, repo.privateConfig()) {
		t.Errorf("add with both personal configs: exit %d, stderr %q; want 5 naming both files", res.Exit, res.Stderr)
	}
	if berths := list(t, repo); len(berths) != 0 {
		t.Errorf("add with both personal configs created berths: %+v", berths)
	}
	if res := repo.Berth(t, wt, "config", "path"); res.Exit != 5 {
		t.Errorf("config path with both personal configs: exit %d, want 5", res.Exit)
	}

	d := doctor(t, repo, nil, repo.Dir)
	expectFinding(t, d, "config", repo.localConfig())
	if !strings.Contains(d.Raw, jsonText(repo.privateConfig())) {
		t.Errorf("doctor's config finding doesn't name .git/berth.toml:\n%s", d.Raw)
	}
}

func TestConfigPath(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-path")

	for _, dir := range []string{repo.Dir, wt} {
		if p := configPath(t, repo, dir); p != (configPathJSON{"personal", repo.localConfig(), false}) {
			t.Errorf("config path in %s with no personal config = %+v, want main's berth.local.toml, not existing", dir, p)
		}
	}
	if out := mustBerth(t, repo, wt, "config", "path").Stdout; out != repo.localConfig()+"\n" {
		t.Errorf("config path printed %q, want the path alone", out)
	}

	writeFile(t, repo.privateConfig(), "")
	if p := configPath(t, repo, wt); p != (configPathJSON{"personal", repo.privateConfig(), true}) {
		t.Errorf("config path with .git/berth.toml = %+v, want it, existing", p)
	}
	if err := os.Remove(repo.privateConfig()); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo.localConfig(), "")
	if p := configPath(t, repo, wt); p != (configPathJSON{"personal", repo.localConfig(), true}) {
		t.Errorf("config path with berth.local.toml = %+v, want it, existing", p)
	}

	if p := configPath(t, repo, wt, "--repo"); p != (configPathJSON{"repo", repoConfig(wt), false}) {
		t.Errorf("config path --repo = %+v, want the worktree's berth.toml", p)
	}
	if p := configPath(t, repo, repo.Dir, "--user"); p != (configPathJSON{"user", repo.userConfig(), false}) {
		t.Errorf("config path --user = %+v, want the user config", p)
	}
	if res := repo.Berth(t, wt, "config", "path", "--repo", "--user"); res.Exit != 1 {
		t.Errorf("config path --repo --user: exit %d, want 1", res.Exit)
	}
	if res := repo.Berth(t, wt, "config", "path", "--bogus"); res.Exit != 1 {
		t.Errorf("config path --bogus: exit %d, want 1", res.Exit)
	}
}

// config edit creates berth.local.toml with a header, keeps it out of git status without
// touching .gitignore, runs an editor command that has its own flags, and validates the
// result.
func TestConfigEditCreatesAndValidates(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-edit")
	editor := scriptedEditor(t)
	gitignore, err := os.ReadFile(filepath.Join(repo.Dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}

	none := repo.BerthWith(t, []string{"VISUAL=", "EDITOR="}, wt, "config", "edit")
	if none.Exit != 1 || !strings.Contains(none.Stderr, "$VISUAL") || !strings.Contains(none.Stderr, "$EDITOR") || !strings.Contains(none.Stderr, repo.localConfig()) {
		t.Errorf("config edit with no editor: exit %d, stderr %q; want 1 naming $VISUAL, $EDITOR and the path", none.Exit, none.Stderr)
	}
	if _, err := os.Stat(repo.localConfig()); err == nil {
		t.Errorf("config edit with no editor created %s", repo.localConfig())
	}

	res := repo.BerthWith(t, editorEnv(t, editor, "[berth]\nhost = \"{name}.edited.localhost\"\n"), wt, "config", "edit", "--json")
	if res.Exit != 0 {
		t.Fatalf("config edit: exit %d\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	var out struct {
		Path    string `json:"path"`
		Created bool   `json:"created"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil || out.Path != repo.localConfig() || !out.Created {
		t.Errorf("config edit --json = %q, want the path and created true", res.Stdout)
	}
	data, err := os.ReadFile(repo.localConfig())
	if err != nil {
		t.Fatalf("config edit didn't create berth.local.toml: %v", err)
	}
	if !strings.HasPrefix(string(data), "#") || !strings.HasSuffix(string(data), "host = \"{name}.edited.localhost\"\n") {
		t.Errorf("berth.local.toml = %q, want a commented header followed by the edit", data)
	}
	exclude, err := os.ReadFile(filepath.Join(repo.Dir, ".git", "info", "exclude"))
	if err != nil || !strings.Contains("\n"+string(exclude), "\n/berth.local.toml\n") {
		t.Errorf("info/exclude = %q, %v; want an anchored /berth.local.toml line", exclude, err)
	}
	if after, _ := os.ReadFile(filepath.Join(repo.Dir, ".gitignore")); string(after) != string(gitignore) {
		t.Errorf(".gitignore changed:\n%s", after)
	}
	if st := repo.Git(t, "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Errorf("git status in main after config edit:\n%s", st)
	}

	res = repo.BerthWith(t, editorEnv(t, editor, "\n[env.set]\nFROM_EDIT = \"yes\"\n"), repo.Dir, "config", "edit", "--json")
	if err := json.Unmarshal([]byte(res.Stdout), &out); res.Exit != 0 || err != nil || out.Created {
		t.Errorf("second config edit: exit %d, %q; want 0 and created false", res.Exit, res.Stdout)
	}
	if exclude, _ := os.ReadFile(filepath.Join(repo.Dir, ".git", "info", "exclude")); strings.Count(string(exclude), "/berth.local.toml") != 1 {
		t.Errorf("info/exclude = %q, want one /berth.local.toml entry", exclude)
	}

	env := []string{"VISUAL=" + shellQuote(editor) + " --wait", "EDITOR=false", "BERTH_TEST_EDIT=" + writeTemp(t, "\n[berht]\nhost = \"typo\"\n")}
	bad := repo.BerthWith(t, env, wt, "config", "edit")
	if bad.Exit != 5 || !strings.Contains(bad.Stderr, repo.localConfig()+": unknown key berht") {
		t.Errorf("config edit leaving a typo: exit %d, stderr %q; want 5 with %s: unknown key berht", bad.Exit, bad.Stderr, repo.localConfig())
	}
	failed := repo.BerthWith(t, []string{"VISUAL=", "EDITOR=false"}, wt, "config", "edit")
	if failed.Exit != 1 {
		t.Errorf("config edit with a failing editor: exit %d, want 1", failed.Exit)
	}
}

// With .git/berth.toml in use, config edit opens it and writes no exclude entry.
func TestConfigEditKeepsTheHiddenFile(t *testing.T) {
	repo := NewRepo(t)
	writeFile(t, repo.privateConfig(), "[berth]\nhost = \"{name}.private.localhost\"\n")
	res := repo.BerthWith(t, editorEnv(t, scriptedEditor(t), "\n[env.set]\nA = \"b\"\n"), repo.Dir, "config", "edit")
	if res.Exit != 0 {
		t.Fatalf("config edit: exit %d\n%s", res.Exit, res.Stderr)
	}
	if data, _ := os.ReadFile(repo.privateConfig()); !strings.HasSuffix(string(data), "A = \"b\"\n") || strings.HasPrefix(string(data), "#") {
		t.Errorf(".git/berth.toml = %q, want the edit appended and no header added", data)
	}
	if _, err := os.Stat(repo.localConfig()); err == nil {
		t.Errorf("config edit created berth.local.toml while .git/berth.toml exists")
	}
	if exclude, _ := os.ReadFile(filepath.Join(repo.Dir, ".git", "info", "exclude")); strings.Contains(string(exclude), "berth.local.toml") {
		t.Errorf("info/exclude names berth.local.toml though it was never created:\n%s", exclude)
	}
}

// doctor doesn't ask a berth to clone main's berth.local.toml, and warns about a
// berth.local.toml in the berth itself, which berth never reads.
func TestLocalConfigIsNotAnIgnoredFileWarning(t *testing.T) {
	repo := NewRepo(t)
	if res := repo.BerthWith(t, editorEnv(t, scriptedEditor(t), ""), repo.Dir, "config", "edit"); res.Exit != 0 {
		t.Fatalf("config edit: exit %d\n%s", res.Exit, res.Stderr)
	}
	wt := repo.AddWorktree(t, "feat-doctor")
	mustBerth(t, repo, wt, "add")

	d := doctor(t, repo, nil, wt)
	for _, f := range d.with("ignored_file") {
		if f.Location == "berth.local.toml" {
			t.Errorf("doctor warns that the berth lacks main's berth.local.toml:\n%s", d.Raw)
		}
	}

	writeFile(t, filepath.Join(wt, "berth.local.toml"), "[berth]\nhost = \"{name}.ignored.localhost\"\n")
	d = doctor(t, repo, nil, wt)
	if d.Exit != 0 || len(d.with("personal_config")) != 1 || !strings.Contains(d.with("personal_config")[0].Message, repo.localConfig()) {
		t.Errorf("doctor with berth.local.toml in the berth: exit %d, want 0 and one personal_config warning naming main's path:\n%s", d.Exit, d.Raw)
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "content")
	writeFile(t, path, content)
	return path
}
