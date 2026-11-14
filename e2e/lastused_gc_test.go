//go:build e2e

package e2e

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// backdate sets the modification time of everything in the worktree and in git's
// administrative directory for it to long ago, so only what happens next is recent.
func backdate(t *testing.T, wt string) {
	t.Helper()
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(readString(t, filepath.Join(wt, ".git"))), "gitdir: ")
	if !ok {
		t.Fatalf("%s/.git does not point at an administrative directory", wt)
	}
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, root := range []string{wt, gitdir} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			return os.Chtimes(path, old, old)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestGitGCInMainLeavesLastUsed(t *testing.T) {
	repo := NewRepo(t)
	wt, id := addBerth(t, repo, "feat-gc")
	backdate(t, wt)
	before := useOf(t, repo, id)
	time.Sleep(tick)

	repo.Git(t, "gc", "-q")
	repo.Git(t, "reflog", "expire", "--all")
	if after := useOf(t, repo, id); !after.LastUsed.Equal(before.LastUsed) {
		t.Errorf("git gc and git reflog expire in main moved last_used from %s to %s, want it unchanged", before.LastUsed.Format(time.RFC3339Nano), after.LastUsed.Format(time.RFC3339Nano))
	}
}

func TestCommitInTheWorktreeMovesLastUsed(t *testing.T) {
	repo := NewRepo(t)
	wt, id := addBerth(t, repo, "feat-commit")
	backdate(t, wt)
	before := useOf(t, repo, id)
	time.Sleep(tick)

	start := time.Now()
	repo.mustRun(t, wt, "git", "commit", "-q", "--allow-empty", "-m", "empty")
	expectMovedTo(t, "a commit in the worktree", before, useOf(t, repo, id), start)
}
