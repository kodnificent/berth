//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runLine is the line berth prints for a hooks folder inside the work tree.
const runLine = `if command -v berth >/dev/null 2>&1; then berth hook run "$@" || true; fi`

const hookMarker = "# berth post-checkout hook"

// berthOnPath puts the berth under test first on PATH, as a user's shell would.
func berthOnPath() []string {
	return []string{"PATH=" + filepath.Dir(berthBin) + string(os.PathListSeparator) + os.Getenv("PATH")}
}

func porcelain(t *testing.T, repo *Repo) string {
	t.Helper()
	return repo.Git(t, "status", "--porcelain", "--untracked-files=all")
}

type hookFinding struct {
	Check    string `json:"check"`
	Level    string `json:"level"`
	Location string `json:"location"`
	Message  string `json:"message"`
}

// hookFindings runs berth doctor --json in main and returns its hook findings, which
// must never fail doctor.
func hookFindings(t *testing.T, repo *Repo) []hookFinding {
	t.Helper()
	res := repo.Berth(t, repo.Dir, "doctor", "--json")
	var d struct {
		Findings []hookFinding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &d); err != nil {
		t.Fatalf("berth doctor --json: %v\n%s%s", err, res.Stdout, res.Stderr)
	}
	if res.Exit != 0 {
		t.Fatalf("berth doctor in main exited %d, want 0:\n%s", res.Exit, res.Stdout)
	}
	var out []hookFinding
	for _, f := range d.Findings {
		if f.Check == "hook" {
			if f.Level != "warning" {
				t.Errorf("hook finding is %q, want a warning: %+v", f.Level, f)
			}
			out = append(out, f)
		}
	}
	return out
}

// Where core.hooksPath is unset, points into the git directory, or points outside the
// work tree, berth hook install writes berth's hook in the folder git runs hooks from,
// git worktree add then creates a berth, and uninstall removes it again.
func TestHookInstallWhereGitRunsPrivateHooks(t *testing.T) {
	cases := []struct {
		name string
		// setup sets core.hooksPath, if at all, and returns the folder the hook must land in.
		setup func(t *testing.T, repo *Repo) string
	}{
		{"unset", func(t *testing.T, repo *Repo) string {
			return filepath.Join(repo.Dir, ".git", "hooks")
		}},
		{"git hooks absolute", func(t *testing.T, repo *Repo) string {
			dir := filepath.Join(repo.Dir, ".git", "hooks")
			repo.Git(t, "config", "core.hooksPath", dir)
			return dir
		}},
		{"git hooks relative", func(t *testing.T, repo *Repo) string {
			repo.Git(t, "config", "core.hooksPath", ".git/hooks")
			return filepath.Join(repo.Dir, ".git", "hooks")
		}},
		{"outside the repository", func(t *testing.T, repo *Repo) string {
			dir := filepath.Join(t.TempDir(), "my-hooks")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			dir, _ = filepath.EvalSymlinks(dir)
			repo.Git(t, "config", "core.hooksPath", dir)
			return dir
		}},
		{"symlink outside to git hooks", func(t *testing.T, repo *Repo) string {
			link := filepath.Join(t.TempDir(), "hooks-link")
			dir := filepath.Join(repo.Dir, ".git", "hooks")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(dir, link); err != nil {
				t.Fatal(err)
			}
			repo.Git(t, "config", "core.hooksPath", link)
			return dir
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := NewRepo(t)
			dir := c.setup(t, repo)
			hookPath := filepath.Join(dir, "post-checkout")
			before := porcelain(t, repo)

			if got := hookFindings(t, repo); len(got) != 0 {
				t.Errorf("doctor warns about the hook before it was ever installed: %+v", got)
			}
			res := repo.Berth(t, repo.Dir, "hook", "install", "--json")
			var out struct {
				Hook    string `json:"hook"`
				Changed bool   `json:"changed"`
			}
			if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil || res.Exit != 0 {
				t.Fatalf("berth hook install: exit %d, %v\n%s%s", res.Exit, err, res.Stdout, res.Stderr)
			}
			if real, _ := filepath.EvalSymlinks(out.Hook); out.Hook != hookPath && real != hookPath {
				t.Errorf("hook install reports %s, want %s", out.Hook, hookPath)
			}
			if data, err := os.ReadFile(hookPath); err != nil || !strings.Contains(string(data), hookMarker) {
				t.Fatalf("no berth hook at %s after install: %v", hookPath, err)
			}
			if after := porcelain(t, repo); after != before {
				t.Errorf("hook install changed the work tree:\nbefore:\n%s\nafter:\n%s", before, after)
			}

			wt := hookWorktree(t, repo, nil, "feat-"+strings.ReplaceAll(c.name, " ", "-"))
			if got := berthsAt(list(t, repo), wt); len(got) != 1 {
				t.Errorf("git worktree add with the hook installed made %d berths, want 1", len(got))
			}
			if got := hookFindings(t, repo); len(got) != 0 {
				t.Errorf("doctor warns about a hook that is berth's own: %+v", got)
			}

			mustBerth(t, repo, repo.Dir, "hook", "uninstall")
			if _, err := os.Stat(hookPath); err == nil {
				t.Errorf("hook uninstall left %s", hookPath)
			}
			if again := mustBerth(t, repo, repo.Dir, "hook", "uninstall"); !strings.Contains(again.Stdout, "no post-checkout hook") {
				t.Errorf("a second uninstall: %q, want it to say there is none", again.Stdout)
			}
			if got := hookFindings(t, repo); len(got) != 0 {
				t.Errorf("doctor warns about the hook after berth hook uninstall: %+v", got)
			}
		})
	}
}

// Where core.hooksPath points at a folder inside the work tree, tracked or not, with a
// post-checkout there or not, berth hook install and uninstall refuse, write nothing,
// and name the folder and the line that makes git worktree add create a berth. That
// line works.
func TestHookInstallRefusesHooksPathInsideWorkTree(t *testing.T) {
	cases := []struct {
		name string
		// setup sets core.hooksPath and returns the folder git runs hooks from.
		setup func(t *testing.T, repo *Repo) string
		// existing, when set, is a post-checkout the repository already has there.
		existing string
	}{
		{name: "husky without a hook", setup: func(t *testing.T, repo *Repo) string {
			repo.Git(t, "config", "core.hooksPath", ".husky")
			return filepath.Join(repo.Dir, ".husky")
		}},
		{name: "husky with a committed hook", existing: "#!/bin/sh\necho husky ran\n", setup: func(t *testing.T, repo *Repo) string {
			repo.Git(t, "config", "core.hooksPath", ".husky")
			return filepath.Join(repo.Dir, ".husky")
		}},
		{name: "absolute untracked githooks", setup: func(t *testing.T, repo *Repo) string {
			dir := filepath.Join(repo.Dir, ".githooks")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			repo.Git(t, "config", "core.hooksPath", dir)
			return dir
		}},
		{name: "symlink outside into the work tree", setup: func(t *testing.T, repo *Repo) string {
			dir := filepath.Join(repo.Dir, ".githooks")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(t.TempDir(), "hooks-link")
			if err := os.Symlink(dir, link); err != nil {
				t.Fatal(err)
			}
			repo.Git(t, "config", "core.hooksPath", link)
			return dir
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := NewRepo(t)
			dir := c.setup(t, repo)
			hookPath := filepath.Join(dir, "post-checkout")
			if c.existing != "" {
				writeFile(t, hookPath, c.existing)
				if err := os.Chmod(hookPath, 0o755); err != nil {
					t.Fatal(err)
				}
				repo.Git(t, "add", "-A")
				repo.Git(t, "commit", "-q", "-m", "husky hook")
			}
			before := porcelain(t, repo)

			for _, args := range [][]string{{"hook", "install"}, {"hook", "install", "-n"}, {"hook", "uninstall"}, {"hook", "uninstall"}} {
				res := repo.Berth(t, repo.Dir, args...)
				if res.Exit != 1 {
					t.Errorf("berth %s exited %d, want 1\n%s%s", strings.Join(args, " "), res.Exit, res.Stdout, res.Stderr)
				}
				for _, want := range []string{"core.hooksPath", dir} {
					if !strings.Contains(res.Stderr, want) {
						t.Errorf("berth %s doesn't name %q:\n%s", strings.Join(args, " "), want, res.Stderr)
					}
				}
				if args[1] == "install" && !strings.Contains(res.Stderr, runLine) {
					t.Errorf("berth %s doesn't print the line to add:\n%s", strings.Join(args, " "), res.Stderr)
				}
			}
			js := repo.Berth(t, repo.Dir, "hook", "install", "--json")
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(js.Stdout), &body); err != nil || body.Error.Code != "error" || !strings.Contains(body.Error.Message, runLine) {
				t.Errorf("berth hook install --json: exit %d, %v\n%s", js.Exit, err, js.Stdout)
			}

			if after := porcelain(t, repo); after != before {
				t.Errorf("a refused hook install changed the work tree:\nbefore:\n%s\nafter:\n%s", before, after)
			}
			data, err := os.ReadFile(hookPath)
			switch {
			case c.existing == "" && err == nil:
				t.Errorf("a refused hook install wrote %s", hookPath)
			case c.existing != "" && string(data) != c.existing:
				t.Errorf("a refused hook install changed %s:\n%s", hookPath, data)
			}
			if _, err := os.Stat(filepath.Join(repo.Dir, ".git", "hooks", "post-checkout")); err == nil {
				t.Errorf("berth wrote a hook in .git/hooks, which git doesn't run with core.hooksPath set")
			}
			if got := hookFindings(t, repo); len(got) != 0 {
				t.Errorf("doctor warns about a hook berth was never able to install: %+v", got)
			}

			// The user adds the printed line themself.
			content := c.existing
			if content == "" {
				content = "#!/bin/sh\n"
			}
			writeFile(t, hookPath, content+runLine+"\n")
			if err := os.Chmod(hookPath, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"switch", "-q", "-c", "topic"}, {"checkout", "-q", "main"}} {
				if res, _ := gitWith(t, repo, berthOnPath(), repo.Dir, args...); res.Exit != 0 {
					t.Fatalf("git %v with the printed line: exit %d\n%s", args, res.Exit, res.Stderr)
				}
			}
			old := t.TempDir()
			writeFile(t, filepath.Join(old, "berth"), "#!/bin/sh\necho 'usage: berth hook install|uninstall' >&2\nexit 2\n")
			if err := os.Chmod(filepath.Join(old, "berth"), 0o755); err != nil {
				t.Fatal(err)
			}
			oldPath := []string{"PATH=" + old + string(os.PathListSeparator) + os.Getenv("PATH")}
			if res, _ := gitWith(t, repo, oldPath, repo.Dir, "checkout", "-q", "-b", "topic-old"); res.Exit != 0 {
				t.Errorf("git checkout with an older berth that has no hook run: exit %d, want the line never to fail it\n%s", res.Exit, res.Stderr)
			}
			repo.Git(t, "checkout", "-q", "main")
			wt := hookWorktree(t, repo, berthOnPath(), "feat-line")
			if got := berthsAt(list(t, repo), wt); len(got) != 1 {
				t.Errorf("git worktree add with the printed line made %d berths, want 1", len(got))
			}
			if got := list(t, repo); len(got) != 1 {
				t.Errorf("there are %d berths, want only the one git worktree add made", len(got))
			}
		})
	}
}

// berth doctor warns when the hook berth installed is gone, replaced or edited, or no
// longer runs because core.hooksPath moved, and stays quiet once the hook is berth's
// again.
func TestDoctorWarnsWhenInstalledHookIsNotBerths(t *testing.T) {
	repo := NewRepo(t)
	hookPath := filepath.Join(repo.Dir, ".git", "hooks", "post-checkout")
	mustBerth(t, repo, repo.Dir, "hook", "install")
	if got := hookFindings(t, repo); len(got) != 0 {
		t.Fatalf("doctor warns right after install: %+v", got)
	}
	installed := readString(t, hookPath)

	expect := func(what, where string) {
		t.Helper()
		got := hookFindings(t, repo)
		if len(got) != 1 {
			t.Fatalf("with the hook %s, doctor reports %d hook findings, want 1: %+v", what, len(got), got)
		}
		if !strings.Contains(got[0].Location+" "+got[0].Message, where) {
			t.Errorf("with the hook %s, the warning doesn't name %s: %+v", what, where, got[0])
		}
	}

	if err := os.Remove(hookPath); err != nil {
		t.Fatal(err)
	}
	expect("deleted", hookPath)

	writeFile(t, hookPath, installed+"echo mine\n")
	expect("edited", hookPath)

	writeFile(t, hookPath, "#!/bin/sh\necho mine\n")
	expect("replaced", hookPath)

	writeFile(t, hookPath, installed)
	if got := hookFindings(t, repo); len(got) != 0 {
		t.Errorf("doctor warns once berth's hook is back: %+v", got)
	}

	husky := filepath.Join(repo.Dir, ".husky")
	repo.Git(t, "config", "core.hooksPath", ".husky")
	expect("bypassed by core.hooksPath", husky)
	writeFile(t, filepath.Join(husky, "post-checkout"), "#!/bin/sh\n"+runLine+"\n")
	if got := hookFindings(t, repo); len(got) != 0 {
		t.Errorf("doctor warns once the .husky hook runs berth: %+v", got)
	}
	repo.Git(t, "config", "--unset", "core.hooksPath")

	mustBerth(t, repo, repo.Dir, "hook", "uninstall")
	if got := hookFindings(t, repo); len(got) != 0 {
		t.Errorf("doctor warns after berth hook uninstall: %+v", got)
	}
}
