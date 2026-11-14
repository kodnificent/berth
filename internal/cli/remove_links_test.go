package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveUntrackedDeletesALinkRecordedAsOursAndNeverItsTarget(t *testing.T) {
	root, target := t.TempDir(), t.TempDir()
	mainFile := filepath.Join(target, "kept.txt")
	if err := os.WriteFile(mainFile, []byte("main's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("this machine can't make symlinks: %v", err)
	}
	if err := removeUntracked(root, "linked", map[string]bool{"linked/tracked.txt": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("the link is still there: %v", err)
	}
	if _, err := os.Stat(mainFile); err != nil {
		t.Errorf("removing the link reached its target: %v", err)
	}
}
