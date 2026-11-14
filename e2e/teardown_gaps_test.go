//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

type removedJSON struct {
	Removed json.RawMessage `json:"removed"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// oneJSONObject decodes stdout into v and fails if anything but one JSON value is there.
func oneJSONObject(t *testing.T, stdout string, v any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	if err := dec.Decode(v); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("stdout holds more than one JSON value:\n%s", stdout)
	}
}

func plainBerth(t *testing.T, name string) (*Repo, string) {
	t.Helper()
	repo := composelessRepo(t)
	if err := os.Remove(filepath.Join(repo.Dir, ".env")); err != nil {
		t.Fatal(err)
	}
	wt := repo.AddWorktree(t, name)
	mustBerth(t, repo, wt, "add")
	return repo, wt
}

func TestRemoveDryRunWithNothingToDeleteListsAnEmptyArray(t *testing.T) {
	repo, wt := plainBerth(t, "feat-empty")
	var out removedJSON
	oneJSONObject(t, mustBerth(t, repo, wt, "remove", "--dry-run", "--json").Stdout, &out)
	if got := string(bytes.TrimSpace(out.Removed)); got != "[]" {
		t.Errorf("removed = %s, want [] for a berth with nothing to delete", got)
	}
}

func TestPruneDryRunListsAnOrphanWithNothingToDelete(t *testing.T) {
	repo, wt := plainBerth(t, "feat-gone")
	repo.Git(t, "worktree", "remove", "--force", wt)

	var out struct {
		Berths []removedJSON `json:"berths"`
	}
	oneJSONObject(t, mustBerth(t, repo, repo.Dir, "prune", "--dry-run", "--json").Stdout, &out)
	if len(out.Berths) != 1 {
		t.Fatalf("prune --dry-run lists %d berths, want the one orphan", len(out.Berths))
	}
	if got := string(bytes.TrimSpace(out.Berths[0].Removed)); got != "[]" {
		t.Errorf("the orphan's removed = %s, want []", got)
	}
	if human := mustBerth(t, repo, repo.Dir, "prune", "--dry-run").Stdout; !strings.Contains(human, "Would remove berth") {
		t.Errorf("prune --dry-run does not name the orphan it would remove:\n%s", human)
	}
}

func TestRemoveThatFailsPartwayPrintsOneJSONObject(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the test's user can't delete from")
	}
	repo := composelessRepo(t)
	if err := os.MkdirAll(filepath.Join(repo.Dir, "cache", "held"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo.Dir, "cache", "held", "file"), "x")
	writeFile(t, filepath.Join(repo.Dir, ".git", "info", "exclude"), "cache\n")
	writeFile(t, repo.privateConfig(), "[files]\nclone = [\"cache\"]\n")
	wt := repo.AddWorktree(t, "feat-stuck")
	mustBerth(t, repo, wt, "add")
	held := filepath.Join(wt, "cache", "held")
	if err := os.Chmod(held, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(held, 0o755) })

	res := repo.Berth(t, wt, "remove", "--json")
	if res.Exit != 1 {
		t.Fatalf("remove exits %d, want 1 when a file can't be deleted\n%s", res.Exit, res.Stderr)
	}
	var out removedJSON
	oneJSONObject(t, res.Stdout, &out)
	var removed []struct{ Kind, Name string }
	if err := json.Unmarshal(out.Removed, &removed); err != nil || removed == nil {
		t.Fatalf("removed is not an array (%v):\n%s", err, res.Stdout)
	}
	var names []string
	for _, r := range removed {
		names = append(names, r.Kind+" "+r.Name)
	}
	if !slices.Contains(names, "file .env") || slices.Contains(names, "file cache") {
		t.Errorf("removed = %v, want the .env berth deleted and not the cache it could not", names)
	}
	if out.Error == nil || out.Error.Code != "error" || !strings.Contains(out.Error.Message, "cache") {
		t.Errorf("the object carries no error naming what was left:\n%s", res.Stdout)
	}

	if err := os.Chmod(held, 0o755); err != nil {
		t.Fatal(err)
	}
	mustBerth(t, repo, wt, "remove")
}

func TestResetRefusesWhileContainersRunWithNoCloneVolumes(t *testing.T) {
	repo := NewRepo(t)
	ignoreSQLite(t, repo)
	rel := "database/app.sqlite"
	makeLedger(t, filepath.Join(repo.Dir, rel), "delete", 20, 50)
	writeFile(t, repo.privateConfig(), sqliteConfig(rel))
	wt := repo.AddWorktree(t, "feat-running")
	mustBerth(t, repo, wt, "add", "--wait")
	berthDB := filepath.Join(wt, rel)
	sqlite(t, berthDB, "CREATE TABLE berth_only (v INTEGER);")
	repo.Compose(t, wt, "up", "-d", "--wait", "search")

	res := repo.Berth(t, wt, "reset")
	if res.Exit != 1 || !strings.Contains(res.Stderr, "are running") {
		t.Fatalf("reset with a container running exits %d, want 1 naming the running containers\n%s", res.Exit, res.Stderr)
	}
	if got := sqlite(t, berthDB, "SELECT COUNT(*) FROM sqlite_master WHERE name = 'berth_only';"); got != "1" {
		t.Errorf("reset discarded the berth's database though it refused")
	}

	repo.Compose(t, wt, "stop")
	mustBerth(t, repo, wt, "reset")
	mustBerth(t, repo, wt, "status", "--wait")
	if got := sqlite(t, berthDB, "SELECT COUNT(*) FROM sqlite_master WHERE name = 'berth_only';"); got != "0" {
		t.Errorf("reset with the containers stopped left the berth's old database")
	}
}

func TestListResumesMainAfterACloneIsKilled(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	repo.Compose(t, repo.Dir, "build", "web")
	user := strings.TrimSpace(repo.Compose(t, repo.Dir, "run", "-d", "--no-deps", "web", "sleep", "600"))
	wt := repo.AddWorktree(t, "feat-list")

	if res := berthWithDelay(t, repo, wt, "120s", "add"); res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s", res.Exit, res.Stderr)
	}
	pid := stateOf(t, repo, wt).volume(t, "cache").PID
	waitFor(t, 30*time.Second, "main's container on the cache volume to pause for the copy", func() bool {
		_, paused := containerState(t, user)
		return paused
	})

	mustBerth(t, repo, repo.Dir, "list")
	if _, paused := containerState(t, user); !paused {
		t.Fatalf("berth list resumed main's container while the copy that paused it was still running")
	}
	if got := stateOf(t, repo, wt).volume(t, "cache").Status; got != "cloning" {
		t.Fatalf("after berth list the running copy reads %q, want cloning", got)
	}

	if err := killNow(pid); err != nil {
		t.Fatalf("kill -9 %d: %v", pid, err)
	}
	waitFor(t, 10*time.Second, "the clone process to die", func() bool { return !alive(pid) })

	mustBerth(t, repo, repo.Dir, "list")
	if running, paused := containerState(t, user); !running || paused {
		t.Errorf("after berth list main's container is running=%v paused=%v, want running and not paused", running, paused)
	}
	if got := stateOf(t, repo, wt).volume(t, "cache").Status; got != "failed" {
		t.Errorf("the killed clone reads %q, want failed", got)
	}
}

func TestPruneThatCannotReachDockerPrintsOneJSONObject(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-nodocker")
	mustBerth(t, repo, wt, "add")
	repo.Git(t, "worktree", "remove", "--force", wt)

	res := repo.BerthWith(t, deadDocker(t), repo.Dir, "prune", "--json")
	if res.Exit != 1 {
		t.Fatalf("prune exits %d, want 1 when Docker can't be asked\n%s", res.Exit, res.Stderr)
	}
	var out struct {
		Berths json.RawMessage `json:"berths"`
		Error  *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	oneJSONObject(t, res.Stdout, &out)
	if got := string(bytes.TrimSpace(out.Berths)); !strings.HasPrefix(got, "[") {
		t.Errorf("berths = %s, want an array", got)
	}
	if out.Error == nil || out.Error.Code != "error" {
		t.Errorf("the object carries no error:\n%s", res.Stdout)
	}
	mustBerth(t, repo, repo.Dir, "prune")
}

func TestResetRefusesWhenDockerCannotSayWhatIsRunning(t *testing.T) {
	repo := NewRepo(t)
	ignoreSQLite(t, repo)
	rel := "database/app.sqlite"
	makeLedger(t, filepath.Join(repo.Dir, rel), "delete", 20, 50)
	writeFile(t, repo.privateConfig(), sqliteConfig(rel))
	wt := repo.AddWorktree(t, "feat-nodocker-reset")
	mustBerth(t, repo, wt, "add", "--wait")
	berthDB := filepath.Join(wt, rel)
	sqlite(t, berthDB, "CREATE TABLE berth_only (v INTEGER);")

	res := repo.BerthWith(t, deadDocker(t), wt, "reset")
	if res.Exit != 1 || !strings.Contains(res.Stderr, "Docker is not reachable") {
		t.Fatalf("reset with Docker unreachable exits %d, want 1 saying Docker is not reachable\n%s", res.Exit, res.Stderr)
	}
	if got := sqlite(t, berthDB, "SELECT COUNT(*) FROM sqlite_master WHERE name = 'berth_only';"); got != "1" {
		t.Errorf("reset discarded the berth's database though it could not ask Docker what is running")
	}
}
