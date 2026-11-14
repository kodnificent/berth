package clone

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kodnificent/berth/internal/winsys"
	"golang.org/x/sys/windows"
)

// A directory link whose target sorts after it has nothing at the target yet when the
// walk reaches it, and Windows fixes a link as a file or a directory when it is made.
func TestDirectorySymlinkKeepsItsKindWhenItsTargetSortsLater(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "z-real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "z-real", "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("z-real", filepath.Join(src, "a-link")); err != nil {
		t.Skipf("this machine can't make symlinks: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "out")
	if _, _, err := copyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "a-link", "file.txt")); err != nil {
		t.Errorf("the copied directory link can't be followed: %v", err)
	}
}

func TestRetargetSeesAbsoluteLinkReachingMainThroughAJunction(t *testing.T) {
	src, elsewhere := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "real.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(elsewhere, "alias")
	if err := winsys.MakeJunction(src, alias); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(elsewhere, "berth")

	got, changed := Retarget(filepath.Join(alias, "real.txt"), src, src, dst)

	if want := filepath.Join(dst, "real.txt"); !changed || got != want {
		t.Errorf("Retarget through a junction = %q, %v; want %q, true", got, changed, want)
	}
}

func TestRetargetGivesARootedTargetTheVolumeOfItsLink(t *testing.T) {
	src := filepath.Join(filepath.VolumeName(os.TempDir())+`\`, "repo")
	dst := filepath.Join(filepath.VolumeName(os.TempDir())+`\`, "berths", "x")

	got, changed := Retarget(`\repo\a\b`, filepath.Join(src, "link"), src, dst)

	if want := filepath.Join(dst, "a", "b"); !changed || got != want {
		t.Errorf("Retarget of a rooted target = %q, %v; want %q, true", got, changed, want)
	}
}

func TestRetryAfterACopyFailedPartWayRepointsAJunction(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	real := filepath.Join(src, "d", "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	mainFile := filepath.Join(real, "file.txt")
	if err := os.WriteFile(mainFile, []byte("main's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := winsys.MakeJunction(real, filepath.Join(src, "d", "a-junction")); err != nil {
		t.Fatal(err)
	}
	held := filepath.Join(src, "d", "z-held")
	if err := os.WriteFile(held, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := windows.UTF16PtrFromString(held)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}

	_, failed := Merge(src, dst, "d")
	windows.CloseHandle(h)
	if failed == nil {
		t.Fatal("Merge with a file no one else may open succeeded, so this proves nothing")
	}
	if _, err := Merge(src, dst, "d"); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(dst, "d", "a-junction")
	if got, err := os.Readlink(link); err != nil || got != filepath.Join(dst, "d", "real") {
		t.Errorf("after the retry a-junction -> %q, %v; want the berth's own directory", got, err)
	}
	if err := os.WriteFile(filepath.Join(link, "file.txt"), []byte("the berth's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(mainFile); string(data) != "main's\n" {
		t.Errorf("a write through the berth's junction reached main's file: %q", data)
	}
}
