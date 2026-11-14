//go:build e2e

package e2e

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPruneIdleRemovesBerthWhosePreRemoveSavesIntoItsWorktree(t *testing.T) {
	repo := NewRepo(t)
	writeFile(t, repo.privateConfig(), "[hooks]\npre-remove = "+tomlString(`echo saved > "$BERTH_PATH/dump.sql"`)+"\n")
	wt, s := idleCandidate(t, repo, "feat-dump")
	ageBerth(t, repo, wt, s.ID, time.Now().Add(-30*day))

	pruneJSON(t, repo, "--idle", "7d")
	if !pruned(t, repo, s.ID) {
		t.Errorf("prune --idle 7d kept berth %s because its own pre-remove wrote into its worktree", s.ID)
	}
	if got := readString(t, filepath.Join(wt, "dump.sql")); got != "saved\n" {
		t.Errorf("the dump pre-remove saved reads %q after prune, want %q", got, "saved\n")
	}
}
