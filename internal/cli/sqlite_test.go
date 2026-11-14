package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/volume"
	"github.com/kodnificent/berth/internal/worktree"
)

func copyingRecord(t *testing.T) (registry.Record, *worktree.Worktree) {
	t.Helper()
	admin := t.TempDir()
	rec := registry.Record{AdminDir: admin, Worktree: t.TempDir(), SQLite: []registry.SQLiteFile{{Path: "database/app.sqlite", Own: true, Status: volume.Cloning}}}
	return rec, &worktree.Worktree{AdminDir: admin, Path: rec.Worktree}
}

func TestInterruptedSQLiteCopyReadsAsFailed(t *testing.T) {
	rec, _ := copyingRecord(t)

	got := sqliteEntries(rec)
	if got[0].Status != volume.Failed || got[0].Error == "" {
		t.Errorf("a copy recorded as under way with no berth command running reads %q (%q), want failed with a reason", got[0].Status, got[0].Error)
	}
}

func TestSQLiteCopyUnderWayReadsAsCloning(t *testing.T) {
	rec, w := copyingRecord(t)
	lock, err := registry.LockWorktree(w)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	if got := sqliteEntries(rec); got[0].Status != volume.Cloning {
		t.Errorf("a copy add is still making reads %q, want cloning", got[0].Status)
	}
}

func TestRemovingSQLiteDirsKeepsAnyoneElsesFiles(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"database/sqlite", "storage"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "storage", "notes.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	removeSQLiteDirs(root, []registry.SQLiteFile{
		{Path: "database/sqlite/app.sqlite", Dirs: []string{"database", "database/sqlite"}},
		{Path: "storage/cache.db", Dirs: []string{"storage"}},
	})

	if _, err := os.Stat(filepath.Join(root, "database")); !os.IsNotExist(err) {
		t.Errorf("database, which berth made and left empty, is still there (err %v)", err)
	}
	if _, err := os.Stat(filepath.Join(root, "storage", "notes.txt")); err != nil {
		t.Errorf("removing berth's directories took a file berth never made: %v", err)
	}
}
