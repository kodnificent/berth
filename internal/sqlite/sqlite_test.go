package sqlite

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestCloneRefusesAFileThatIsNotADatabase(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "notes.db")
	if err := os.WriteFile(src, []byte("plain text, long enough to fill a SQLite header and then some more, and a little more besides"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "copy.db")
	if _, err := Clone(src, dst, Options{}); err == nil {
		t.Fatal("Clone copied a file that is not a SQLite database")
	}
	if _, err := os.Lstat(dst); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Clone left %s behind after refusing (err %v)", dst, err)
	}
}

// A -shm that appears during every attempt means a connection to main kept opening the
// WAL while berth copied without its lock, so no attempt can be trusted.
func TestCloneFailsWhenTheWALKeepsOpeningDuringTheCopy(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "app.sqlite")
	header := make([]byte, 4096)
	copy(header, magic)
	header[18], header[19] = 2, 2
	if err := os.WriteFile(src, header, 0o644); err != nil {
		t.Fatal(err)
	}
	// A dangling link can't be opened but is there to be seen, as a -shm created between
	// the open and the check would be on every attempt.
	if err := os.Symlink(filepath.Join(dir, "gone"), src+"-shm"); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "copy.sqlite")

	if _, err := Clone(src, dst, Options{}); err == nil {
		t.Error("Clone reported success though no attempt held the WAL still")
	}
}
