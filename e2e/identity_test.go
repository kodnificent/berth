//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type statusJSON struct {
	ID       string `json:"id"`
	Worktree string `json:"worktree"`
}

type listedBerth struct {
	ID         string `json:"id"`
	Repository string `json:"repository"`
	Worktree   string `json:"worktree"`
	Name       string `json:"name"`
	Host       string `json:"host"`
	Orphaned   *bool  `json:"orphaned"`
}

func mustBerth(t *testing.T, repo *Repo, dir string, args ...string) Result {
	t.Helper()
	res := repo.Berth(t, dir, args...)
	if res.Exit != 0 {
		t.Fatalf("berth %v (in %s): exit %d\nstdout:\n%s\nstderr:\n%s", args, dir, res.Exit, res.Stdout, res.Stderr)
	}
	return res
}

func status(t *testing.T, repo *Repo, dir string, args ...string) (statusJSON, string) {
	t.Helper()
	res := mustBerth(t, repo, dir, append([]string{"status", "--json"}, args...)...)
	var s statusJSON
	if err := json.Unmarshal([]byte(res.Stdout), &s); err != nil {
		t.Fatalf("berth status --json: %v\n%s", err, res.Stdout)
	}
	if s.ID == "" {
		t.Fatalf("berth status --json has no id:\n%s", res.Stdout)
	}
	return s, res.Stdout
}

func list(t *testing.T, repo *Repo) []listedBerth {
	t.Helper()
	res := mustBerth(t, repo, repo.Dir, "list", "--json")
	var l struct {
		Berths []listedBerth `json:"berths"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &l); err != nil {
		t.Fatalf("berth list --json: %v\n%s", err, res.Stdout)
	}
	for _, b := range l.Berths {
		if b.Orphaned == nil {
			t.Fatalf("berth list --json entry %q has no orphaned field:\n%s", b.ID, res.Stdout)
		}
	}
	return l.Berths
}

func berthsAt(berths []listedBerth, worktree string) []listedBerth {
	var out []listedBerth
	for _, b := range berths {
		if b.Worktree == worktree {
			out = append(out, b)
		}
	}
	return out
}

func findBerth(t *testing.T, berths []listedBerth, id string) listedBerth {
	t.Helper()
	for _, b := range berths {
		if b.ID == id {
			return b
		}
	}
	t.Fatalf("berth %s not in list: %+v", id, berths)
	return listedBerth{}
}

// berth add with no path acts on the worktree containing the current
// directory; with a path, on that worktree.
func TestAddTargetsCurrentOrGivenWorktree(t *testing.T) {
	repo := NewRepo(t)
	fromCwd := repo.AddWorktree(t, "from-cwd")
	byAbsPath := repo.AddWorktree(t, "by-abs-path")
	byRelPath := repo.AddWorktree(t, "by-rel-path")

	mustBerth(t, repo, filepath.Join(fromCwd, "web"), "add")
	mustBerth(t, repo, repo.Dir, "add", byAbsPath)
	mustBerth(t, repo, repo.Dir, "add", filepath.Join("..", "worktrees", "by-rel-path", "web"))

	for _, wt := range []string{fromCwd, byAbsPath, byRelPath} {
		s, _ := status(t, repo, wt)
		if s.Worktree != wt {
			t.Errorf("status in %s reports worktree %q", wt, s.Worktree)
		}
		s2, _ := status(t, repo, repo.Dir, wt)
		if s2.ID != s.ID {
			t.Errorf("status with path %s reports berth %s, status inside it reports %s", wt, s2.ID, s.ID)
		}
	}

	berths := list(t, repo)
	if len(berths) != 3 {
		t.Fatalf("list has %d berths, want 3: %+v", len(berths), berths)
	}
	for _, wt := range []string{fromCwd, byAbsPath, byRelPath} {
		if got := berthsAt(berths, wt); len(got) != 1 {
			t.Errorf("list has %d berths at %s, want 1: %+v", len(got), wt, berths)
		}
	}
}

// berth add on a path that isn't inside a git worktree exits 3.
func TestAddOutsideWorktreeExitsThree(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-x")
	notGit := t.TempDir()

	cases := []struct {
		name, dir string
		args      []string
	}{
		{"cwd outside git", notGit, []string{"add"}},
		{"path outside git", wt, []string{"add", notGit}},
		{"path that does not exist", wt, []string{"add", filepath.Join(notGit, "missing")}},
		{"path inside the git directory", repo.Dir, []string{"add", filepath.Join(repo.Dir, ".git")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := repo.Berth(t, c.dir, c.args...)
			if res.Exit != 3 {
				t.Errorf("exit = %d, want 3\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
			}
			res = repo.Berth(t, c.dir, append(c.args, "--json")...)
			if res.Exit != 3 || !strings.Contains(res.Stdout, `"worktree_not_found"`) {
				t.Errorf("--json: exit = %d, want 3 with worktree_not_found\nstdout:\n%s", res.Exit, res.Stdout)
			}
		})
	}
	if berths := list(t, repo); len(berths) != 0 {
		t.Errorf("failed adds recorded berths: %+v", berths)
	}
}

// berth add on the main worktree exits 1 and says the main worktree is the
// source, not a berth.
func TestAddOnMainWorktreeRefuses(t *testing.T) {
	repo := NewRepo(t)
	repo.AddWorktree(t, "feat-x")

	for _, c := range []struct{ dir, path string }{
		{repo.Dir, ""},
		{filepath.Join(repo.Dir, "web"), ""},
		{filepath.Join(filepath.Dir(repo.Dir), "worktrees", "feat-x"), repo.Dir},
	} {
		args := []string{"add"}
		if c.path != "" {
			args = append(args, c.path)
		}
		res := repo.Berth(t, c.dir, args...)
		if res.Exit != 1 {
			t.Errorf("berth %v in %s: exit = %d, want 1", args, c.dir, res.Exit)
		}
		msg := strings.ToLower(res.Stderr)
		if !strings.Contains(msg, "main worktree") || !strings.Contains(msg, "source") {
			t.Errorf("berth %v in %s: message %q does not say the main worktree is the source", args, c.dir, res.Stderr)
		}
	}
	if berths := list(t, repo); len(berths) != 0 {
		t.Errorf("add on main recorded berths: %+v", berths)
	}
}

// Running berth add twice on the same worktree gives one berth: the
// second run exits 0 and berth status --json is identical after both.
func TestAddTwiceGivesOneBerth(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-x")

	mustBerth(t, repo, wt, "add")
	_, first := status(t, repo, wt)
	mustBerth(t, repo, wt, "add")
	_, second := status(t, repo, wt)

	if first != second {
		t.Errorf("status changed after the second add:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if got := berthsAt(list(t, repo), wt); len(got) != 1 {
		t.Errorf("list has %d berths at %s, want 1: %+v", len(got), wt, got)
	}
}

// Two berth add runs started at the same moment on the same worktree both
// exit 0 and produce one berth. Repeated on several worktrees so a race that only loses
// sometimes still fails the test.
func TestConcurrentAddGivesOneBerth(t *testing.T) {
	repo := NewRepo(t)
	for i := range 8 {
		wt := repo.AddWorktree(t, fmt.Sprintf("race-%d", i))

		cmds := make([]*exec.Cmd, 2)
		outs := make([]*strings.Builder, 2)
		for j := range cmds {
			cmds[j] = exec.Command(berthBin, "add")
			cmds[j].Dir = wt
			cmds[j].Env = repo.Env()
			outs[j] = &strings.Builder{}
			cmds[j].Stdout, cmds[j].Stderr = outs[j], outs[j]
		}
		var start, done sync.WaitGroup
		start.Add(1)
		errs := make([]error, len(cmds))
		for j, cmd := range cmds {
			done.Add(1)
			go func() {
				defer done.Done()
				start.Wait()
				errs[j] = cmd.Run()
			}()
		}
		start.Done()
		done.Wait()

		for j, err := range errs {
			if err != nil {
				t.Errorf("%s: concurrent add %d failed: %v\n%s", wt, j, err, outs[j])
			}
		}
		if got := berthsAt(list(t, repo), wt); len(got) != 1 {
			t.Errorf("list has %d berths at %s after two concurrent adds, want 1: %+v", len(got), wt, got)
		}
	}
}

// After git worktree move, berth status at the new path reports the same
// berth as before the move.
func TestBerthFollowsWorktreeMove(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add")
	before, _ := status(t, repo, wt)

	moved := filepath.Join(filepath.Dir(repo.Dir), "moved", "feat-x-elsewhere")
	if err := os.MkdirAll(filepath.Dir(moved), 0o755); err != nil {
		t.Fatal(err)
	}
	repo.Git(t, "worktree", "move", wt, moved)

	after, _ := status(t, repo, moved)
	if after.ID != before.ID {
		t.Errorf("berth after move = %s, want %s", after.ID, before.ID)
	}
	if after.Worktree != moved {
		t.Errorf("status reports worktree %q, want %q", after.Worktree, moved)
	}
	b := findBerth(t, list(t, repo), before.ID)
	if *b.Orphaned {
		t.Errorf("moved berth is listed as orphaned: %+v", b)
	}
	if b.Worktree != moved {
		t.Errorf("list reports worktree %q for the moved berth, want %q", b.Worktree, moved)
	}
	mustBerth(t, repo, moved, "add")
	if after2, _ := status(t, repo, moved); after2.ID != before.ID {
		t.Errorf("add after move made berth %s, want the existing %s", after2.ID, before.ID)
	}
}

// After git worktree remove and a new git worktree add with the same
// directory name, berth add creates a new berth, and the old one is listed as
// orphaned, not reused.
func TestRemovedWorktreeBerthIsNotReused(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add")
	old, _ := status(t, repo, wt)

	repo.Git(t, "worktree", "remove", wt)
	repo.Git(t, "worktree", "add", "-q", wt, "feat-x")
	mustBerth(t, repo, wt, "add")
	fresh, _ := status(t, repo, wt)

	if fresh.ID == old.ID {
		t.Fatalf("new worktree inherited the removed worktree's berth %s", old.ID)
	}
	berths := list(t, repo)
	if b := findBerth(t, berths, old.ID); !*b.Orphaned {
		t.Errorf("old berth is not listed as orphaned: %+v", b)
	}
	if b := findBerth(t, berths, fresh.ID); *b.Orphaned {
		t.Errorf("new berth is listed as orphaned: %+v", b)
	}
}

// berth list --json lists berths from two different repositories, each
// with its repository, worktree path and whether it is orphaned.
func TestListSpansRepositories(t *testing.T) {
	one := NewRepo(t)
	two := NewRepo(t)
	two.ShareState(one)

	wtOne := one.AddWorktree(t, "feat-one")
	wtTwo := two.AddWorktree(t, "feat-two")
	gone := two.AddWorktree(t, "feat-gone")
	mustBerth(t, one, wtOne, "add")
	mustBerth(t, two, wtTwo, "add")
	mustBerth(t, two, gone, "add")
	goneID, _ := status(t, two, gone)
	two.Git(t, "worktree", "remove", gone)

	for _, from := range []*Repo{one, two} {
		berths := list(t, from)
		if len(berths) != 3 {
			t.Fatalf("list from %s has %d berths, want 3: %+v", from.Dir, len(berths), berths)
		}
		for _, want := range []struct {
			repo, worktree string
			orphaned       bool
		}{
			{one.Dir, wtOne, false},
			{two.Dir, wtTwo, false},
			{two.Dir, gone, true},
		} {
			got := berthsAt(berths, want.worktree)
			if len(got) != 1 {
				t.Errorf("list has %d berths at %s, want 1: %+v", len(got), want.worktree, berths)
				continue
			}
			if got[0].Repository != want.repo {
				t.Errorf("berth at %s has repository %q, want %q", want.worktree, got[0].Repository, want.repo)
			}
			if *got[0].Orphaned != want.orphaned {
				t.Errorf("berth at %s orphaned = %v, want %v", want.worktree, *got[0].Orphaned, want.orphaned)
			}
		}
		if b := findBerth(t, berths, goneID.ID); b.Worktree != gone {
			t.Errorf("orphaned berth reports worktree %q, want %q", b.Worktree, gone)
		}
	}
}

// A worktree locked with git worktree lock whose directory is missing is
// not listed as orphaned.
func TestLockedMissingWorktreeIsNotOrphaned(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add")
	s, _ := status(t, repo, wt)

	repo.Git(t, "worktree", "lock", "--reason", "on an unmounted drive", wt)
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}

	b := findBerth(t, list(t, repo), s.ID)
	if *b.Orphaned {
		t.Errorf("locked worktree with a missing directory is listed as orphaned: %+v", b)
	}
	if b.Worktree != wt {
		t.Errorf("list reports worktree %q, want %q", b.Worktree, wt)
	}
}

// berth list names each berth and its host, in both outputs, so a berth can be told
// apart without running status in its worktree.
func TestListShowsNameAndHost(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-listed")
	mustBerth(t, repo, wt, "add")
	s, _ := status(t, repo, wt)
	b := findBerth(t, list(t, repo), s.ID)
	if b.Name != "feat-listed" || b.Host != "feat-listed.localhost" {
		t.Errorf("list --json gives name %q and host %q, want feat-listed and feat-listed.localhost", b.Name, b.Host)
	}
	human := mustBerth(t, repo, repo.Dir, "list").Stdout
	for _, want := range []string{"NAME", "HOST", "feat-listed", "feat-listed.localhost"} {
		if !strings.Contains(human, want) {
			t.Errorf("berth list lacks %q:\n%s", want, human)
		}
	}
}
