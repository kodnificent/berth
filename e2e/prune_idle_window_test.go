//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// earlierAndLater makes idle berths feat-a and feat-b and prunes them. The first whose
// pre-remove runs does earlier in the other's worktree and lingers; the other's pre-remove,
// should it run, records its path and does later. It returns the berth whose hook ran
// first, the other one, the paths the later hook ran for, and prune's result.
func earlierAndLater(t *testing.T, repo *Repo, earlier, later string) (first, second stateStatus, laterRan []string, res Result) {
	t.Helper()
	marks := t.TempDir()
	mark, ran := filepath.Join(marks, "first"), filepath.Join(marks, "later.ran")
	a := filepath.Join(filepath.Dir(repo.Dir), "worktrees", "feat-a")
	b := filepath.Join(filepath.Dir(repo.Dir), "worktrees", "feat-b")
	// A berth command the earlier action runs sets inner, so the hook it runs in turn
	// does nothing and is not mistaken for prune running the later hook.
	script := `[ -n "$INNER" ] && exit 0
for v in $(env | sed -n 's/^\(COMPOSE_[A-Z_]*\)=.*/\1/p'); do unset "$v"; done
if mkdir ` + shellQuote(mark) + ` 2>/dev/null; then
  if [ "$BERTH_PATH" = ` + shellQuote(a) + ` ]; then other=` + shellQuote(b) + `; else other=` + shellQuote(a) + `; fi
  echo "$other" > ` + shellQuote(filepath.Join(mark, "other")) + `
  (cd "$other" && ` + earlier + `)
  sleep 2
else
  echo "$BERTH_PATH" >> ` + shellQuote(ran) + `
  ` + later + `
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

	res = repo.Berth(t, repo.Dir, "prune", "--idle", "7d")
	other := strings.TrimSpace(readString(t, filepath.Join(mark, "other")))
	for wt, s := range states {
		if wt == other {
			second = s
		} else {
			first = s
		}
	}
	return first, second, lines(t, ran), res
}

func TestPruneIdleKeepsBerthCommittedAndEditedBeforeItsTurnWithoutRunningItsHook(t *testing.T) {
	repo := NewRepo(t)
	_, second, laterRan, _ := earlierAndLater(t, repo,
		`git commit -q --allow-empty -m "used during prune" && echo edit >> web/index.html`,
		`git stash -u -q`)
	if pruned(t, repo, second.ID) {
		t.Errorf("prune --idle 7d pruned berth %s, committed to and edited after the list, once its pre-remove stashed the edit", second.ID)
	}
	if len(laterRan) > 0 {
		t.Errorf("pre-remove ran for %v, a berth used after the list", laterRan)
	}
}

func TestPruneIdleKeepsBerthStartedBeforeItsTurnWithoutRunningItsHook(t *testing.T) {
	repo := NewRepo(t)
	_, second, laterRan, _ := earlierAndLater(t, repo,
		`docker compose up -d --no-deps search >/dev/null 2>&1`,
		`docker compose stop >/dev/null 2>&1`)
	if len(labelled(t, "container", second.ComposeProject)) == 0 {
		t.Fatalf("the earlier pre-remove did not start a container for berth %s", second.ID)
	}
	if pruned(t, repo, second.ID) {
		t.Errorf("prune --idle 7d pruned berth %s, started after the list, once its pre-remove stopped the stack", second.ID)
	}
	if len(laterRan) > 0 {
		t.Errorf("pre-remove ran for %v, a berth started after the list", laterRan)
	}
}

func TestPruneIdleKeepsBerthLockedBeforeItsTurn(t *testing.T) {
	repo := NewRepo(t)
	_, second, _, _ := earlierAndLater(t, repo, `git worktree lock "$PWD"`, `true`)
	if pruned(t, repo, second.ID) {
		t.Errorf("prune --idle 7d pruned berth %s, whose worktree was locked after the list", second.ID)
	}
}

// The earlier action sets the worktree's times back after the remove, so it still looks
// idle and only the berth being gone can stop its hook.
func TestPruneIdleSkipsBerthRemovedBeforeItsTurn(t *testing.T) {
	repo := NewRepo(t)
	_, second, laterRan, res := earlierAndLater(t, repo, `INNER=1 `+shellQuote(berthBin)+` remove >/dev/null && find . "$(git rev-parse --absolute-git-dir)" -exec touch -h -t 202001010000 {} +`, `true`)
	if res.Exit != 0 {
		t.Errorf("prune --idle 7d with a berth removed during an earlier hook exited %d, want 0\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	if len(laterRan) > 0 {
		t.Errorf("pre-remove ran for %v, a berth already removed", laterRan)
	}
	if _, err := os.Lstat(filepath.Join(repo.StateDir, "hooks", second.ID+".post-add.lock")); !os.IsNotExist(err) {
		t.Errorf("prune --idle 7d left a post-add lock for removed berth %s (%v)", second.ID, err)
	}
}
