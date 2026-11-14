package clone

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetarget(t *testing.T) {
	// An absolute path on this platform: on Windows one needs a drive.
	abs := func(p string) string {
		if p == "" || !strings.HasPrefix(p, "/") {
			return filepath.FromSlash(p)
		}
		return filepath.VolumeName(os.TempDir()) + filepath.FromSlash(p)
	}
	src, dst := abs("/src"), abs("/berths/x")
	for _, c := range []struct {
		target, linkDir, want string
		changed               bool
	}{
		{"/src/a/b", "/src/link", "/berths/x/a/b", true},
		{"/src", "/src/link", "/berths/x", true},
		{"/srcother/a", "/src/link", "", false},
		{"/elsewhere", "/src/link", "", false},
		{"../a", "/src/link", "", false},
		{"../../outside", "/src/link", "/outside", true},
	} {
		c.target, c.linkDir, c.want = abs(c.target), abs(c.linkDir), abs(c.want)
		got, changed := Retarget(c.target, c.linkDir, src, dst)
		if got != c.want || changed != c.changed {
			t.Errorf("Retarget(%q from %s) = %q, %v; want %q, %v", c.target, c.linkDir, got, changed, c.want, c.changed)
		}
	}
}

func TestMergeFillsInOnlyWhatIsMissing(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write := func(root, rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(src, "d/tracked.txt", "source")
	write(src, "d/new/file.txt", "new")
	write(src, "d/only.txt", "only")
	write(dst, "d/tracked.txt", "berth")

	r, err := Merge(src, dst, "d")
	created, dirs := r.Created, r.Sizes
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]int64{filepath.Join("d", "new"): 3}; len(dirs) != 1 || dirs[filepath.Join("d", "new")] != 3 {
		t.Errorf("dirs = %v, want %v: the size of each created directory, and no files", dirs, want)
	}
	if want := []string{filepath.Join("d", "new"), filepath.Join("d", "only.txt")}; len(created) != 2 || created[0] != want[0] || created[1] != want[1] {
		t.Errorf("created = %v, want %v", created, want)
	}
	if data, _ := os.ReadFile(filepath.Join(dst, "d", "tracked.txt")); string(data) != "berth" {
		t.Errorf("Merge overwrote a file the berth already had: %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(dst, "d", "new", "file.txt")); string(data) != "new" {
		t.Errorf("d/new/file.txt = %q", data)
	}

	r, err = Merge(src, dst, filepath.Join("x", "y", "missing"))
	created = r.Created
	if err != nil || created != nil {
		t.Errorf("Merge of a path the source lacks = %v, %v", created, err)
	}
	write(src, "p/q/r.txt", "deep")
	r, err = Merge(src, dst, filepath.Join("p", "q", "r.txt"))
	created, dirs = r.Created, r.Sizes
	if err != nil || len(created) != 1 || created[0] != "p" {
		t.Errorf("Merge into missing parents created %v, %v; want [p]", created, err)
	}
	if dirs["p"] != 4 {
		t.Errorf("Merge into missing parents gave p the size %d, want the 4 bytes of r.txt", dirs["p"])
	}
}

func TestCopyTreeKeepsLinksAndModes(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Windows keeps only a read-only flag, so f reads back as 0666 there.
	written, err := os.Stat(filepath.Join(src, "f"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("f", filepath.Join(src, "l")); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "out")
	if _, _, err := copyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(dst, "f")); err != nil || info.Mode().Perm() != written.Mode().Perm() {
		t.Errorf("copied f: %v, %v", info, err)
	}
	if target, err := os.Readlink(filepath.Join(dst, "l")); err != nil || target != "f" {
		t.Errorf("copied link -> %q, %v", target, err)
	}
}

func TestProbeLeavesNothingInEitherDirectory(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if _, err := Probe(src, dst); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{src, dst} {
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("Probe left %v in %s", entries, dir)
		}
	}
}

// A copy that fails after making a link must not leave that link aimed at main for the
// next run to keep.
func TestRetryAfterACopyFailedPartWayLeavesNoLinkIntoMain(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	d := filepath.Join(src, "d")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(d, "real.txt")
	if err := os.WriteFile(real, []byte("main's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(d, "a-link")); err != nil {
		t.Skipf("this machine can't make symlinks: %v", err)
	}
	unreadable := filepath.Join(d, "z-unreadable")
	if err := os.WriteFile(unreadable, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(unreadable, 0o644) })
	if _, err := os.ReadFile(unreadable); err == nil {
		t.Skip("a file with no permissions can still be read here, so the copy can't be made to fail")
	}

	if _, err := Merge(src, dst, "d"); err == nil {
		t.Fatal("Merge of a tree holding an unreadable file succeeded, so this proves nothing")
	}
	if err := os.Chmod(unreadable, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Merge(src, dst, "d"); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(dst, "d", "a-link")
	if got, err := os.Readlink(link); err != nil || got != filepath.Join(dst, "d", "real.txt") {
		t.Errorf("after the retry a-link -> %q, %v; want the berth's own file", got, err)
	}
	if err := os.WriteFile(link, []byte("the berth's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(real); string(data) != "main's\n" {
		t.Errorf("a write through the berth's link reached main's file: %q", data)
	}
}

// A process killed between copying a link and repointing it leaves this state, which
// nothing but the next run can repair.
func TestMergeRepointsALinkTheBerthAlreadyHasThatLeadsIntoMain(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	real := filepath.Join(src, "real.txt")
	if err := os.WriteFile(real, []byte("main's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{src, dst} {
		if err := os.Symlink(real, filepath.Join(root, "link")); err != nil {
			t.Skipf("this machine can't make symlinks: %v", err)
		}
	}

	r, err := Merge(src, dst, "link")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(filepath.Join(dst, "link")); err != nil || got != filepath.Join(dst, "real.txt") {
		t.Errorf("link -> %q, %v; want the berth's own file", got, err)
	}
	if len(r.Created) != 0 {
		t.Errorf("Merge reported repointing a link as creating %v", r.Created)
	}
}

// A worktree can sit inside main, and then a path into the berth is also a path under main.
func TestMergeLeavesALinkAlreadyIntoABerthInsideMain(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(src, "worktrees", "feat")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(src, "real.txt"), filepath.Join(src, "link")); err != nil {
		t.Skipf("this machine can't make symlinks: %v", err)
	}
	want := filepath.Join(dst, "real.txt")
	if err := os.Symlink(want, filepath.Join(dst, "link")); err != nil {
		t.Fatal(err)
	}

	if _, err := Merge(src, dst, "link"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(filepath.Join(dst, "link")); err != nil || got != want {
		t.Errorf("link -> %q, %v; want it left at %q", got, err, want)
	}
}

func TestRetargetSeesARelativeLinkReachingMainThroughAnAlias(t *testing.T) {
	root := t.TempDir()
	src, elsewhere := filepath.Join(root, "src"), filepath.Join(root, "elsewhere")
	for _, d := range []string{src, elsewhere} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(src, filepath.Join(elsewhere, "alias")); err != nil {
		t.Skipf("this machine can't make symlinks: %v", err)
	}
	dst := filepath.Join(root, "berth")

	got, changed := Retarget(filepath.Join("..", "elsewhere", "alias", "x"), src, src, dst)

	if want := filepath.Join(dst, "x"); !changed || got != want {
		t.Errorf("Retarget = %q, %v; want %q, true", got, changed, want)
	}
}
