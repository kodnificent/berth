//go:build e2e

package e2e

import (
	"path/filepath"
	"testing"
)

// A WAL the berth's app left beside its copy belongs to that copy; replayed into the copy
// reset makes, it would bring back the berth's old pages.
func TestSQLiteResetDiscardsTheBerthsLeftoverWAL(t *testing.T) {
	repo := NewRepo(t)
	ignoreSQLite(t, repo)
	rel := "database/app.sqlite"
	db := filepath.Join(repo.Dir, rel)
	makeLedger(t, db, "wal", 200, 500)
	writeFile(t, repo.privateConfig(), sqliteConfig(rel))
	wt := repo.AddWorktree(t, "feat-wal")
	mustBerth(t, repo, wt, "add", "--wait")
	berthDB := filepath.Join(wt, rel)
	sqlite(t, berthDB, ".dbconfig no_ckpt_on_close on\nPRAGMA wal_autocheckpoint=0;\nINSERT INTO ledger (amount, pad) SELECT 7, randomblob(2000) FROM ledger LIMIT 50;\n")
	if !fileExistsAt(berthDB + "-wal") {
		t.Fatal("the berth's app left no WAL beside its copy, so this proves nothing")
	}
	sqlite(t, db, "INSERT INTO ledger (amount) VALUES (11), (13);")

	mustBerth(t, repo, wt, "reset")

	if got := sqlite(t, berthDB, "PRAGMA integrity_check;"); got != "ok" {
		t.Errorf("the berth's %s after reset fails PRAGMA integrity_check:\n%s", rel, got)
	}
	if dump(t, berthDB) != dump(t, db) {
		t.Errorf("after reset the berth's %s differs from main's, so its old WAL was replayed into the new copy", rel)
	}
}

// A database file the berth already had before add isn't berth's: add never overwrites
// it with main's, and remove never deletes it.
func TestSQLiteFileTheBerthAlreadyHadIsLeftAlone(t *testing.T) {
	repo := NewRepo(t)
	ignoreSQLite(t, repo)
	rel := "database/app.sqlite"
	makeLedger(t, filepath.Join(repo.Dir, rel), "delete", 50, 100)
	writeFile(t, repo.privateConfig(), sqliteConfig(rel))
	wt := repo.AddWorktree(t, "feat-own")
	own := filepath.Join(wt, rel)
	makeLedger(t, own, "delete", 5, 10)
	before := fileSum(t, own)

	if res := repo.Berth(t, wt, "add", "--wait"); res.Exit != 4 {
		t.Errorf("add --wait exited %d with a listed file it could not copy, want 4\nstderr:\n%s", res.Exit, res.Stderr)
	}
	if fileSum(t, own) != before {
		t.Errorf("add overwrote the berth's own %s", rel)
	}

	removeJSON(t, repo, wt)
	if !fileExistsAt(own) {
		t.Errorf("remove deleted %s, which the berth had before add", rel)
	}
}
