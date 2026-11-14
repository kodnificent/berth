//go:build e2e

package e2e

import (
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

var dependencyFiles = []string{
	filepath.Join("vendor", "acme", "lib", "data.bin"),
	filepath.Join("node_modules", "acme", "index.bin"),
}

// setupDependencies gives main gitignored vendor/ and node_modules/ listed under
// [files] clone, each holding a file large enough to live in data extents rather than
// inline in filesystem metadata, where it could not share extents.
func setupDependencies(t *testing.T, repo *Repo) {
	t.Helper()
	appendFile(t, filepath.Join(repo.Dir, ".gitignore"), "vendor/\nnode_modules/\n")
	repo.Git(t, "commit", "-q", "-am", "ignore dependencies")
	for _, rel := range dependencyFiles {
		data := make([]byte, 1<<20)
		if _, err := rand.Read(data); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(repo.Dir, rel), string(data))
	}
	writeFile(t, repo.privateConfig(), "[files]\nclone = [\"vendor\", \"node_modules\"]\n")
}

// reposIn makes t.TempDir, and so NewRepo, create its directories under dir.
func reposIn(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("TMPDIR", dir)
}

func fsType(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("df", "--output=fstype", dir).Output()
	if err != nil {
		t.Fatalf("df --output=fstype %s: %v", dir, err)
	}
	lines := strings.Fields(string(out))
	return lines[len(lines)-1]
}

var extentLine = regexp.MustCompile(`^\s*\d+:\s+\d+\.\.\s*\d+:\s+(\d+)\.\.`)

// physicalExtents lists where a file's data sits on disk, as filefrag reports it.
func physicalExtents(t *testing.T, path string) []string {
	t.Helper()
	if out, err := exec.Command("sync").CombinedOutput(); err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}
	out, err := exec.Command("filefrag", "-v", path).CombinedOutput()
	if err != nil {
		t.Fatalf("filefrag -v %s: %v\n%s", path, err, out)
	}
	var starts []string
	for _, line := range strings.Split(string(out), "\n") {
		if m := extentLine.FindStringSubmatch(line); m != nil {
			starts = append(starts, m[1])
		}
	}
	if len(starts) == 0 {
		t.Fatalf("filefrag reports no extents for %s:\n%s", path, out)
	}
	return starts
}

func sharesAnExtent(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// dependencyReport is every line of add's output that names the cloned directory.
func dependencyReport(res Result, dir string) []string {
	var out []string
	for _, line := range strings.Split(res.Stdout+"\n"+res.Stderr, "\n") {
		if strings.Contains(line, dir) {
			out = append(out, line)
		}
	}
	return out
}

func saysCopyOnWrite(line string) bool {
	l := strings.ToLower(line)
	return strings.Contains(l, "cow") || strings.Contains(l, "copy-on-write")
}

func expectCopyOnWriteReported(t *testing.T, res Result) {
	t.Helper()
	for _, dir := range []string{"vendor", "node_modules"} {
		lines := dependencyReport(res, dir)
		reported := false
		for _, line := range lines {
			if saysCopyOnWrite(line) {
				reported = true
			}
		}
		if !reported {
			t.Errorf("add does not report %s as cloned by copy-on-write; lines naming it: %q\nstdout:\n%s\nstderr:\n%s", dir, lines, res.Stdout, res.Stderr)
		}
	}
}

// The reporting half, on the platform this repo is developed on: APFS has
// copy-on-write too, and add says which path ran for every item.
func TestAddReportsCopyOnWriteForDependenciesOnAPFS(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("APFS is macOS only; Linux filesystems are covered by the btrfs and ext4 tests")
	}
	repo := NewRepo(t)
	setupDependencies(t, repo)
	wt := repo.AddWorktree(t, "feat-apfs")
	expectCopyOnWriteReported(t, mustBerth(t, repo, wt, "add"))
}

func btrfsRepo(t *testing.T) *Repo {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("btrfs reflinks are tested on Linux only")
	}
	dir := os.Getenv("BERTH_TEST_BTRFS_DIR")
	if dir == "" {
		t.Skip("BERTH_TEST_BTRFS_DIR is not set; the CI btrfs job sets it to a directory on a btrfs loop device")
	}
	if got := fsType(t, dir); got != "btrfs" {
		t.Fatalf("BERTH_TEST_BTRFS_DIR %s is on %s, not btrfs", dir, got)
	}
	reposIn(t, dir)
	return NewRepo(t)
}

// On btrfs, the berth's vendor/ and node_modules/ files share extents with
// main's.
func TestDependenciesShareExtentsOnBtrfs(t *testing.T) {
	repo := btrfsRepo(t)
	setupDependencies(t, repo)
	wt := repo.AddWorktree(t, "feat-btrfs")
	mustBerth(t, repo, wt, "add")

	for _, rel := range dependencyFiles {
		if readString(t, filepath.Join(wt, rel)) != readString(t, filepath.Join(repo.Dir, rel)) {
			t.Errorf("%s in the berth differs from main's", rel)
			continue
		}
		if !sharesAnExtent(physicalExtents(t, filepath.Join(repo.Dir, rel)), physicalExtents(t, filepath.Join(wt, rel))) {
			t.Errorf("%s in the berth shares no extent with main's; it was copied, not reflinked", rel)
		}
	}
}

// The negative case: sharing extents must not mean sharing the file, so a
// write in the berth never reaches main.
func TestBtrfsCloneIsNotTheSameFile(t *testing.T) {
	repo := btrfsRepo(t)
	setupDependencies(t, repo)
	wt := repo.AddWorktree(t, "feat-btrfs-write")
	mustBerth(t, repo, wt, "add")

	for _, rel := range dependencyFiles {
		before := readString(t, filepath.Join(repo.Dir, rel))
		writeFile(t, filepath.Join(wt, rel), "changed in the berth\n")
		if readString(t, filepath.Join(repo.Dir, rel)) != before {
			t.Errorf("writing %s in the berth changed main's copy", rel)
		}
	}
}

// On btrfs, add reports the copy-on-write path for the dependency clones.
func TestAddReportsCopyOnWriteOnBtrfs(t *testing.T) {
	repo := btrfsRepo(t)
	setupDependencies(t, repo)
	wt := repo.AddWorktree(t, "feat-btrfs-report")
	expectCopyOnWriteReported(t, mustBerth(t, repo, wt, "add"))
}

func ext4Repo(t *testing.T) *Repo {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("ext4 full copies are tested on Linux only")
	}
	if got := fsType(t, os.TempDir()); got != "ext4" {
		t.Skipf("the temp directory %s is on %s, not ext4", os.TempDir(), got)
	}
	return NewRepo(t)
}

// On ext4, the berth's dependency files are full copies holding their own
// data, sharing no extent with main's.
func TestDependenciesAreFullCopiesOnExt4(t *testing.T) {
	repo := ext4Repo(t)
	setupDependencies(t, repo)
	wt := repo.AddWorktree(t, "feat-ext4")
	mustBerth(t, repo, wt, "add")

	for _, rel := range dependencyFiles {
		if readString(t, filepath.Join(wt, rel)) != readString(t, filepath.Join(repo.Dir, rel)) {
			t.Errorf("%s in the berth differs from main's", rel)
			continue
		}
		if sharesAnExtent(physicalExtents(t, filepath.Join(repo.Dir, rel)), physicalExtents(t, filepath.Join(wt, rel))) {
			t.Errorf("%s in the berth shares data with main's on ext4, which has no copy-on-write; it must be a full copy", rel)
		}
	}
}

// On ext4, add reports the dependency clones as full copies, never as
// copy-on-write.
func TestAddReportsFullCopyOnExt4(t *testing.T) {
	repo := ext4Repo(t)
	setupDependencies(t, repo)
	wt := repo.AddWorktree(t, "feat-ext4-report")
	res := mustBerth(t, repo, wt, "add")

	for _, dir := range []string{"vendor", "node_modules"} {
		lines := dependencyReport(res, dir)
		copied := false
		for _, line := range lines {
			if saysCopyOnWrite(line) {
				t.Errorf("add reports %s as copy-on-write on ext4: %q", dir, line)
			}
			if strings.Contains(strings.ToLower(line), "copy") {
				copied = true
			}
		}
		if !copied {
			t.Errorf("add does not report %s as a copy; lines naming it: %q\nstdout:\n%s\nstderr:\n%s", dir, lines, res.Stdout, res.Stderr)
		}
	}
}

// On ext4, doctor says dependency clones will be full copies.
func TestDoctorSaysFullCopiesOnExt4(t *testing.T) {
	repo := ext4Repo(t)
	setupDependencies(t, repo)
	wt := repo.AddWorktree(t, "feat-ext4-doctor")
	mustBerth(t, repo, wt, "add")

	d := doctor(t, repo, nil, wt)
	for _, f := range d.Findings {
		if strings.Contains(strings.ToLower(f.Message), "full cop") {
			return
		}
	}
	t.Errorf("doctor on ext4 has no finding saying clones will be full copies:\n%s", d.Raw)
}
