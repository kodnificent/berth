//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func devDriveRepo(t *testing.T) *Repo {
	t.Helper()
	onlyOnWindows(t)
	dir := os.Getenv("BERTH_TEST_DEV_DRIVE")
	if dir == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("BERTH_TEST_DEV_DRIVE is not set; the Windows CI job must point it at a directory on a Dev Drive")
		}
		t.Skip("BERTH_TEST_DEV_DRIVE is not set")
	}
	if got := volumeFilesystem(t, dir); got != "ReFS" {
		t.Fatalf("BERTH_TEST_DEV_DRIVE %s is on %s, not ReFS", dir, got)
	}
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	return NewRepo(t)
}

func ntfsRepo(t *testing.T) *Repo {
	t.Helper()
	onlyOnWindows(t)
	if got := volumeFilesystem(t, os.TempDir()); got != "NTFS" {
		if os.Getenv("CI") != "" {
			t.Fatalf("the temp directory %s is on %s; the Windows CI job must keep it on NTFS", os.TempDir(), got)
		}
		t.Skipf("the temp directory %s is on %s, not NTFS", os.TempDir(), got)
	}
	return NewRepo(t)
}

func TestDependenciesShareClustersWithMainOnDevDrive(t *testing.T) {
	repo := devDriveRepo(t)
	setupDependencies(t, repo)
	wt := repo.AddWorktree(t, "feat-devdrive")
	mustBerth(t, repo, wt, "add")

	for _, rel := range dependencyFiles {
		if readString(t, filepath.Join(wt, rel)) != readString(t, filepath.Join(repo.Dir, rel)) {
			t.Errorf("%s in the berth differs from main's", rel)
			continue
		}
		if !sharesAnExtent(clusterNumbers(t, filepath.Join(repo.Dir, rel)), clusterNumbers(t, filepath.Join(wt, rel))) {
			t.Errorf("%s in the berth shares no cluster with main's; it was copied, not block cloned", rel)
		}
	}
}

func TestAddReportsCopyOnWriteOnDevDrive(t *testing.T) {
	repo := devDriveRepo(t)
	setupDependencies(t, repo)
	wt := repo.AddWorktree(t, "feat-devdrive-report")
	expectCopyOnWriteReported(t, mustBerth(t, repo, wt, "add"))
}

func TestWriteInADevDriveCloneNeverReachesMain(t *testing.T) {
	repo := devDriveRepo(t)
	setupDependencies(t, repo)
	wt := repo.AddWorktree(t, "feat-devdrive-write")
	mustBerth(t, repo, wt, "add")

	for _, rel := range dependencyFiles {
		before := readString(t, filepath.Join(repo.Dir, rel))
		writeFile(t, filepath.Join(wt, rel), "changed in the berth\n")
		if readString(t, filepath.Join(repo.Dir, rel)) != before {
			t.Errorf("writing %s in the berth changed main's copy", rel)
		}
	}
}

func TestSQLiteCopySharesClustersWithMainOnDevDrive(t *testing.T) {
	repo := devDriveRepo(t)
	ignoreSQLite(t, repo)
	rel := "database/app.sqlite"
	db := filepath.Join(repo.Dir, rel)
	makeLedger(t, db, "delete", 8000, 2000)
	writeFile(t, repo.privateConfig(), sqliteConfig(rel))
	wt := repo.AddWorktree(t, "feat-devdrive-sqlite")

	mustBerth(t, repo, wt, "add", "--wait")

	berthDB := filepath.Join(wt, rel)
	if !fileExistsAt(berthDB) {
		t.Fatalf("add did not clone %s into the berth", rel)
	}
	if !sharesAnExtent(clusterNumbers(t, db), clusterNumbers(t, berthDB)) {
		t.Errorf("the berth's %s shares no cluster with main's; it was copied, not block cloned", rel)
	}
}

// A hard link would share clusters too, and a write in the berth would then reach main.
func TestDependenciesAreFullCopiesOnNTFS(t *testing.T) {
	repo := ntfsRepo(t)
	setupDependencies(t, repo)
	wt := repo.AddWorktree(t, "feat-ntfs")
	mustBerth(t, repo, wt, "add")

	for _, rel := range dependencyFiles {
		if readString(t, filepath.Join(wt, rel)) != readString(t, filepath.Join(repo.Dir, rel)) {
			t.Errorf("%s in the berth differs from main's", rel)
			continue
		}
		if sharesAnExtent(clusterNumbers(t, filepath.Join(repo.Dir, rel)), clusterNumbers(t, filepath.Join(wt, rel))) {
			t.Errorf("%s in the berth shares clusters with main's on NTFS; it must be a full copy", rel)
		}
	}
}

func TestAddReportsFullCopyNamingDevDriveOnNTFS(t *testing.T) {
	repo := ntfsRepo(t)
	setupDependencies(t, repo)
	wt := repo.AddWorktree(t, "feat-ntfs-report")
	res := mustBerth(t, repo, wt, "add")

	for _, dir := range []string{"vendor", "node_modules"} {
		lines := dependencyReport(res, dir)
		copied := false
		for _, line := range lines {
			l := strings.ToLower(line)
			// A hint may say what a Dev Drive would do; only a claim about this clone counts.
			if saysCopyOnWrite(line) && !strings.Contains(l, "dev drive") {
				t.Errorf("add reports %s as copy-on-write on NTFS: %q", dir, line)
			}
			if strings.Contains(l, "copy") {
				copied = true
			}
		}
		if !copied {
			t.Errorf("add does not report %s as a copy; lines naming it: %q\nstdout:\n%s\nstderr:\n%s", dir, lines, res.Stdout, res.Stderr)
		}
	}
	if !strings.Contains(strings.ToLower(res.Stdout+res.Stderr), "dev drive") {
		t.Errorf("add on NTFS does not name Dev Drive:\nstdout:\n%s\nstderr:\n%s", res.Stdout, res.Stderr)
	}
}

func TestDoctorSaysFullCopiesNamingDevDriveOnNTFS(t *testing.T) {
	repo := ntfsRepo(t)
	setupDependencies(t, repo)
	wt := repo.AddWorktree(t, "feat-ntfs-doctor")
	mustBerth(t, repo, wt, "add")

	d := doctor(t, repo, nil, wt)
	for _, f := range d.Findings {
		m := strings.ToLower(f.Message)
		if strings.Contains(m, "full cop") && strings.Contains(m, "dev drive") {
			return
		}
	}
	t.Errorf("doctor on NTFS has no finding saying clones will be full copies and naming Dev Drive:\n%s", d.Raw)
}

// The links sort before their targets, so a clone working through the directory in order
// meets each link before the berth has what it points at, and Windows fixes a symlink as
// a file or a directory when it is made.
func TestSymlinksIntoMainPointIntoTheBerthAndCanBeFollowedOnWindows(t *testing.T) {
	onlyOnWindows(t)
	repo := NewRepo(t)
	writeFile(t, filepath.Join(repo.Dir, "linked", "z-real", "file.txt"), "main's\n")
	links := map[string]string{
		filepath.Join("linked", "a-dir"):  filepath.Join("linked", "z-real"),
		filepath.Join("linked", "a-file"): filepath.Join("linked", "z-real", "file.txt"),
	}
	for link, target := range links {
		symlink(t, filepath.Join(repo.Dir, target), filepath.Join(repo.Dir, link))
	}
	writeFile(t, repo.privateConfig(), "[files]\nclone = [\"linked\"]\n")
	wt := repo.AddWorktree(t, "feat-win-links")

	mustBerth(t, repo, wt, "add")

	for link, target := range links {
		if got, want := readlink(t, filepath.Join(wt, link)), filepath.Join(wt, target); got != want {
			t.Errorf("%s -> %q in the berth, want %q", link, got, want)
		}
		if got, want := readlink(t, filepath.Join(repo.Dir, link)), filepath.Join(repo.Dir, target); got != want {
			t.Errorf("cloning changed main's %s -> %q, want it left at %q", link, got, want)
		}
	}
	writeFile(t, filepath.Join(wt, "linked", "z-real", "file.txt"), "the berth's\n")
	for _, through := range []string{filepath.Join("linked", "a-dir", "file.txt"), filepath.Join("linked", "a-file")} {
		if got := readString(t, filepath.Join(wt, through)); got != "the berth's\n" {
			t.Errorf("reading %s in the berth gave %q, want the berth's own file", through, got)
		}
	}
	if got := readString(t, filepath.Join(repo.Dir, "linked", "z-real", "file.txt")); got != "main's\n" {
		t.Errorf("writing in the berth reached main's file: %q", got)
	}
}

func TestJunctionIntoMainIsClonedAsAJunctionPointingIntoTheBerth(t *testing.T) {
	onlyOnWindows(t)
	repo := NewRepo(t)
	writeFile(t, filepath.Join(repo.Dir, "linked", "z-real", "file.txt"), "main's\n")
	link := filepath.Join("linked", "a-junction")
	mainTarget := filepath.Join(repo.Dir, "linked", "z-real")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(repo.Dir, link), mainTarget).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v\n%s", err, out)
	}
	writeFile(t, repo.privateConfig(), "[files]\nclone = [\"linked\"]\n")
	wt := repo.AddWorktree(t, "feat-win-junction")

	mustBerth(t, repo, wt, "add")

	if got, want := readlink(t, filepath.Join(wt, link)), filepath.Join(wt, "linked", "z-real"); got != want {
		t.Errorf("%s -> %q in the berth, want %q", link, got, want)
	}
	// 0xa0000003 is IO_REPARSE_TAG_MOUNT_POINT, the tag that makes it a junction and not a symlink.
	if out, err := exec.Command("fsutil", "reparsepoint", "query", filepath.Join(wt, link)).CombinedOutput(); err != nil || !strings.Contains(strings.ToLower(string(out)), "0xa0000003") {
		t.Errorf("%s in the berth is not a junction: %v\n%s", link, err, out)
	}
	if got := readlink(t, filepath.Join(repo.Dir, link)); got != mainTarget {
		t.Errorf("cloning changed main's junction to %q, want it left at %q", got, mainTarget)
	}
	writeFile(t, filepath.Join(wt, "linked", "z-real", "file.txt"), "the berth's\n")
	if got := readString(t, filepath.Join(wt, link, "file.txt")); got != "the berth's\n" {
		t.Errorf("reading through the berth's junction gave %q, want the berth's own file", got)
	}
	if got := readString(t, filepath.Join(repo.Dir, "linked", "z-real", "file.txt")); got != "main's\n" {
		t.Errorf("writing in the berth reached main's file: %q", got)
	}
}

// The failing path is listed first, so the other can only be there if add carried on.
func TestClonedPathHoldingASymlinkFailsNamingDeveloperModeAndTheOthersFinish(t *testing.T) {
	onlyOnWindows(t)
	repo := NewRepo(t)
	writeFile(t, filepath.Join(repo.Dir, "symlinked", "real.txt"), "real\n")
	symlink(t, filepath.Join(repo.Dir, "symlinked", "real.txt"), filepath.Join(repo.Dir, "symlinked", "shortcut"))
	writeFile(t, filepath.Join(repo.Dir, "plain", "file.txt"), "plain\n")
	writeFile(t, repo.privateConfig(), "[files]\nclone = [\"symlinked\", \"plain\"]\n")
	wt := repo.AddWorktree(t, "feat-no-devmode")

	res := berthWithoutSymlinkPrivilege(t, repo, wt, "add")

	out := strings.ToLower(res.Stdout + res.Stderr)
	if !strings.Contains(out, "developer mode") || !strings.Contains(out, "symlinked") {
		t.Errorf("add without Developer Mode (exit %d) doesn't name Developer Mode and the path:\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	if got, err := os.ReadFile(filepath.Join(wt, "plain", "file.txt")); err != nil || string(got) != "plain\n" {
		t.Errorf("the other cloned path did not finish: %q, %v\nstdout:\n%s\nstderr:\n%s", got, err, res.Stdout, res.Stderr)
	}
	if _, err := os.Lstat(filepath.Join(wt, "symlinked", "shortcut")); !os.IsNotExist(err) {
		t.Errorf("the path that failed still has its link in the berth, so it did not fail (err %v)", err)
	}
}

// SQLite locks the bytes at 1 GiB, and Windows locks are mandatory, so only a file past
// that offset can have a copy refused by main's writer. Smaller files are covered by
// TestSQLiteCopyUnderWriteLoadIsNeverTorn.
func TestSQLiteFileOverOneGibibyteClonedUnderWriteLoadPassesIntegrityCheck(t *testing.T) {
	onlyOnWindows(t)
	sqliteBin(t)
	repo := NewRepo(t)
	ignoreSQLite(t, repo)
	rel := "database/app.sqlite"
	db := filepath.Join(repo.Dir, rel)
	const rows = 1100
	makeLedger(t, db, "delete", rows, 1<<20)
	if info, err := os.Stat(db); err != nil || info.Size() <= 1<<30 {
		t.Fatalf("main's database is not over 1 GiB (%v), so its copy never reads the locked bytes and this proves nothing", err)
	}
	writeFile(t, repo.privateConfig(), sqliteConfig(rel))

	writeLoad(t, db)
	waitFor(t, 2*time.Minute, "main's write load to commit rows", func() bool { return ledgerRows(t, db) > rows })
	committed := ledgerRows(t, db)
	wt := repo.AddWorktree(t, "feat-win-big-sqlite")

	mustBerth(t, repo, wt, "add", "--wait")

	berthDB := filepath.Join(wt, rel)
	if !fileExistsAt(berthDB) {
		t.Fatalf("add --wait left no %s in the berth", rel)
	}
	checkConsistent(t, fmt.Sprintf("the berth's copy of %s", rel), berthDB)
	if got := ledgerRows(t, berthDB); got < committed {
		t.Errorf("the berth's copy holds %d rows, fewer than the %d main had committed before add began", got, committed)
	}
}

func TestDoctorReportsMainsPathWrittenWithBackslashes(t *testing.T) {
	onlyOnWindows(t)
	repo := NewRepo(t)
	writeFile(t, filepath.Join(repo.Dir, "leaks", "settings.ini"), "[paths]\nroot="+repo.Dir+`\storage`+"\n")
	writeFile(t, filepath.Join(repo.Dir, "leaks", "cache.json"), `{"root": "`+strings.ReplaceAll(repo.Dir, `\`, `\\`)+`\\storage"}`+"\n")
	writeFile(t, filepath.Join(repo.Dir, "leaks", "sibling.txt"), repo.Dir+`-other\storage`+"\n")
	writeFile(t, repo.privateConfig(), "[files]\nclone = [\"leaks\"]\n")
	wt := repo.AddWorktree(t, "feat-win-leak")
	mustBerth(t, repo, wt, "add")

	d := doctor(t, repo, nil, wt)

	expectFinding(t, d, "source_path", "leaks/settings.ini:2")
	expectFinding(t, d, "source_path", "leaks/cache.json:1")
	for _, f := range d.with("source_path") {
		if strings.Contains(f.Location, "sibling.txt") {
			t.Errorf("doctor reports a file holding a different directory whose name starts with main's: %+v", f)
		}
	}
}

// Windows paths ignore case, and editors and build tools often lowercase the drive letter.
func TestDoctorReportsMainsBackslashPathWithALowercaseDriveLetter(t *testing.T) {
	onlyOnWindows(t)
	repo := NewRepo(t)
	lowered := strings.ToLower(repo.Dir[:1]) + repo.Dir[1:]
	if lowered == repo.Dir {
		t.Fatalf("main's path %s already starts lowercase, so this proves nothing", repo.Dir)
	}
	writeFile(t, filepath.Join(repo.Dir, "leaks", "settings.ini"), "root="+lowered+`\storage`+"\n")
	writeFile(t, repo.privateConfig(), "[files]\nclone = [\"leaks\"]\n")
	wt := repo.AddWorktree(t, "feat-win-leak-case")
	mustBerth(t, repo, wt, "add")

	expectFinding(t, doctor(t, repo, nil, wt), "source_path", "leaks/settings.ini:1")
}
