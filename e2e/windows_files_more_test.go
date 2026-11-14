//go:build e2e

package e2e

import (
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A file's last cluster is shorter than a cluster unless the size happens to divide
// evenly, and a clone region must still end on a cluster boundary.
func TestFileEndingPartWayThroughAClusterBlockClonesExactlyOnDevDrive(t *testing.T) {
	repo := devDriveRepo(t)
	appendFile(t, filepath.Join(repo.Dir, ".gitignore"), "vendor/\n")
	repo.Git(t, "commit", "-q", "-am", "ignore vendor")
	rel := filepath.Join("vendor", "odd.bin")
	data := make([]byte, 1234567)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo.Dir, rel), string(data))
	writeFile(t, repo.privateConfig(), "[files]\nclone = [\"vendor\"]\n")
	wt := repo.AddWorktree(t, "feat-odd-size")

	mustBerth(t, repo, wt, "add")

	got, err := os.ReadFile(filepath.Join(wt, rel))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatalf("the clone of a %d-byte file reads back differently, %d bytes", len(data), len(got))
	}
	if !sharesAnExtent(clusterNumbers(t, filepath.Join(repo.Dir, rel)), clusterNumbers(t, filepath.Join(wt, rel))) {
		t.Error("the file shares no cluster with main's; it was copied, not block cloned")
	}
}

func TestEmptyFilesAreClonedNotCountedAsFullCopiesOnDevDrive(t *testing.T) {
	repo := devDriveRepo(t)
	appendFile(t, filepath.Join(repo.Dir, ".gitignore"), "vendor/\n")
	repo.Git(t, "commit", "-q", "-am", "ignore vendor")
	writeFile(t, filepath.Join(repo.Dir, "vendor", "empty.txt"), "")
	writeFile(t, repo.privateConfig(), "[files]\nclone = [\"vendor\"]\n")
	wt := repo.AddWorktree(t, "feat-empty")

	res := mustBerth(t, repo, wt, "add")

	if line := strings.Join(dependencyReport(res, "vendor"), "\n"); !strings.Contains(line, "(cow)") {
		t.Errorf("add does not report an empty file as cloned copy-on-write: %q\nstderr:\n%s", line, res.Stderr)
	}
	if info, err := os.Stat(filepath.Join(wt, "vendor", "empty.txt")); err != nil || info.Size() != 0 {
		t.Errorf("the empty file in the berth: %v, %v", info, err)
	}
}

func refusedSymlinkAdd(t *testing.T) (*Repo, string, Result) {
	t.Helper()
	onlyOnWindows(t)
	repo := NewRepo(t)
	writeFile(t, filepath.Join(repo.Dir, "symlinked", "real.txt"), "real\n")
	symlink(t, filepath.Join(repo.Dir, "symlinked", "real.txt"), filepath.Join(repo.Dir, "symlinked", "shortcut"))
	writeFile(t, repo.privateConfig(), "[files]\nclone = [\"symlinked\"]\n")
	wt := repo.AddWorktree(t, "feat-refused")
	return repo, wt, berthWithoutSymlinkPrivilege(t, repo, wt, "add")
}

func TestPathRefusedASymlinkLeavesNothingBehindAndAddExitsNonZero(t *testing.T) {
	_, wt, res := refusedSymlinkAdd(t)

	if res.Exit == 0 {
		t.Errorf("add reported success with a cloned path it could not make:\nstdout:\n%s\nstderr:\n%s", res.Stdout, res.Stderr)
	}
	if _, err := os.Stat(filepath.Join(wt, "symlinked")); !os.IsNotExist(err) {
		t.Errorf("the refused path left something in the berth: %v", err)
	}
}

func TestAddFinishesARefusedSymlinkPathOnceSymlinksWork(t *testing.T) {
	repo, wt, _ := refusedSymlinkAdd(t)

	mustBerth(t, repo, wt, "add")

	if got, want := readlink(t, filepath.Join(wt, "symlinked", "shortcut")), filepath.Join(wt, "symlinked", "real.txt"); got != want {
		t.Errorf("shortcut -> %q, want %q", got, want)
	}
}

func TestRemoveDeletesAClonedJunctionAndLeavesItsTarget(t *testing.T) {
	onlyOnWindows(t)
	repo := NewRepo(t)
	writeFile(t, filepath.Join(repo.Dir, "linked", "z-real", "file.txt"), "main's\n")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(repo.Dir, "linked", "a-junction"), filepath.Join(repo.Dir, "linked", "z-real")).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v\n%s", err, out)
	}
	writeFile(t, repo.privateConfig(), "[files]\nclone = [\"linked\"]\n")
	wt := repo.AddWorktree(t, "feat-junction-remove")
	mustBerth(t, repo, wt, "add")

	mustBerth(t, repo, wt, "remove")

	if got := readString(t, filepath.Join(repo.Dir, "linked", "z-real", "file.txt")); got != "main's\n" {
		t.Errorf("removing the berth touched main's file through its junction: %q", got)
	}
	if _, err := os.Lstat(filepath.Join(wt, "linked")); !os.IsNotExist(err) {
		t.Errorf("the berth's cloned path, with its junction, is still there: %v", err)
	}
}

// Rewriting a whole file frees its clusters and allocates new ones, so only a write into
// the middle of the file lands on clusters it still shares with main.
func TestWriteInsideABlockClonedFileNeverReachesMain(t *testing.T) {
	repo := devDriveRepo(t)
	setupDependencies(t, repo)
	wt := repo.AddWorktree(t, "feat-devdrive-inplace")
	mustBerth(t, repo, wt, "add")
	rel := dependencyFiles[0]
	before := readString(t, filepath.Join(repo.Dir, rel))
	if !sharesAnExtent(clusterNumbers(t, filepath.Join(repo.Dir, rel)), clusterNumbers(t, filepath.Join(wt, rel))) {
		t.Fatalf("%s shares no cluster with main's, so this write proves nothing", rel)
	}

	f, err := os.OpenFile(filepath.Join(wt, rel), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	const off = 1 << 19
	_, err = f.WriteAt([]byte("changed in the berth"), off)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	if readString(t, filepath.Join(repo.Dir, rel)) != before {
		t.Errorf("writing inside the berth's %s changed main's copy", rel)
	}
	if got := readString(t, filepath.Join(wt, rel)); got[off:off+20] != "changed in the berth" {
		t.Errorf("the berth's %s did not take the write", rel)
	}
}

func TestRemoveRefusesToDeleteThroughAParentThatBecameALinkIntoMain(t *testing.T) {
	repo := NewRepo(t)
	wt, mainFile := berthWithParentLinkedIntoMain(t, repo, "feat-parent-link")

	res := mustBerth(t, repo, wt, "remove")

	if got := readString(t, mainFile); got != "main's\n" {
		t.Errorf("removing the berth deleted main's file through a parent link: %q", got)
	}
	expectLeftBehindParentLink(t, "remove", res)
}

func TestPruneRefusesToDeleteThroughAParentThatBecameALinkIntoMain(t *testing.T) {
	repo := NewRepo(t)
	wt, mainFile := berthWithParentLinkedIntoMain(t, repo, "feat-parent-link")
	id := stateOf(t, repo, wt).ID
	ageBerth(t, repo, wt, id, time.Now().Add(-30*day))

	res := repo.Berth(t, repo.Dir, "prune", "--idle", "7d")

	if res.Exit != 0 {
		t.Fatalf("prune: exit %d\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	if !pruned(t, repo, id) {
		t.Fatalf("prune kept the idle berth:\nstdout:\n%s\nstderr:\n%s", res.Stdout, res.Stderr)
	}
	if got := readString(t, mainFile); got != "main's\n" {
		t.Errorf("pruning the berth deleted main's file through a parent link: %q", got)
	}
	expectLeftBehindParentLink(t, "prune", res)
}

// The berth's record is gone once remove or prune finishes, so the message must not send
// the user back to a command that no longer knows the file.
func expectLeftBehindParentLink(t *testing.T, cmd string, res Result) {
	t.Helper()
	out := res.Stdout + res.Stderr
	if n := strings.Count(out, "leaving a/b"); n != 1 {
		t.Errorf("%s reports leaving a/b %d times, want once:\nstdout:\n%s\nstderr:\n%s", cmd, n, res.Stdout, res.Stderr)
	}
	if strings.Contains(out, "run this again") {
		t.Errorf("%s tells the user to run it again after deleting the berth's record:\n%s", cmd, out)
	}
}

// berthWithParentLinkedIntoMain gives the berth a cloned a/b, then checks out a commit
// where a is a symlink to main's a, so a/b in the berth is main's a/b.
func berthWithParentLinkedIntoMain(t *testing.T, repo *Repo, name string) (wt, mainFile string) {
	t.Helper()
	appendFile(t, filepath.Join(repo.Dir, ".gitignore"), "a/b/\n")
	writeFile(t, filepath.Join(repo.Dir, "a", "keep.txt"), "tracked\n")
	repo.Git(t, "add", ".")
	repo.Git(t, "commit", "-q", "-m", "track a, ignore a/b")
	mainFile = filepath.Join(repo.Dir, "a", "b", "data.txt")
	writeFile(t, mainFile, "main's\n")
	writeFile(t, repo.privateConfig(), "[files]\nclone = [\"a/b\"]\n")
	wt = repo.AddWorktree(t, name)
	mustBerth(t, repo, wt, "add", "--wait")

	maker := repo.AddWorktree(t, "maker")
	if err := os.RemoveAll(filepath.Join(maker, "a")); err != nil {
		t.Fatal(err)
	}
	symlink(t, filepath.Join(repo.Dir, "a"), filepath.Join(maker, "a"))
	repo.mustRun(t, maker, "git", "add", "-A")
	repo.mustRun(t, maker, "git", "commit", "-q", "-m", "make a a link into main")
	repo.mustRun(t, wt, "git", "checkout", "-q", "--detach", "maker")
	return wt, mainFile
}

func TestRefusedComposeOverrideSymlinkStillWritesTheBerthAndNamesEveryRefusedPath(t *testing.T) {
	onlyOnWindows(t)
	repo := NewRepo(t)
	appendFile(t, filepath.Join(repo.Dir, ".gitignore"), "compose.override.yml\n")
	writeFile(t, filepath.Join(repo.Dir, "override.yml"), "services: {}\n")
	repo.Git(t, "add", ".")
	repo.Git(t, "commit", "-q", "-m", "ignore the override")
	symlink(t, filepath.Join(repo.Dir, "override.yml"), filepath.Join(repo.Dir, "compose.override.yml"))
	writeFile(t, filepath.Join(repo.Dir, "symlinked", "real.txt"), "real\n")
	symlink(t, filepath.Join(repo.Dir, "symlinked", "real.txt"), filepath.Join(repo.Dir, "symlinked", "shortcut"))
	writeFile(t, repo.privateConfig(), "[files]\nclone = [\"symlinked\"]\n")
	wt := repo.AddWorktree(t, "feat-refused-override")

	res := berthWithoutSymlinkPrivilege(t, repo, wt, "add")

	if res.Exit == 0 {
		t.Errorf("add reported success with paths it could not make:\nstdout:\n%s\nstderr:\n%s", res.Stdout, res.Stderr)
	}
	out := res.Stdout + res.Stderr
	for _, name := range []string{"Developer Mode", "compose.override.yml", "symlinked"} {
		if !strings.Contains(out, name) {
			t.Errorf("add's output doesn't name %s:\nstdout:\n%s\nstderr:\n%s", name, res.Stdout, res.Stderr)
		}
	}
	if !fileExistsAt(filepath.Join(wt, ".env")) {
		t.Errorf("add stopped before writing the berth's .env:\nstderr:\n%s", res.Stderr)
	}
	if strings.Count(out, "Windows refused") != 1 {
		t.Errorf("add should end with one error for every refused path:\nstdout:\n%s\nstderr:\n%s", res.Stdout, res.Stderr)
	}
}
