//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// tick outlasts one second, so a last use recorded to the second still moves past
// anything recorded before it.
const tick = 1100 * time.Millisecond

type berthUse struct {
	ID        string
	CreatedAt time.Time
	LastUsed  time.Time
	InUse     string
}

func usesWith(t *testing.T, repo *Repo, env []string) (map[string]berthUse, Result) {
	t.Helper()
	res := repo.BerthWith(t, env, repo.Dir, "list", "--json")
	if res.Exit != 0 {
		t.Fatalf("berth list --json: exit %d\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	var l struct {
		Berths []struct {
			ID        string          `json:"id"`
			CreatedAt time.Time       `json:"created_at"`
			LastUsed  json.RawMessage `json:"last_used"`
			InUse     json.RawMessage `json:"in_use"`
		} `json:"berths"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &l); err != nil {
		t.Fatalf("berth list --json: %v\n%s", err, res.Stdout)
	}
	out := map[string]berthUse{}
	for _, b := range l.Berths {
		if len(b.InUse) == 0 {
			t.Fatalf("berth list --json entry %s has no in_use field:\n%s", b.ID, res.Stdout)
		}
		var last time.Time
		if err := json.Unmarshal(b.LastUsed, &last); err != nil || string(b.LastUsed) == "null" {
			t.Fatalf("berth list --json entry %s has last_used %s, want a time:\n%s", b.ID, b.LastUsed, res.Stdout)
		}
		out[b.ID] = berthUse{b.ID, b.CreatedAt, last, string(b.InUse)}
	}
	return out, res
}

func useOf(t *testing.T, repo *Repo, id string) berthUse {
	t.Helper()
	uses, res := usesWith(t, repo, nil)
	u, ok := uses[id]
	if !ok {
		t.Fatalf("berth %s not in berth list --json:\n%s", id, res.Stdout)
	}
	return u
}

func addBerth(t *testing.T, repo *Repo, name string) (string, string) {
	t.Helper()
	wt := repo.AddWorktree(t, name)
	mustBerth(t, repo, wt, "add")
	s, _ := status(t, repo, wt)
	return wt, s.ID
}

// expectMovedTo fails unless last use moved past before and is no earlier than start,
// to the second.
func expectMovedTo(t *testing.T, what string, before, after berthUse, start time.Time) {
	t.Helper()
	if !after.LastUsed.After(before.LastUsed) || after.LastUsed.Before(start.Truncate(time.Second)) {
		t.Errorf("after %s at %s, last_used is %s (was %s), want it moved to that time or later", what, start.Format(time.RFC3339Nano), after.LastUsed.Format(time.RFC3339Nano), before.LastUsed.Format(time.RFC3339Nano))
	}
}

// A berth whose compose project has a running container is in use, and
// was last used when list ran.
func TestRunningContainerMarksBerthInUseNow(t *testing.T) {
	repo := NewRepo(t)
	running, runningID := addBerth(t, repo, "feat-running")
	_, idleID := addBerth(t, repo, "feat-idle")
	repo.Compose(t, running, "up", "-d", "--no-deps", "search")
	time.Sleep(tick)

	start := time.Now()
	uses, res := usesWith(t, repo, nil)
	end := time.Now()
	r := uses[runningID]
	if r.InUse != "true" {
		t.Errorf("berth with a running container has in_use %s, want true:\n%s", r.InUse, res.Stdout)
	}
	if r.LastUsed.Before(start.Truncate(time.Second)) || r.LastUsed.After(end) {
		t.Errorf("berth with a running container has last_used %s, want the time of the list, between %s and %s", r.LastUsed.Format(time.RFC3339Nano), start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))
	}
	if i := uses[idleID]; i.InUse != "false" {
		t.Errorf("berth whose own project runs nothing has in_use %s while another berth's container runs, want false:\n%s", i.InUse, res.Stdout)
	}
}

// Creating a file anywhere in the worktree moves last use to that time.
func TestCreatingAFileInTheWorktreeMovesLastUsed(t *testing.T) {
	repo := NewRepo(t)
	wt, id := addBerth(t, repo, "feat-create")
	before := useOf(t, repo, id)
	time.Sleep(tick)

	start := time.Now()
	writeFile(t, filepath.Join(wt, "web", "notes", "new.txt"), "new\n")
	expectMovedTo(t, "creating web/notes/new.txt", before, useOf(t, repo, id), start)
}

// Editing an existing file, which leaves its directory's own time alone,
// moves last use too.
func TestEditingAFileInTheWorktreeMovesLastUsed(t *testing.T) {
	repo := NewRepo(t)
	wt, id := addBerth(t, repo, "feat-edit")
	before := useOf(t, repo, id)
	time.Sleep(tick)

	start := time.Now()
	appendFile(t, filepath.Join(wt, "web", "index.html"), "<p>edited</p>\n")
	expectMovedTo(t, "editing web/index.html", before, useOf(t, repo, id), start)
}

// Each berth command run on the berth moves its last use forward.
func TestBerthCommandsMoveLastUsed(t *testing.T) {
	repo := NewRepo(t)
	wt, id := addBerth(t, repo, "feat-commands")
	for _, args := range [][]string{
		{"status"},
		{"env"},
		{"add"},
		{"exec", "--", "true"},
		{"reset"},
		{"doctor"},
	} {
		t.Run(args[0], func(t *testing.T) {
			before := useOf(t, repo, id)
			time.Sleep(tick)
			start := time.Now()
			res := repo.Berth(t, wt, args...)
			if args[0] != "doctor" && res.Exit != 0 {
				t.Fatalf("berth %v: exit %d\nstdout:\n%s\nstderr:\n%s", args, res.Exit, res.Stdout, res.Stderr)
			}
			expectMovedTo(t, "berth "+strings.Join(args, " "), before, useOf(t, repo, id), start)
		})
	}
}

// berth commands a berth hook runs, with BERTH_PATH set, are berth's own
// work and not a use of the berth.
func TestBerthCommandsInsideHooksLeaveLastUsed(t *testing.T) {
	repo := NewRepo(t)
	wt, id := addBerth(t, repo, "feat-in-hook")
	before := useOf(t, repo, id)
	time.Sleep(tick)

	inHook := []string{"BERTH_PATH=" + wt}
	for _, args := range [][]string{{"status"}, {"env"}, {"exec", "--", "true"}, {"doctor"}} {
		res := repo.BerthWith(t, inHook, wt, args...)
		if args[0] != "doctor" && res.Exit != 0 {
			t.Fatalf("berth %v with BERTH_PATH set: exit %d\nstdout:\n%s\nstderr:\n%s", args, res.Exit, res.Stdout, res.Stderr)
		}
	}
	if after := useOf(t, repo, id); !after.LastUsed.Equal(before.LastUsed) {
		t.Errorf("berth commands with BERTH_PATH set moved last_used from %s to %s, want it unchanged", before.LastUsed.Format(time.RFC3339Nano), after.LastUsed.Format(time.RFC3339Nano))
	}
}

// List itself is not a use, so a berth left alone after add stays at its
// creation time. The wait is over a minute so a list that counted as use would show.
func TestUntouchedBerthStaysAtCreation(t *testing.T) {
	repo := NewRepo(t)
	_, id := addBerth(t, repo, "feat-untouched")
	useOf(t, repo, id)
	time.Sleep(65 * time.Second)

	u := useOf(t, repo, id)
	if d := u.LastUsed.Sub(u.CreatedAt); d < -time.Second || d > time.Minute {
		t.Errorf("untouched berth has last_used %s, created_at %s, want within a minute of creation", u.LastUsed.Format(time.RFC3339Nano), u.CreatedAt.Format(time.RFC3339Nano))
	}
	if u.InUse != "false" {
		t.Errorf("berth with no containers has in_use %s, want false", u.InUse)
	}
}

// A record written the way v0.1 wrote it, with no last use anywhere and
// no worktree left to change, shows its creation time.
func TestV01RecordShowsCreationAsLastUsed(t *testing.T) {
	repo := NewRepo(t)
	created := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	id := randomHex(t, 8)
	gone := filepath.Join(filepath.Dir(repo.Dir), "worktrees", "v01-gone")
	rec, err := json.MarshalIndent(map[string]any{
		"id":         id,
		"repository": repo.Dir,
		"common_dir": filepath.Join(repo.Dir, ".git"),
		"admin_dir":  filepath.Join(repo.Dir, ".git", "worktrees", "v01-gone"),
		"worktree":   gone,
		"created_at": created,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo.StateDir, "berths", id+".json"), string(rec)+"\n")

	u := useOf(t, repo, id)
	if u.LastUsed.IsZero() || !u.LastUsed.Equal(created) {
		t.Errorf("v0.1 berth created %s has last_used %s, want its creation time", created.Format(time.RFC3339), u.LastUsed.Format(time.RFC3339Nano))
	}
}

// A live berth whose last use was never recorded, as one v0.1 made, is
// never shown as last used at the zero time or before it existed.
func TestBerthWithoutRecordedUseIsNeverZero(t *testing.T) {
	repo := NewRepo(t)
	_, id := addBerth(t, repo, "feat-v01")
	entries, err := os.ReadDir(repo.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	// v0.1 kept nothing in the state directory but the registry and its lock, so
	// removing the rest leaves the berth as v0.1 would have.
	for _, e := range entries {
		if e.Name() != "berths" && e.Name() != "lock" {
			if err := os.RemoveAll(filepath.Join(repo.StateDir, e.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}

	u := useOf(t, repo, id)
	if u.LastUsed.IsZero() || u.LastUsed.Before(u.CreatedAt.Truncate(time.Second)) || u.LastUsed.After(time.Now()) {
		t.Errorf("berth with no recorded use has last_used %s, created_at %s, want its creation time or later, never zero", u.LastUsed.Format(time.RFC3339Nano), u.CreatedAt.Format(time.RFC3339Nano))
	}
}

func registryBytes(t *testing.T, repo *Repo) map[string]string {
	t.Helper()
	dir := filepath.Join(repo.StateDir, "berths")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		out[e.Name()] = readString(t, filepath.Join(dir, e.Name()))
	}
	return out
}

// Last use is recorded somewhere other than the registry, so using a
// berth leaves every registry record as it was.
func TestRecordingLastUseLeavesRegistryUnchanged(t *testing.T) {
	repo := NewRepo(t)
	wt, id := addBerth(t, repo, "feat-registry")
	before := useOf(t, repo, id)
	records := registryBytes(t, repo)
	time.Sleep(tick)

	start := time.Now()
	mustBerth(t, repo, wt, "status")
	mustBerth(t, repo, wt, "env")
	mustBerth(t, repo, wt, "exec", "--", "true")
	appendFile(t, filepath.Join(wt, "web", "index.html"), "<p>edited</p>\n")
	repo.Compose(t, wt, "up", "-d", "--no-deps", "search")
	after := useOf(t, repo, id)
	expectMovedTo(t, "status, env, exec, an edit and a running container", before, after, start)

	got := registryBytes(t, repo)
	if len(got) != len(records) {
		t.Fatalf("registry holds %d records after the berth was used, %d before", len(got), len(records))
	}
	for name, data := range records {
		if got[name] != data {
			t.Errorf("registry record %s changed when last use was recorded:\nbefore:\n%s\nafter:\n%s", name, data, got[name])
		}
	}
}

func stateFiles(t *testing.T, repo *Repo) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	err := filepath.WalkDir(repo.StateDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(repo.StateDir, path)
			out[rel] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func expectNoNewState(t *testing.T, repo *Repo, before map[string]bool, after string) {
	t.Helper()
	for f := range stateFiles(t, repo) {
		if !before[f] && f != "lock" {
			t.Errorf("after %s, berth's state directory still holds %s, which the berth's use left there", after, f)
		}
	}
}

// Removing a berth leaves nothing of its last use behind.
func TestRemoveLeavesNoLastUse(t *testing.T) {
	repo := NewRepo(t)
	kept, keptID := addBerth(t, repo, "feat-kept")
	mustBerth(t, repo, kept, "status")
	useOf(t, repo, keptID)
	before := stateFiles(t, repo)

	wt, id := addBerth(t, repo, "feat-removed")
	mustBerth(t, repo, wt, "status")
	appendFile(t, filepath.Join(wt, "web", "index.html"), "<p>edited</p>\n")
	useOf(t, repo, id)

	removeJSON(t, repo, wt)
	expectNoNewState(t, repo, before, "berth remove")
}

// Pruning an orphaned berth leaves nothing of its last use behind.
func TestPruneLeavesNoLastUse(t *testing.T) {
	repo := NewRepo(t)
	kept, keptID := addBerth(t, repo, "feat-kept")
	mustBerth(t, repo, kept, "status")
	useOf(t, repo, keptID)
	before := stateFiles(t, repo)

	wt, id := addBerth(t, repo, "feat-pruned")
	mustBerth(t, repo, wt, "status")
	useOf(t, repo, id)

	repo.Git(t, "worktree", "remove", "--force", wt)
	pruneJSON(t, repo)
	expectNoNewState(t, repo, before, "berth prune")
}

func deadDocker(t *testing.T) []string {
	return []string{"DOCKER_HOST=unix://" + filepath.Join(t.TempDir(), "no-docker.sock")}
}

// With Docker unreachable, list still answers and says in use is unknown
// for every berth.
func TestListWithoutDockerReportsInUseUnknown(t *testing.T) {
	repo := NewRepo(t)
	first, _ := addBerth(t, repo, "feat-one")
	addBerth(t, repo, "feat-two")
	repo.Compose(t, first, "up", "-d", "--no-deps", "search")

	uses, res := usesWith(t, repo, deadDocker(t))
	if len(uses) != 2 {
		t.Fatalf("berth list --json without Docker lists %d berths, want 2:\n%s", len(uses), res.Stdout)
	}
	for id, u := range uses {
		if u.InUse != "null" {
			t.Errorf("berth %s has in_use %s with Docker unreachable, want null", id, u.InUse)
		}
	}
}

// List says when it could not reach Docker.
func TestListWithoutDockerWarns(t *testing.T) {
	repo := NewRepo(t)
	addBerth(t, repo, "feat-one")

	for _, args := range [][]string{{"list"}, {"list", "--json"}} {
		res := repo.BerthWith(t, deadDocker(t), repo.Dir, args...)
		if res.Exit != 0 {
			t.Fatalf("berth %v without Docker: exit %d, want 0\nstdout:\n%s\nstderr:\n%s", args, res.Exit, res.Stdout, res.Stderr)
		}
		// The test's temp dir, and so every path list prints, is named after the test.
		said := strings.ReplaceAll(res.Stdout+res.Stderr, filepath.Dir(repo.Dir), "")
		if !strings.Contains(strings.ToLower(said), "docker") {
			t.Errorf("berth %v without Docker gives no warning naming Docker:\nstdout:\n%s\nstderr:\n%s", args, res.Stdout, res.Stderr)
		}
	}
}

// List stays fast when every berth's worktree is large.
func TestListWithLargeWorktreesIsFast(t *testing.T) {
	if os.Getenv("BERTH_E2E_LARGE") == "" {
		t.Skip("set BERTH_E2E_LARGE=1 to make 10 worktrees of 100,000 files each")
	}
	repo := NewRepo(t)
	appendFile(t, filepath.Join(repo.Dir, ".gitignore"), "deps/\n")
	repo.Git(t, "commit", "-q", "-am", "ignore deps")
	const berths, dirs, perDir = 10, 1000, 100
	var worktrees []string
	for i := 0; i < berths; i++ {
		wt, _ := addBerth(t, repo, fmt.Sprintf("feat-large-%02d", i))
		worktrees = append(worktrees, wt)
	}

	made := time.Now()
	dirsToFill := make(chan string)
	errs := make(chan error, berths*dirs)
	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for dir := range dirsToFill {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					errs <- err
					continue
				}
				for f := 0; f < perDir; f++ {
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d.js", f)), nil, 0o644); err != nil {
						errs <- err
						break
					}
				}
			}
		}()
	}
	for _, wt := range worktrees {
		for d := 0; d < dirs; d++ {
			dirsToFill <- filepath.Join(wt, "deps", fmt.Sprintf("pkg%04d", d))
		}
	}
	close(dirsToFill)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	t.Logf("made %d files in %s", berths*dirs*perDir, time.Since(made))

	start := time.Now()
	uses, res := usesWith(t, repo, nil)
	took := time.Since(start)
	t.Logf("berth list --json with %d berths of %d files each took %s", berths, dirs*perDir, took)
	if len(uses) != berths {
		t.Fatalf("berth list --json lists %d berths, want %d:\n%s", len(uses), berths, res.Stdout)
	}
	if took > 2*time.Second {
		t.Errorf("berth list --json took %s with %d berths of %d files each, want within 2s", took, berths, dirs*perDir)
	}
}

func listRow(t *testing.T, out, id string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, id+" ") {
			return line
		}
	}
	t.Fatalf("berth list has no row for %s:\n%s", id, out)
	return ""
}

// Human list marks a berth with a running container as in use, and no
// other.
func TestHumanListMarksInUse(t *testing.T) {
	repo := NewRepo(t)
	running, runningID := addBerth(t, repo, "feat-running")
	_, idleID := addBerth(t, repo, "feat-idle")
	repo.Compose(t, running, "up", "-d", "--no-deps", "search")

	out := mustBerth(t, repo, repo.Dir, "list").Stdout
	if row := listRow(t, out, runningID); !strings.Contains(strings.ToLower(row), "in use") {
		t.Errorf("berth list doesn't mark the berth with a running container as in use:\n%s", out)
	}
	if row := listRow(t, out, idleID); strings.Contains(strings.ToLower(row), "in use") {
		t.Errorf("berth list marks a berth with no running container as in use:\n%s", out)
	}
}

// Human list has a column saying when each berth was last used.
func TestHumanListShowsLastUsed(t *testing.T) {
	repo := NewRepo(t)
	addBerth(t, repo, "feat-shown")

	out := mustBerth(t, repo, repo.Dir, "list").Stdout
	header, _, _ := strings.Cut(out, "\n")
	if !strings.Contains(strings.ToUpper(header), "USED") {
		t.Errorf("berth list has no last used column:\n%s", out)
	}
}
