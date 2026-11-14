package lockfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestTheHolderCanDeleteALockFileItHasOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	f, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if err := os.Remove(path); err != nil {
		t.Fatalf("deleting the open lock file: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the lock file is still there after it was deleted: %v", err)
	}
}

func TestOpenWithoutCreateLeavesAMissingFileMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	if _, err := Open(path, false); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open of a missing lock file without create: %v, want it not to exist", err)
	}
}
