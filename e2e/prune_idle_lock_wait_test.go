//go:build e2e

package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPruneIdleWithoutHookKeepsBerthCommittedWhileItWaitsForTheLock(t *testing.T) {
	repo := NewRepo(t)
	wt, s := idleCandidate(t, repo, "feat-waits")
	ageBerth(t, repo, wt, s.ID, time.Now().Add(-30*day))
	admin := strings.TrimSpace(repo.mustRun(t, wt, "git", "rev-parse", "--absolute-git-dir"))
	lock, err := os.OpenFile(filepath.Join(admin, "berth-lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	// Standing in for a berth remove or another prune that holds the worktree's lock.
	if err := lockLikeBerth(lock); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(berthBin, "prune", "--idle", "7d")
	cmd.Dir, cmd.Env = repo.Dir, repo.Env()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	repo.mustRun(t, wt, "git", "commit", "-q", "--allow-empty", "-m", "while prune waits")
	time.Sleep(time.Second)
	if err := unlockLikeBerth(lock); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Errorf("prune --idle 7d: %v\n%s", err, out.String())
	}
	if pruned(t, repo, s.ID) {
		t.Errorf("prune --idle 7d pruned berth %s, committed to while prune waited for its worktree lock\n%s", s.ID, out.String())
	}
}
