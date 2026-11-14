//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

const day = 24 * time.Hour

func idleCandidate(t *testing.T, repo *Repo, name string) (string, stateStatus) {
	t.Helper()
	wt := repo.AddWorktree(t, name)
	mustBerth(t, repo, wt, "add", "--wait")
	return wt, stateOf(t, repo, wt)
}

func setCreatedAt(t *testing.T, repo *Repo, id string, at time.Time) {
	t.Helper()
	path := filepath.Join(repo.StateDir, "berths", id+".json")
	var rec map[string]any
	if err := json.Unmarshal([]byte(readString(t, path)), &rec); err != nil {
		t.Fatalf("registry record %s: %v", path, err)
	}
	rec["created_at"] = at.UTC().Format(time.RFC3339Nano)
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data)+"\n")
}

// backdateReflog rewrites the time on every entry of the HEAD log in adminDir, since
// berth reads a commit or checkout's time from the entry, not from the file.
func backdateReflog(t *testing.T, adminDir string, at time.Time) {
	t.Helper()
	path := filepath.Join(adminDir, "logs", "HEAD")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	stamp := regexp.MustCompile(`(?m)^([^\t\n]*> )\d+( [+-]\d{4}\t)`)
	if err := os.WriteFile(path, stamp.ReplaceAll(data, []byte("${1}"+strconv.FormatInt(at.Unix(), 10)+"${2}")), 0o644); err != nil {
		t.Fatal(err)
	}
}

func backdateTree(t *testing.T, root string, at time.Time) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return lchtimes(path, at)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// ageBerth makes every signal of the berth's last use read at: its creation, the use
// berth commands recorded, and the times of whatever git reports changed in the worktree
// and of git's own index and the entries of its HEAD log. Run it after the last berth
// command on the berth, since each of those records a use.
func ageBerth(t *testing.T, repo *Repo, wt, id string, at time.Time) {
	t.Helper()
	setCreatedAt(t, repo, id, at)
	writeFile(t, filepath.Join(repo.StateDir, "lastuse", id), at.UTC().Format(time.RFC3339Nano)+"\n")
	backdateTree(t, wt, at)
	adminDir := strings.TrimSpace(repo.mustRun(t, wt, "git", "rev-parse", "--absolute-git-dir"))
	backdateReflog(t, adminDir, at)
	backdateTree(t, adminDir, at)
}

func pruned(t *testing.T, repo *Repo, id string) bool {
	t.Helper()
	_, ok := listFull(t, repo)[id]
	return !ok
}

// Prune --idle 7d --dry-run lists every orphan and every berth unused for
// 7 days, with what would be deleted, and deletes nothing; prune --idle 7d deletes
// exactly that list.
func TestPruneIdleDeletesExactlyWhatDryRunLists(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	orphan, orphanS := idleCandidate(t, repo, "feat-orphan")
	idle, idleS := idleCandidate(t, repo, "feat-idle")
	recent, recentS := idleCandidate(t, repo, "feat-recent")
	ageBerth(t, repo, idle, idleS.ID, time.Now().Add(-8*day))
	ageBerth(t, repo, recent, recentS.ID, time.Now().Add(-6*day))
	repo.Git(t, "worktree", "remove", "--force", orphan)

	dry := pruneJSON(t, repo, "--idle", "7d", "--dry-run")
	if !dry.DryRun {
		t.Errorf("prune --idle 7d --dry-run --json reports dry_run false")
	}
	byID := map[string]removeResult{}
	for _, b := range dry.Berths {
		byID[b.ID] = b
	}
	if len(byID) != 2 || byID[orphanS.ID].ID == "" || byID[idleS.ID].ID == "" {
		t.Fatalf("prune --idle 7d --dry-run lists %+v, want exactly the orphan %s and the idle berth %s", dry.Berths, orphanS.ID, idleS.ID)
	}
	for _, s := range []stateStatus{orphanS, idleS} {
		if v := s.ComposeProject + "_cache"; !byID[s.ID].has("volume", v) {
			t.Errorf("prune --idle 7d --dry-run does not list volume %s for %s: %v", v, s.ID, byID[s.ID].items())
		}
	}
	if !byID[idleS.ID].has("file", ".env") {
		t.Errorf("prune --idle 7d --dry-run does not list the idle berth's .env: %v", byID[idleS.ID].items())
	}
	for _, s := range []stateStatus{orphanS, idleS} {
		if !dockerHas("volume", s.ComposeProject+"_cache") {
			t.Errorf("prune --idle 7d --dry-run deleted volume %s_cache", s.ComposeProject)
		}
	}
	if !fileExistsAt(filepath.Join(idle, ".env")) {
		t.Errorf("prune --idle 7d --dry-run deleted the idle berth's .env")
	}
	if n := len(listFull(t, repo)); n != 3 {
		t.Errorf("after prune --idle 7d --dry-run, list has %d berths, want all 3", n)
	}

	real := pruneJSON(t, repo, "--idle", "7d")
	if real.DryRun {
		t.Errorf("prune --idle 7d --json reports dry_run true")
	}
	if len(real.Berths) != len(dry.Berths) {
		t.Fatalf("prune --idle 7d removed %d berths, dry run listed %d", len(real.Berths), len(dry.Berths))
	}
	for _, b := range real.Berths {
		want, ok := byID[b.ID]
		if !ok {
			t.Errorf("prune --idle 7d removed %s, which the dry run didn't list", b.ID)
			continue
		}
		if strings.Join(b.items(), "\n") != strings.Join(want.items(), "\n") {
			t.Errorf("prune --idle 7d removed for %s:\n%s\nwant what the dry run listed:\n%s", b.ID, strings.Join(b.items(), "\n"), strings.Join(want.items(), "\n"))
		}
		for _, it := range b.Removed {
			if (it.Kind == "volume" || it.Kind == "container" || it.Kind == "network") && dockerHas(it.Kind, it.Name) {
				t.Errorf("prune --idle 7d reports removing %s %s, but it still exists", it.Kind, it.Name)
			}
		}
	}
	if fileExistsAt(filepath.Join(idle, ".env")) {
		t.Errorf("the pruned idle berth's .env is still in its worktree")
	}
	after := listFull(t, repo)
	if len(after) != 1 || after[recentS.ID].ID == "" {
		t.Errorf("after prune --idle 7d, list has %v, want only the berth used 6 days ago, %s", after, recentS.ID)
	}
	if !dockerHas("volume", recentS.ComposeProject+"_cache") {
		t.Errorf("prune --idle 7d deleted the volume of the berth used 6 days ago")
	}
	if !dockerHas("volume", repo.Project+"_cache") {
		t.Errorf("prune --idle 7d deleted main's cache volume")
	}
}

// The "Idle pruning keeps the work" guarantee: a pruned idle berth's
// worktree keeps its tracked files, its uncommitted changes and every untracked file
// berth didn't write, byte for byte.
func TestPruneIdleKeepsTheWork(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-work")
	appendFile(t, filepath.Join(wt, "web", "index.html"), "<p>unstaged edit</p>\n")
	appendFile(t, filepath.Join(wt, "README.md"), "staged edit\n")
	repo.mustRun(t, wt, "git", "add", "README.md")
	appendFile(t, filepath.Join(wt, ".gitignore"), "scratch/\n")
	writeFile(t, filepath.Join(wt, "notes", "todo.txt"), "untracked, no final newline")
	if err := os.WriteFile(filepath.Join(wt, "notes", "secret.key"), []byte{0, 1, 2, 255}, 0o600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(wt, "scratch", "ignored.bin"), "ignored but mine\n")
	if err := os.Symlink("README.md", filepath.Join(wt, "readme-link")); err != nil {
		t.Fatal(err)
	}

	contents := func() map[string]fileState {
		s := snapshot(t, wt, nil)
		for k, v := range s {
			v.modTime = 0
			s[k] = v
		}
		return s
	}
	gitView := func() string {
		return repo.mustRun(t, wt, "git", "diff", "--binary") + "\x00" + repo.mustRun(t, wt, "git", "diff", "--cached", "--binary")
	}
	before, beforeGit := contents(), gitView()

	mustBerth(t, repo, wt, "add", "--wait")
	id := stateOf(t, repo, wt).ID
	ageBerth(t, repo, wt, id, time.Now().Add(-30*day))
	pruneJSON(t, repo, "--idle", "7d")
	if !pruned(t, repo, id) {
		t.Fatalf("berth %s unused for 30 days was not pruned by prune --idle 7d", id)
	}

	if diffs := diffSnapshots(before, contents()); len(diffs) > 0 {
		t.Errorf("prune --idle changed the worktree's own files:\n%s", strings.Join(diffs, "\n"))
	}
	if got := gitView(); got != beforeGit {
		t.Errorf("prune --idle changed the worktree's uncommitted changes:\nbefore:\n%s\nafter:\n%s", beforeGit, got)
	}
}

// A berth with a running container is never pruned, however old its last
// use.
func TestPruneIdleKeepsBerthWithRunningContainer(t *testing.T) {
	repo := NewRepo(t)
	running, runningS := idleCandidate(t, repo, "feat-running")
	idle, idleS := idleCandidate(t, repo, "feat-idle")
	repo.Compose(t, running, "up", "-d", "--no-deps", "search")
	ageBerth(t, repo, running, runningS.ID, time.Now().Add(-365*day))
	ageBerth(t, repo, idle, idleS.ID, time.Now().Add(-30*day))

	pruneJSON(t, repo, "--idle", "7d")
	if pruned(t, repo, runningS.ID) {
		t.Errorf("prune --idle 7d pruned berth %s, which has a running container", runningS.ID)
	}
	if got := labelled(t, "container", runningS.ComposeProject); len(got) == 0 {
		t.Errorf("prune --idle 7d removed the running container of berth %s", runningS.ID)
	}
	if !pruned(t, repo, idleS.ID) {
		t.Errorf("prune --idle 7d kept berth %s, unused for 30 days with nothing running", idleS.ID)
	}
}

// A berth whose worktree is locked is never pruned.
func TestPruneIdleKeepsBerthWithLockedWorktree(t *testing.T) {
	repo := NewRepo(t)
	locked, lockedS := idleCandidate(t, repo, "feat-locked")
	idle, idleS := idleCandidate(t, repo, "feat-idle")
	repo.Git(t, "worktree", "lock", "--reason", "keep me", locked)
	ageBerth(t, repo, locked, lockedS.ID, time.Now().Add(-30*day))
	ageBerth(t, repo, idle, idleS.ID, time.Now().Add(-30*day))

	pruneJSON(t, repo, "--idle", "7d")
	if pruned(t, repo, lockedS.ID) {
		t.Errorf("prune --idle 7d pruned berth %s, whose worktree is locked", lockedS.ID)
	}
	if !fileExistsAt(filepath.Join(locked, ".env")) {
		t.Errorf("prune --idle 7d deleted the .env of berth %s, whose worktree is locked", lockedS.ID)
	}
	if !pruned(t, repo, idleS.ID) {
		t.Errorf("prune --idle 7d kept berth %s, unused for 30 days and not locked", idleS.ID)
	}
}

// Pre-remove runs for each idle berth; when it fails, that berth is kept,
// the others are pruned, and prune exits 1.
func TestPruneIdleKeepsBerthWhosePreRemoveFails(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	ran := filepath.Join(t.TempDir(), "pre-remove.ran")
	script := `echo "$BERTH_PATH" >> ` + shellQuote(ran) + `
case "$BERTH_PATH" in *[/\\]feat-fails) exit 7 ;; esac`
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n\n[hooks]\npre-remove = "+tomlString(script)+"\n")
	first, firstS := idleCandidate(t, repo, "feat-first")
	fails, failsS := idleCandidate(t, repo, "feat-fails")
	last, lastS := idleCandidate(t, repo, "feat-last")
	for wt, s := range map[string]stateStatus{first: firstS, fails: failsS, last: lastS} {
		ageBerth(t, repo, wt, s.ID, time.Now().Add(-30*day))
	}

	res := repo.Berth(t, repo.Dir, "prune", "--idle", "7d")
	if res.Exit != 1 {
		t.Errorf("prune --idle 7d with one failing pre-remove exited %d, want 1\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	got := lines(t, ran)
	for _, wt := range []string{first, fails, last} {
		found := false
		for _, l := range got {
			found = found || l == wt
		}
		if !found {
			t.Errorf("pre-remove did not run for idle berth %s; it ran for %v", wt, got)
		}
	}
	if pruned(t, repo, failsS.ID) {
		t.Errorf("prune --idle 7d pruned berth %s although its pre-remove failed", failsS.ID)
	}
	if !fileExistsAt(filepath.Join(fails, ".env")) || !dockerHas("volume", failsS.ComposeProject+"_cache") {
		t.Errorf("prune --idle 7d deleted parts of berth %s although its pre-remove failed", failsS.ID)
	}
	for _, s := range []stateStatus{firstS, lastS} {
		if !pruned(t, repo, s.ID) {
			t.Errorf("prune --idle 7d kept berth %s, whose pre-remove succeeded", s.ID)
		}
	}
}

// berthWithin runs berth, failing the test rather than hanging when it outlasts limit,
// as a berth command waiting on a lock prune holds would.
func berthWithin(t *testing.T, repo *Repo, limit time.Duration, dir string, args ...string) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, berthBin, args...)
	cmd.Dir, cmd.Env = dir, repo.Env()
	ownGroup(cmd)
	// The hook's own berth commands would keep the output pipes open after berth dies.
	cmd.Cancel = func() error { return killGroupOf(cmd) }
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("berth %v did not finish within %s\nstdout:\n%s\nstderr:\n%s", args, limit, stdout.String(), stderr.String())
	}
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.Exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("berth %v: %v", args, err)
	}
	return res
}

// A pre-remove that runs berth status and berth env completes, and its
// berth is pruned.
func TestPruneIdlePreRemoveCanRunBerth(t *testing.T) {
	repo := NewRepo(t)
	out := filepath.Join(t.TempDir(), "pre-remove.out")
	script := `set -e
cd "$BERTH_PATH"
` + shellQuote(berthBin) + ` status >/dev/null
echo status=ok >> ` + shellQuote(out) + `
` + shellQuote(berthBin) + ` env >/dev/null
echo env=ok >> ` + shellQuote(out)
	writeFile(t, repo.privateConfig(), "[hooks]\npre-remove = "+tomlString(script)+"\n")
	wt, s := idleCandidate(t, repo, "feat-hook-berth")
	ageBerth(t, repo, wt, s.ID, time.Now().Add(-30*day))

	res := berthWithin(t, repo, 2*time.Minute, repo.Dir, "prune", "--idle", "7d")
	if res.Exit != 0 {
		t.Errorf("prune --idle 7d with a pre-remove running berth status and env: exit %d\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	if got := readString(t, out); !strings.Contains(got, "status=ok") || !strings.Contains(got, "env=ok") {
		t.Errorf("pre-remove's berth status and berth env did not both succeed, it wrote %q", got)
	}
	if !pruned(t, repo, s.ID) {
		t.Errorf("berth %s was not pruned after its pre-remove ran berth status and env", s.ID)
	}
}

// A berth made by v0.1, with no recorded last use, is pruned only when it
// was created longer ago than the duration.
func TestPruneIdleJudgesV01BerthByCreation(t *testing.T) {
	repo := NewRepo(t)
	old, oldS := idleCandidate(t, repo, "feat-v01-old")
	young, youngS := idleCandidate(t, repo, "feat-v01-young")
	ageBerth(t, repo, old, oldS.ID, time.Now().Add(-30*day))
	ageBerth(t, repo, young, youngS.ID, time.Now().Add(-30*day))
	setCreatedAt(t, repo, youngS.ID, time.Now().Add(-2*day))
	// v0.1 kept no last use, so neither berth has any.
	if err := os.RemoveAll(filepath.Join(repo.StateDir, "lastuse")); err != nil {
		t.Fatal(err)
	}

	pruneJSON(t, repo, "--idle", "7d")
	if !pruned(t, repo, oldS.ID) {
		t.Errorf("prune --idle 7d kept v0.1 berth %s, created 30 days ago and unused since", oldS.ID)
	}
	if pruned(t, repo, youngS.ID) {
		t.Errorf("prune --idle 7d pruned v0.1 berth %s, created 2 days ago", youngS.ID)
	}
}

// With Docker unreachable, no idle berth is pruned: each is reported as
// unknown and prune exits 1.
func TestPruneIdleWithoutDockerPrunesNothing(t *testing.T) {
	repo := NewRepo(t)
	one, oneS := idleCandidate(t, repo, "feat-one")
	two, twoS := idleCandidate(t, repo, "feat-two")
	ageBerth(t, repo, one, oneS.ID, time.Now().Add(-30*day))
	ageBerth(t, repo, two, twoS.ID, time.Now().Add(-30*day))

	res := repo.BerthWith(t, deadDocker(t), repo.Dir, "prune", "--idle", "7d")
	if res.Exit != 1 {
		t.Errorf("prune --idle 7d with Docker unreachable exited %d, want 1\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	for wt, s := range map[string]stateStatus{one: oneS, two: twoS} {
		reported := false
		for _, line := range strings.Split(res.Stdout+res.Stderr, "\n") {
			reported = reported || strings.Contains(line, s.ID) && strings.Contains(strings.ToLower(line), "unknown")
		}
		if !reported {
			t.Errorf("prune --idle 7d with Docker unreachable does not report berth %s as unknown:\nstdout:\n%s\nstderr:\n%s", s.ID, res.Stdout, res.Stderr)
		}
		if pruned(t, repo, s.ID) || !fileExistsAt(filepath.Join(wt, ".env")) {
			t.Errorf("prune --idle 7d with Docker unreachable pruned berth %s", s.ID)
		}
	}
}

// usedByFirstPreRemove makes two idle berths whose pre-remove, the first time it runs,
// runs use in the other berth's worktree. It returns the berth whose pre-remove ran first
// and the one it used, read after prune.
func usedByFirstPreRemove(t *testing.T, repo *Repo, use string) (first, used stateStatus) {
	t.Helper()
	mark := filepath.Join(t.TempDir(), "first")
	a := filepath.Join(filepath.Dir(repo.Dir), "worktrees", "feat-a")
	b := filepath.Join(filepath.Dir(repo.Dir), "worktrees", "feat-b")
	script := `if mkdir ` + shellQuote(mark) + ` 2>/dev/null; then
  if [ "$BERTH_PATH" = ` + shellQuote(a) + ` ]; then other=` + shellQuote(b) + `; else other=` + shellQuote(a) + `; fi
  echo "$other" > ` + shellQuote(filepath.Join(mark, "other")) + `
  cd "$other"
  ` + use + `
fi`
	writeFile(t, repo.privateConfig(), "[hooks]\npre-remove = "+tomlString(script)+"\n")
	states := map[string]stateStatus{}
	for _, name := range []string{"feat-a", "feat-b"} {
		wt, s := idleCandidate(t, repo, name)
		states[wt] = s
	}
	for wt, s := range states {
		ageBerth(t, repo, wt, s.ID, time.Now().Add(-30*day))
	}

	repo.Berth(t, repo.Dir, "prune", "--idle", "7d")
	other := strings.TrimSpace(readString(t, filepath.Join(mark, "other")))
	for wt, s := range states {
		if wt == other {
			used = s
		} else {
			first = s
		}
	}
	return first, used
}

// A berth that gets a new file after the list is made, from an earlier
// berth's pre-remove, is kept.
func TestPruneIdleKeepsBerthUsedAfterTheList(t *testing.T) {
	repo := NewRepo(t)
	first, used := usedByFirstPreRemove(t, repo, `echo work > made-during-prune.txt`)
	if !pruned(t, repo, first.ID) {
		t.Errorf("prune --idle 7d kept berth %s, idle throughout", first.ID)
	}
	if pruned(t, repo, used.ID) {
		t.Errorf("prune --idle 7d pruned berth %s, whose worktree changed after the list was made", used.ID)
	}
}

// A berth that starts a container after the list is made, from an earlier
// berth's pre-remove, is kept.
func TestPruneIdleKeepsBerthStartedAfterTheList(t *testing.T) {
	repo := NewRepo(t)
	// The hook runs with the pruned berth's environment, which must not pick the
	// compose project of the other berth.
	first, used := usedByFirstPreRemove(t, repo, `for v in $(env | sed -n 's/^\(COMPOSE_[A-Z_]*\)=.*/\1/p'); do unset "$v"; done
  docker compose up -d --no-deps search >/dev/null 2>&1`)
	if !pruned(t, repo, first.ID) {
		t.Errorf("prune --idle 7d kept berth %s, idle throughout", first.ID)
	}
	if len(labelled(t, "container", used.ComposeProject)) == 0 {
		t.Fatalf("pre-remove did not start a container for berth %s", used.ID)
	}
	if pruned(t, repo, used.ID) {
		t.Errorf("prune --idle 7d pruned berth %s, which started a container after the list was made", used.ID)
	}
}

// After pruning, berth add in that worktree makes a fresh berth cloned
// from the source.
func TestAddAfterIdlePruneClonesFromSource(t *testing.T) {
	repo := NewRepo(t)
	mainCache := fillCache(t, repo)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	wt, s := idleCandidate(t, repo, "feat-again")
	inVolume(t, s.ComposeProject+"_cache", "echo only in the old berth > /v/old-berth.txt", false)
	ageBerth(t, repo, wt, s.ID, time.Now().Add(-30*day))
	pruneJSON(t, repo, "--idle", "7d")
	if !pruned(t, repo, s.ID) {
		t.Fatalf("berth %s unused for 30 days was not pruned", s.ID)
	}

	mustBerth(t, repo, wt, "add", "--wait")
	again := stateOf(t, repo, wt)
	if got, want := checksum(t, again.ComposeProject+"_cache"), checksum(t, mainCache); got != want {
		t.Errorf("berth added after prune has cache volume\n%s\nwant a fresh clone of main's\n%s", got, want)
	}
	if !fileExistsAt(filepath.Join(wt, ".env")) {
		t.Errorf("berth added after prune has no .env")
	}
}

// An invalid duration exits 1 and deletes nothing.
func TestPruneIdleRejectsInvalidDuration(t *testing.T) {
	repo := NewRepo(t)
	orphan, orphanS := idleCandidate(t, repo, "feat-orphan")
	idle, idleS := idleCandidate(t, repo, "feat-idle")
	ageBerth(t, repo, idle, idleS.ID, time.Now().Add(-30*day))
	repo.Git(t, "worktree", "remove", "--force", orphan)

	for _, args := range [][]string{
		{"--idle", "seven"},
		{"--idle", "7x"},
		{"--idle", "-7d"},
		{"--idle", "d"},
		{"--idle", ""},
		{"--idle"},
	} {
		res := repo.Berth(t, repo.Dir, append([]string{"prune"}, args...)...)
		if res.Exit != 1 {
			t.Errorf("prune %q exited %d, want 1\nstdout:\n%s\nstderr:\n%s", args, res.Exit, res.Stdout, res.Stderr)
		}
	}
	all := listFull(t, repo)
	if all[orphanS.ID].ID == "" || all[idleS.ID].ID == "" {
		t.Errorf("prune with invalid durations deleted berths, list has %v", all)
	}
	if !fileExistsAt(filepath.Join(idle, ".env")) {
		t.Errorf("prune with invalid durations deleted the idle berth's .env")
	}
}

// Prune without --idle behaves as in v0.1: it prunes orphans only and runs
// no pre-remove.
func TestPruneWithoutIdleLeavesIdleBerths(t *testing.T) {
	repo := NewRepo(t)
	ran := filepath.Join(t.TempDir(), "pre-remove.ran")
	writeFile(t, repo.privateConfig(), "[hooks]\npre-remove = "+tomlString(`echo "$BERTH_PATH" >> `+shellQuote(ran))+"\n")
	orphan, orphanS := idleCandidate(t, repo, "feat-orphan")
	idle, idleS := idleCandidate(t, repo, "feat-idle")
	ageBerth(t, repo, idle, idleS.ID, time.Now().Add(-365*day))
	repo.Git(t, "worktree", "remove", "--force", orphan)

	dry := pruneJSON(t, repo, "--dry-run")
	if len(dry.Berths) != 1 || dry.Berths[0].ID != orphanS.ID {
		t.Errorf("prune --dry-run lists %+v, want only the orphan %s", dry.Berths, orphanS.ID)
	}
	real := pruneJSON(t, repo)
	if len(real.Berths) != 1 || real.Berths[0].ID != orphanS.ID {
		t.Errorf("prune removed %+v, want only the orphan %s", real.Berths, orphanS.ID)
	}
	if pruned(t, repo, idleS.ID) {
		t.Errorf("prune without --idle pruned berth %s, which is idle but not orphaned", idleS.ID)
	}
	if got := lines(t, ran); len(got) > 0 {
		t.Errorf("prune without --idle ran pre-remove for %v", got)
	}
}

func TestPruneIdleTakesHoursAndMinutes(t *testing.T) {
	repo := NewRepo(t)
	wt, s := idleCandidate(t, repo, "feat-hours")
	ageBerth(t, repo, wt, s.ID, time.Now().Add(-3*time.Hour))

	if got := pruneJSON(t, repo, "--idle", "4h", "--dry-run"); len(got.Berths) != 0 {
		t.Errorf("prune --idle 4h --dry-run lists %+v for a berth used 3 hours ago, want none", got.Berths)
	}
	if got := pruneJSON(t, repo, "--idle", "170m", "--dry-run"); len(got.Berths) != 1 || got.Berths[0].ID != s.ID {
		t.Errorf("prune --idle 170m --dry-run lists %+v, want berth %s, used 3 hours ago", got.Berths, s.ID)
	}
	if got := pruneJSON(t, repo, "--idle", "2h"); len(got.Berths) != 1 || got.Berths[0].ID != s.ID {
		t.Errorf("prune --idle 2h removed %+v, want berth %s, used 3 hours ago", got.Berths, s.ID)
	}
}

// Design, Idle berths: prune --idle checks every worktree it judges, where list may stop
// checking. With git status slowed past any bound list would keep, a berth edited now is
// still judged by that edit.
func TestPruneIdleChecksEveryWorktreeHoweverSlow(t *testing.T) {
	repo := NewRepo(t)
	edited, editedS := idleCandidate(t, repo, "feat-edited")
	idle, idleS := idleCandidate(t, repo, "feat-idle")
	ageBerth(t, repo, edited, editedS.ID, time.Now().Add(-30*day))
	ageBerth(t, repo, idle, idleS.ID, time.Now().Add(-30*day))
	appendFile(t, filepath.Join(edited, "web", "index.html"), "<p>edited just now</p>\n")

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shim := t.TempDir()
	writeFile(t, filepath.Join(shim, "git"), `#!/bin/sh
for a in "$@"; do [ "$a" = status ] && sleep 3 && break; done
exec `+shellQuote(realGit)+` "$@"
`)
	if err := makeExecutable(filepath.Join(shim, "git")); err != nil {
		t.Fatal(err)
	}

	res := repo.BerthWith(t, []string{"PATH=" + shim + string(os.PathListSeparator) + os.Getenv("PATH")}, repo.Dir, "prune", "--idle", "7d")
	if res.Exit != 0 {
		t.Errorf("prune --idle 7d with a slow git status: exit %d\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	if pruned(t, repo, editedS.ID) {
		t.Errorf("prune --idle 7d pruned berth %s, whose worktree was edited just now, when git status was slow", editedS.ID)
	}
	if !pruned(t, repo, idleS.ID) {
		t.Errorf("prune --idle 7d kept berth %s, unused for 30 days, when git status was slow", idleS.ID)
	}
}
