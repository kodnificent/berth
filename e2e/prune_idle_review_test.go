//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPruneIdleKeepsBerthWhoseGitStatusFails(t *testing.T) {
	repo := NewRepo(t)
	broken, brokenS := idleCandidate(t, repo, "feat-status-fails")
	idle, idleS := idleCandidate(t, repo, "feat-idle")
	ageBerth(t, repo, broken, brokenS.ID, time.Now().Add(-30*day))
	ageBerth(t, repo, idle, idleS.ID, time.Now().Add(-30*day))

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shim := t.TempDir()
	writeFile(t, filepath.Join(shim, "git"), `#!/bin/sh
case " $* " in *feat-status-fails*" status "*) echo "fatal: status refused" >&2; exit 128 ;; esac
exec `+shellQuote(realGit)+` "$@"
`)
	if err := makeExecutable(filepath.Join(shim, "git")); err != nil {
		t.Fatal(err)
	}

	res := repo.BerthWith(t, []string{"PATH=" + shim + string(os.PathListSeparator) + os.Getenv("PATH")}, repo.Dir, "prune", "--idle", "7d")
	if res.Exit != 1 {
		t.Errorf("prune --idle 7d with git status failing in one worktree exited %d, want 1\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	reported := false
	for _, line := range strings.Split(res.Stdout+res.Stderr, "\n") {
		reported = reported || strings.Contains(line, brokenS.ID) && strings.Contains(strings.ToLower(line), "unknown")
	}
	if !reported {
		t.Errorf("prune --idle 7d does not report berth %s, whose git status failed, as unknown:\nstdout:\n%s\nstderr:\n%s", brokenS.ID, res.Stdout, res.Stderr)
	}
	if pruned(t, repo, brokenS.ID) || !fileExistsAt(filepath.Join(broken, ".env")) {
		t.Errorf("prune --idle 7d pruned berth %s, whose git status failed", brokenS.ID)
	}
	if !pruned(t, repo, idleS.ID) {
		t.Errorf("prune --idle 7d kept berth %s, unused for 30 days with a working git status", idleS.ID)
	}
}

func TestPruneIdleKeepsBerthWhosePreRemoveStartsItsOwnContainer(t *testing.T) {
	repo := NewRepo(t)
	script := `set -e
cd "$BERTH_PATH"
docker compose up -d --no-deps search >/dev/null`
	writeFile(t, repo.privateConfig(), "[hooks]\npre-remove = "+tomlString(script)+"\n")
	wt, s := idleCandidate(t, repo, "feat-own-start")
	ageBerth(t, repo, wt, s.ID, time.Now().Add(-30*day))

	res := repo.Berth(t, repo.Dir, "prune", "--idle", "7d")
	if res.Exit != 0 {
		t.Errorf("prune --idle 7d with a pre-remove starting its own container exited %d, want 0\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "kept berth "+s.ID) {
		t.Errorf("prune --idle 7d does not name berth %s as kept on stderr:\n%s", s.ID, res.Stderr)
	}
	if len(labelled(t, "container", s.ComposeProject)) == 0 {
		t.Errorf("berth %s has no container after its pre-remove started one and prune ran", s.ID)
	}
	if pruned(t, repo, s.ID) || !fileExistsAt(filepath.Join(wt, ".env")) {
		t.Errorf("prune --idle 7d pruned berth %s, whose pre-remove started a container", s.ID)
	}
}

func TestPruneIdleKeepsBerthWhosePreRemoveRecordsAUse(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	script := `set -e
cd "$BERTH_PATH"
env -u BERTH_PATH ` + shellQuote(berthBin) + ` status >/dev/null`
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n\n[hooks]\npre-remove = "+tomlString(script)+"\n")
	wt, s := idleCandidate(t, repo, "feat-own-use")
	ageBerth(t, repo, wt, s.ID, time.Now().Add(-30*day))

	res := repo.Berth(t, repo.Dir, "prune", "--idle", "7d")
	if res.Exit != 0 {
		t.Errorf("prune --idle 7d with a pre-remove recording a use exited %d, want 0\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "kept berth "+s.ID) {
		t.Errorf("prune --idle 7d does not name berth %s as kept on stderr:\n%s", s.ID, res.Stderr)
	}
	if !dockerHas("volume", s.ComposeProject+"_cache") {
		t.Errorf("prune --idle 7d deleted volume %s_cache of berth %s, used during its pre-remove", s.ComposeProject, s.ID)
	}
	if pruned(t, repo, s.ID) || !fileExistsAt(filepath.Join(wt, ".env")) {
		t.Errorf("prune --idle 7d pruned berth %s, used by a berth command during its pre-remove", s.ID)
	}
}
