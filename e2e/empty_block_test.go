//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func composelessRepo(t *testing.T) *Repo {
	t.Helper()
	repo := NewRepo(t)
	repo.Git(t, "rm", "-q", "compose.yml")
	repo.Git(t, "commit", "-q", "-m", "no compose project")
	return repo
}

func statusFiles(t *testing.T, repo *Repo, dir string) []string {
	t.Helper()
	var s struct {
		Files []string `json:"files"`
	}
	out := mustBerth(t, repo, dir, "status", "--json").Stdout
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatalf("status --json: %v\n%s", err, out)
	}
	if s.Files == nil {
		t.Errorf("status --json has no files list:\n%s", out)
	}
	return s.Files
}

func TestBerthWithNothingToSetWritesNoEnv(t *testing.T) {
	repo := composelessRepo(t)
	if err := os.Remove(filepath.Join(repo.Dir, ".env")); err != nil {
		t.Fatal(err)
	}
	wt := repo.AddWorktree(t, "feat-plain")
	mustBerth(t, repo, wt, "add")

	if _, err := os.Stat(filepath.Join(wt, ".env")); !os.IsNotExist(err) {
		t.Errorf("add created a .env with nothing to set in it (err %v)", err)
	}
	if files := statusFiles(t, repo, wt); slices.Contains(files, ".env") {
		t.Errorf("status lists .env among the berth's files, though berth wrote none: %v", files)
	}
	mustBerth(t, repo, wt, "remove")
	if out := repo.Git(t, "-C", wt, "status", "--porcelain", "--ignored"); strings.TrimSpace(out) != "" {
		t.Errorf("the worktree is not as it was before add:\n%s", out)
	}
}

func TestClonedEnvGetsNoBlockWhenNothingIsSet(t *testing.T) {
	repo := composelessRepo(t)
	source := readString(t, filepath.Join(repo.Dir, ".env"))
	wt := repo.AddWorktree(t, "feat-cloned")
	mustBerth(t, repo, wt, "add")

	if got := readString(t, filepath.Join(wt, ".env")); got != source {
		t.Errorf("the berth's .env differs from main's, though berth had nothing to set\nmain:\n%q\nberth:\n%q", source, got)
	}
}

func TestBlockGoesWhenItsLastKeyDoes(t *testing.T) {
	repo := composelessRepo(t)
	source := readString(t, filepath.Join(repo.Dir, ".env"))
	writeFile(t, repo.privateConfig(), "[env.set]\nGREETING = \"hello {name}\"\n")
	wt := repo.AddWorktree(t, "feat-emptied")
	mustBerth(t, repo, wt, "add")
	if _, block, _ := splitBlock(t, readString(t, filepath.Join(wt, ".env"))); !strings.Contains(block, "GREETING=") {
		t.Fatalf("the managed block does not set GREETING:\n%s", block)
	}

	writeFile(t, repo.privateConfig(), "")
	mustBerth(t, repo, wt, "add")
	if got := readString(t, filepath.Join(wt, ".env")); got != source {
		t.Errorf("with its last key gone, .env should be main's again\nmain:\n%q\nberth:\n%q", source, got)
	}

	writeFile(t, repo.privateConfig(), "[env.set]\nGREETING = \"hello {name}\"\n")
	mustBerth(t, repo, wt, "add")
	before, block, _ := splitBlock(t, readString(t, filepath.Join(wt, ".env")))
	if before != source || !strings.Contains(block, "GREETING=") {
		t.Errorf("the block did not come back whole after its key did\noutside the block:\n%q\nblock:\n%s", before, block)
	}
}

func TestStatusListsEnvOnlyWhileTheBlockHoldsAKey(t *testing.T) {
	repo := composelessRepo(t)
	if err := os.Remove(filepath.Join(repo.Dir, ".env")); err != nil {
		t.Fatal(err)
	}
	const own = "MINE=1\n"
	wt := repo.AddWorktree(t, "feat-listed")
	writeFile(t, filepath.Join(wt, ".env"), own)

	writeFile(t, repo.privateConfig(), "[env.set]\nGREETING = \"hello {name}\"\n")
	mustBerth(t, repo, wt, "add")
	if files := statusFiles(t, repo, wt); !slices.Contains(files, ".env") {
		t.Errorf("status does not list .env, though berth wrote its block there: %v", files)
	}

	writeFile(t, repo.privateConfig(), "")
	mustBerth(t, repo, wt, "add")
	if got := readString(t, filepath.Join(wt, ".env")); got != own {
		t.Errorf("with the block gone, .env should hold only the worktree's own line, got:\n%q", got)
	}
	if files := statusFiles(t, repo, wt); slices.Contains(files, ".env") {
		t.Errorf("status lists .env, though berth neither made it nor has a block in it: %v", files)
	}

	writeFile(t, repo.privateConfig(), "[env.set]\nGREETING = \"hello {name}\"\n")
	mustBerth(t, repo, wt, "add")
	if files := statusFiles(t, repo, wt); !slices.Contains(files, ".env") {
		t.Errorf("status does not list .env after its key came back: %v", files)
	}
}

func TestEnvBerthCreatedStaysItsOwnAfterItsLastKeyGoes(t *testing.T) {
	repo := composelessRepo(t)
	if err := os.Remove(filepath.Join(repo.Dir, ".env")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo.privateConfig(), "[env.set]\nGREETING = \"hello {name}\"\n")
	wt := repo.AddWorktree(t, "feat-created")
	mustBerth(t, repo, wt, "add")
	const added = "MINE=1\n"
	appendFile(t, filepath.Join(wt, ".env"), added)

	writeFile(t, repo.privateConfig(), "")
	mustBerth(t, repo, wt, "add")
	if got := readString(t, filepath.Join(wt, ".env")); got != added {
		t.Errorf("with the block gone, .env should hold only the line added since, got:\n%q", got)
	}
	if files := statusFiles(t, repo, wt); !slices.Contains(files, ".env") {
		t.Errorf("status does not list the .env berth created: %v", files)
	}

	mustBerth(t, repo, wt, "remove")
	if out := repo.Git(t, "-C", wt, "status", "--porcelain", "--ignored"); strings.TrimSpace(out) != "" {
		t.Errorf("the worktree is not as it was before add:\n%s", out)
	}
}
