//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func diskBytes(t *testing.T, repo *Repo, worktree string) int64 {
	t.Helper()
	res := mustBerth(t, repo, repo.Dir, "list", "--json")
	var l struct {
		Berths []struct {
			Worktree  string `json:"worktree"`
			DiskBytes *int64 `json:"disk_bytes"`
		} `json:"berths"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &l); err != nil {
		t.Fatalf("berth list --json: %v\n%s", err, res.Stdout)
	}
	for _, b := range l.Berths {
		if b.Worktree == worktree && b.DiskBytes != nil {
			return *b.DiskBytes
		}
	}
	t.Fatalf("berth list --json has no disk_bytes for %s:\n%s", worktree, res.Stdout)
	return 0
}

// berth list reports the size berth recorded when it cloned, without walking the
// cloned trees again, so it returns promptly however large a berth's dependencies are.
func TestListDoesNotWalkClonedTrees(t *testing.T) {
	repo := NewRepo(t)
	appendFile(t, filepath.Join(repo.Dir, ".gitignore"), "deps/\n")
	repo.Git(t, "commit", "-q", "-am", "ignore deps")
	deps := filepath.Join(repo.Dir, "deps")
	t.Cleanup(func() { os.RemoveAll(deps) })
	const files, size = 20000, 1024
	content := make([]byte, size)
	for i := 0; i < files; i++ {
		dir := filepath.Join(deps, fmt.Sprintf("pkg%03d", i%200))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%05d.js", i)), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, repo.privateConfig(), "[files]\nclone = [\"deps\"]\n")
	wt := repo.AddWorktree(t, "feat-deps")
	mustBerth(t, repo, wt, "add")

	before := diskBytes(t, repo, wt)
	if before < files*size {
		t.Errorf("berth list reports %d bytes for a berth that cloned %d bytes of deps, want at least that", before, files*size)
	}

	// A sparse file takes no disk but has a large apparent size, so a walk would show it.
	f, err := os.Create(filepath.Join(wt, "deps", "grown.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1 << 30); err != nil {
		t.Fatal(err)
	}
	f.Close()

	start := time.Now()
	after := diskBytes(t, repo, wt)
	took := time.Since(start)
	t.Logf("berth list --json with %d cloned files took %s", files, took)
	if after != before {
		t.Errorf("berth list reports %d bytes after a file was added to the cloned deps, %d before: it walked the cloned tree", after, before)
	}
	if took > 2*time.Second {
		t.Errorf("berth list took %s, want it to return promptly", took)
	}
	if out := mustBerth(t, repo, repo.Dir, "list").Stdout; out == "" {
		t.Error("berth list printed nothing")
	}
}
