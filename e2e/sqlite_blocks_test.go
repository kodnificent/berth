//go:build e2e

package e2e

import (
	"path/filepath"
	"testing"
)

// On APFS the berth's copy of a database shares its blocks with main's file
// rather than holding a second copy of them.
func TestSQLiteCopySharesBlocksOnAPFS(t *testing.T) {
	repo := NewRepo(t)
	requireAPFS(t, repo.Dir)
	ignoreSQLite(t, repo)
	rel := "database/app.sqlite"
	db := filepath.Join(repo.Dir, rel)
	makeLedger(t, db, "delete", 8000, 2000)
	writeFile(t, repo.privateConfig(), sqliteConfig(rel))

	wt := repo.AddWorktree(t, "feat-cow")
	mustBerth(t, repo, wt, "add", "--wait")

	berthDB := filepath.Join(wt, rel)
	if !fileExistsAt(berthDB) {
		t.Fatalf("add did not clone %s into the berth", rel)
	}
	if fileSum(t, berthDB) != fileSum(t, db) {
		t.Fatalf("the berth's quiet copy of %s differs from main's byte for byte, so it cannot share its blocks", rel)
	}
	for _, off := range blockOffsets(t, db) {
		if a, b := physicalOffset(t, db, off), physicalOffset(t, berthDB, off); a != b {
			t.Errorf("at byte %d main's %s is stored at %d on disk and the berth's copy at %d; want one shared block", off, rel, a, b)
		}
	}
}
