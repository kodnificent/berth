//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

type hintedStatus struct {
	URL  string    `json:"url"`
	Next *[]string `json:"next"`
}

type hintedError struct {
	Error *struct {
		Code    string    `json:"code"`
		Message string    `json:"message"`
		Next    *[]string `json:"next"`
	} `json:"error"`
}

func hintsOf(t *testing.T, repo *Repo, env []string, dir string) hintedStatus {
	t.Helper()
	res := repo.BerthWith(t, env, dir, "status", "--json")
	if res.Exit != 0 {
		t.Fatalf("berth status --json: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	var s hintedStatus
	if err := json.Unmarshal([]byte(res.Stdout), &s); err != nil {
		t.Fatalf("berth status --json: %v\n%s", err, res.Stdout)
	}
	if s.Next == nil {
		t.Fatalf("berth status --json carries no next list:\n%s", res.Stdout)
	}
	return s
}

func refusedStatus(t *testing.T, repo *Repo, dir string) (hintedError, Result) {
	t.Helper()
	res := repo.Berth(t, dir, "status", "--json")
	var e hintedError
	if err := json.Unmarshal([]byte(res.Stdout), &e); err != nil || e.Error == nil {
		t.Fatalf("berth status --json: exit %d, want an error envelope: %v\n%s", res.Exit, err, res.Stdout)
	}
	return e, res
}

func hintContaining(hints []string, want string) bool {
	for _, h := range hints {
		if strings.Contains(h, want) {
			return true
		}
	}
	return false
}

func cloningBerth(t *testing.T, name string) (*Repo, string) {
	t.Helper()
	repo := NewRepo(t)
	fillCache(t, repo)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	wt := repo.AddWorktree(t, name)
	if res := berthWithDelay(t, repo, wt, "20s", "add"); res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s", res.Exit, res.Stderr)
	}
	if e := stateOf(t, repo, wt).volume(t, "cache"); e.Status != "cloning" {
		t.Fatalf("cache is %q right after add, want cloning", e.Status)
	}
	return repo, wt
}

func TestStatusHintsWaitWhileCloning(t *testing.T) {
	repo, wt := cloningBerth(t, "feat-hint-clone")
	s := hintsOf(t, repo, nil, wt)
	if !hintContaining(*s.Next, "berth status --wait") {
		t.Errorf("while cloning, next = %q, want a hint to run berth status --wait", *s.Next)
	}
}

func TestStatusHintsWaitWhilePostAddRuns(t *testing.T) {
	repo := NewRepo(t)
	writeFile(t, repo.privateConfig(), "[hooks]\npost-add = \"sleep 20\"\n")
	wt := repo.AddWorktree(t, "feat-hint-post-add")
	mustBerth(t, repo, wt, "add")
	if pa := hookStatusOf(t, repo, wt).PostAdd; pa == nil || pa.Status == "ok" || pa.Status == "failed" {
		t.Fatalf("post-add is %+v right after add, want it still running", pa)
	}
	s := hintsOf(t, repo, nil, wt)
	if !hintContaining(*s.Next, "berth status --wait") {
		t.Errorf("while post-add runs, next = %q, want a hint to run berth status --wait", *s.Next)
	}
}

func TestStatusHintsReadyBerthURL(t *testing.T) {
	repo := NewRepo(t)
	writeFile(t, repo.privateConfig(), "[berth]\nhost = \"{name}.hinted.localhost\"\n")
	wt := repo.AddWorktree(t, "feat-hint-ready")
	mustBerth(t, repo, wt, "add")
	s := hintsOf(t, repo, nil, wt)
	if s.URL == "" || !strings.Contains(s.URL, "hinted.localhost") {
		t.Fatalf("url = %q, want the berth's own URL", s.URL)
	}
	if !hintContaining(*s.Next, s.URL) {
		t.Errorf("for a ready berth, next = %q, want a hint naming its URL %s", *s.Next, s.URL)
	}
}

func TestStatusWithNoBerthHintsAdd(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-no-berth")
	e, res := refusedStatus(t, repo, wt)
	if res.Exit != 1 || e.Error.Code != "no_berth" {
		t.Fatalf("status with no berth: exit %d, code %q, want 1 and no_berth", res.Exit, e.Error.Code)
	}
	if e.Error.Next == nil || !hintContaining(*e.Error.Next, "berth add") {
		t.Errorf("no_berth error carries next %v, want a hint to run berth add\n%s", e.Error.Next, res.Stdout)
	}
	if berths := list(t, repo); len(berths) != 0 {
		t.Errorf("status with no berth made one: %+v", berths)
	}
}

func TestStatusInMainHintsLinkedWorktrees(t *testing.T) {
	repo := NewRepo(t)
	e, res := refusedStatus(t, repo, repo.Dir)
	if res.Exit != 1 || e.Error.Code != "main_worktree" {
		t.Fatalf("status in main: exit %d, code %q, want 1 and main_worktree", res.Exit, e.Error.Code)
	}
	if e.Error.Next == nil || len(*e.Error.Next) == 0 {
		t.Fatalf("main_worktree error carries no next hint:\n%s", res.Stdout)
	}
	hints := strings.ToLower(strings.Join(*e.Error.Next, "\n"))
	if !strings.Contains(hints, "source") || !strings.Contains(hints, "linked worktree") {
		t.Errorf("main_worktree next = %q, want it to say main is the source and berths live in linked worktrees", *e.Error.Next)
	}
}

// Each case where status carries hints prints the same hints without --json.
func TestHumanStatusPrintsEveryHint(t *testing.T) {
	t.Run("cloning", func(t *testing.T) {
		repo, wt := cloningBerth(t, "feat-human-clone")
		s := hintsOf(t, repo, nil, wt)
		human := mustBerth(t, repo, wt, "status").Stdout
		assertPrintsHints(t, *s.Next, human)
	})
	t.Run("ready", func(t *testing.T) {
		repo := NewRepo(t)
		wt := repo.AddWorktree(t, "feat-human-ready")
		mustBerth(t, repo, wt, "add")
		s := hintsOf(t, repo, nil, wt)
		human := mustBerth(t, repo, wt, "status").Stdout
		assertPrintsHints(t, *s.Next, human)
	})
	for _, c := range []struct {
		name string
		dir  func(*testing.T, *Repo) string
	}{
		{"no berth", func(t *testing.T, r *Repo) string { return r.AddWorktree(t, "feat-human-none") }},
		{"main", func(t *testing.T, r *Repo) string { return r.Dir }},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo := NewRepo(t)
			dir := c.dir(t, repo)
			e, _ := refusedStatus(t, repo, dir)
			if e.Error.Next == nil {
				t.Fatalf("%s error carries no next list", e.Error.Code)
			}
			human := repo.Berth(t, dir, "status")
			assertPrintsHints(t, *e.Error.Next, human.Stderr)
		})
	}
}

func assertPrintsHints(t *testing.T, hints []string, human string) {
	t.Helper()
	if len(hints) == 0 {
		t.Fatalf("next is empty, want at least one hint")
	}
	for _, h := range hints {
		if !strings.Contains(human, h) {
			t.Errorf("human output lacks the hint %q:\n%s", h, human)
		}
	}
}

func TestHintedErrorsKeepCodesAndExits(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-codes")
	for _, c := range []struct{ dir, code string }{
		{wt, "no_berth"},
		{repo.Dir, "main_worktree"},
		{filepath.Join(repo.Dir, "web"), "main_worktree"},
	} {
		e, res := refusedStatus(t, repo, c.dir)
		if res.Exit != 1 || e.Error.Code != c.code {
			t.Errorf("status --json in %s: exit %d, code %q, want 1 and %s", c.dir, res.Exit, e.Error.Code, c.code)
		}
		if res.Stderr != "" {
			t.Errorf("status --json in %s wrote to stderr: %q", c.dir, res.Stderr)
		}
		human := repo.Berth(t, c.dir, "status")
		if human.Exit != 1 || human.Stdout != "" || !strings.HasPrefix(human.Stderr, "berth: ") {
			t.Errorf("status in %s: exit %d, stdout %q, stderr %q; want exit 1 and the error on stderr", c.dir, human.Exit, human.Stdout, human.Stderr)
		}
	}
}

// Status run by post-add sees BERTH_PATH set and post-add still running,
// so it is the one place a --wait hint would never return.
func TestStatusInsideHookHintsNoWait(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	out := t.TempDir()
	script := fmt.Sprintf("%[2]s status --json > %[1]s/status.json; %[2]s status > %[1]s/status.txt",
		shellQuote(out), shellQuote(berthBin))
	writeFile(t, repo.privateConfig(), fmt.Sprintf("[compose.volumes]\ncache = \"clone\"\n[hooks]\npost-add = %s\n", tomlString(script)))
	wt := repo.AddWorktree(t, "feat-hint-hook")
	if res := berthWithDelay(t, repo, wt, "2s", "add"); res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s", res.Exit, res.Stderr)
	}
	if pa := waitPostAdd(t, repo, wt); pa.Status != "ok" {
		t.Fatalf("post-add ended %q, want ok: %+v", pa.Status, pa)
	}

	var s hintedStatus
	raw := readString(t, filepath.Join(out, "status.json"))
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("status from inside post-add: %v\n%s", err, raw)
	}
	if s.Next == nil {
		t.Fatalf("status from inside post-add carries no next list:\n%s", raw)
	}
	if hintContaining(*s.Next, "--wait") {
		t.Errorf("inside post-add, next = %q, want no hint suggesting --wait", *s.Next)
	}
	if human := readString(t, filepath.Join(out, "status.txt")); strings.Contains(human, "--wait") {
		t.Errorf("inside post-add, human status suggests --wait:\n%s", human)
	}
}
