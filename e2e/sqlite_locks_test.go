//go:build e2e

package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// addHolding starts berth add with main's database kept locked for hold after the copy,
// and returns once the copy is in the berth, so the lock is held, and a func that waits
// for add to finish.
func addHolding(t *testing.T, repo *Repo, wt, rel string, hold time.Duration) (wait func()) {
	t.Helper()
	cmd := exec.Command(berthBin, "add")
	cmd.Dir = wt
	cmd.Env = append(repo.Env(), "BERTH_TEST_SQLITE_HOLD="+hold.String())
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() { <-done })
	waitFor(t, 30*time.Second, "add to copy "+rel, func() bool { return fileExistsAt(filepath.Join(wt, rel)) })
	return func() {
		<-done
		if cmd.ProcessState.ExitCode() != 0 {
			t.Fatalf("berth add exited %d:\n%s", cmd.ProcessState.ExitCode(), out.String())
		}
	}
}

func trySQLite(t *testing.T, path, sql string) (string, error) {
	t.Helper()
	cmd := exec.Command(sqliteBin(t), "-batch", "-bail", path)
	cmd.Stdin = strings.NewReader(sql)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// leaveWAL commits rows into main's WAL database and exits without checkpointing, as an
// app stopped mid-session leaves it: the WAL holds commits the database file lacks.
func leaveWAL(t *testing.T, db string) {
	t.Helper()
	sqlite(t, db, ".dbconfig no_ckpt_on_close on\nPRAGMA wal_autocheckpoint=0;\nINSERT INTO ledger (amount, pad) SELECT 5, randomblob(1000) FROM ledger LIMIT 20;\n")
	if info, err := os.Stat(db + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("main's WAL holds no commits, so this proves nothing: %v", err)
	}
}

// While berth copies a rollback-journal database, main's writers can't commit, so the
// file can't change under the copy.
func TestSQLiteCopyHoldsMainsWritersOff(t *testing.T) {
	repo := NewRepo(t)
	ignoreSQLite(t, repo)
	rel := "database/app.sqlite"
	db := filepath.Join(repo.Dir, rel)
	makeLedger(t, db, "delete", 50, 100)
	writeFile(t, repo.privateConfig(), sqliteConfig(rel))
	wt := repo.AddWorktree(t, "feat-hold")

	wait := addHolding(t, repo, wt, rel, 3*time.Second)
	out, err := trySQLite(t, db, "INSERT INTO ledger (amount) VALUES (1);")
	wait()

	if err == nil || !strings.Contains(out, "locked") {
		t.Errorf("main committed a write while berth was copying its database (err %v):\n%s", err, out)
	}
}

// While berth copies a WAL database, no checkpoint can write main's WAL into its
// database file, so the file can't change under the copy.
func TestSQLiteCopyHoldsMainsCheckpointsOff(t *testing.T) {
	repo := NewRepo(t)
	ignoreSQLite(t, repo)
	rel := "database/app.sqlite"
	db := filepath.Join(repo.Dir, rel)
	makeLedger(t, db, "wal", 50, 100)
	leaveWAL(t, db)
	writeFile(t, repo.privateConfig(), sqliteConfig(rel))
	wt := repo.AddWorktree(t, "feat-ckpt")
	before := fileSum(t, db)

	wait := addHolding(t, repo, wt, rel, 3*time.Second)
	out, err := trySQLite(t, db, "PRAGMA wal_checkpoint(TRUNCATE);")
	after := fileSum(t, db)
	wait()

	if err != nil || !strings.HasPrefix(out, "1|") {
		t.Errorf("a checkpoint of main's ran while berth was copying it, want busy (err %v): %s", err, out)
	}
	if after != before {
		t.Errorf("main's database file changed while berth was copying it")
	}
}

// Berth only reads main's database: with nothing else connected to it, add and reset
// leave the database, its WAL and its shared-memory index byte for byte as they were,
// with no checkpoint and nothing deleted.
func TestSQLiteCopyWritesNothingOfMains(t *testing.T) {
	repo := NewRepo(t)
	ignoreSQLite(t, repo)
	rel := "database/app.sqlite"
	db := filepath.Join(repo.Dir, rel)
	makeLedger(t, db, "wal", 50, 100)
	leaveWAL(t, db)
	writeFile(t, repo.privateConfig(), sqliteConfig(rel))
	files := []string{db, db + "-wal", db + "-shm"}
	sums := map[string]string{}
	for _, f := range files {
		sums[f] = fileSum(t, f)
	}

	wt := repo.AddWorktree(t, "feat-ro")
	mustBerth(t, repo, wt, "add", "--wait")
	mustBerth(t, repo, wt, "reset")

	for _, f := range files {
		if !fileExistsAt(f) {
			t.Errorf("berth deleted main's %s", filepath.Base(f))
		} else if fileSum(t, f) != sums[f] {
			t.Errorf("berth changed main's %s", filepath.Base(f))
		}
	}
	if got := ledgerRows(t, filepath.Join(wt, rel)); got != ledgerRows(t, db) {
		t.Errorf("the berth's copy holds %d rows, not main's %d, so main's WAL was left out", got, ledgerRows(t, db))
	}
}

// A branch that tracks a symlink where berth made a directory for the copy turns the
// copy's path into a path in main. Checking it out replaces the directory silently,
// since berth's copy in it is ignored, and berth must then delete nothing through it.
func linkCopyDirIntoMain(t *testing.T) (repo *Repo, wt, db string, sums map[string]string) {
	t.Helper()
	repo = NewRepo(t)
	ignoreSQLite(t, repo)
	rel := "database/app.sqlite"
	db = filepath.Join(repo.Dir, rel)
	makeLedger(t, db, "wal", 50, 100)
	leaveWAL(t, db)
	writeFile(t, repo.privateConfig(), sqliteConfig(rel))
	wt = repo.AddWorktree(t, "feat-link")
	mustBerth(t, repo, wt, "add", "--wait")

	linker := repo.AddWorktree(t, "linker")
	if err := os.Symlink(filepath.Join(repo.Dir, "database"), filepath.Join(linker, "database")); err != nil {
		t.Fatal(err)
	}
	repo.mustRun(t, linker, "git", "add", "database")
	repo.mustRun(t, linker, "git", "commit", "-q", "-m", "link database")
	repo.mustRun(t, wt, "git", "checkout", "-q", "--detach", "linker")
	if info, err := os.Lstat(filepath.Join(wt, "database")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("checking out the branch did not put the symlink in place of berth's directory (err %v)", err)
	}

	sums = map[string]string{}
	for _, f := range []string{db, db + "-wal", db + "-shm"} {
		sums[f] = fileSum(t, f)
	}
	return repo, wt, db, sums
}

func checkMainUntouched(t *testing.T, sums map[string]string) {
	t.Helper()
	for f, sum := range sums {
		if !fileExistsAt(f) {
			t.Errorf("berth deleted main's %s through the symlink", filepath.Base(f))
		} else if fileSum(t, f) != sum {
			t.Errorf("berth changed main's %s through the symlink", filepath.Base(f))
		}
	}
}

func TestSQLiteRemoveDeletesNothingThroughASymlinkIntoMain(t *testing.T) {
	repo, wt, _, sums := linkCopyDirIntoMain(t)

	res := repo.Berth(t, wt, "remove")

	checkMainUntouched(t, sums)
	if res.Exit == 0 {
		t.Errorf("remove exited 0 though it left the berth's copy behind:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stderr+res.Stdout, "symbolic link") {
		t.Errorf("remove does not say why it left the copy:\nstdout:\n%s\nstderr:\n%s", res.Stdout, res.Stderr)
	}
}

func TestSQLiteResetDeletesNothingThroughASymlinkIntoMain(t *testing.T) {
	repo, wt, _, sums := linkCopyDirIntoMain(t)

	res := repo.Berth(t, wt, "reset")

	checkMainUntouched(t, sums)
	if strings.Contains(res.Stderr, "main has no") {
		t.Errorf("reset reports main's database missing after refusing to touch it:\n%s", res.Stderr)
	}
}

// A byte copy reads main's WAL while main may truncate it or start it again from the
// top; a copy holding only the start of the old WAL over a database file that already
// has later pages is a state main never had.
func TestSQLiteByteCopyIsNeverTornByAWALTruncate(t *testing.T) {
	repo := NewRepo(t)
	ignoreSQLite(t, repo)
	rel := "database/app.sqlite"
	db := filepath.Join(repo.Dir, rel)
	makeLedger(t, db, "wal", 10, 10)
	// main's app stays connected, so the wal-index keeps what has been checkpointed
	// rather than being rebuilt by the next connection.
	app := exec.Command(sqliteBin(t), "-batch", db)
	appIn, err := app.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { appIn.Close(); _ = app.Wait() })
	if _, err := appIn.Write([]byte("SELECT count(*) FROM ledger;\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "main's app to open the WAL", func() bool { return fileExistsAt(db + "-shm") })
	var commits strings.Builder
	commits.WriteString(".dbconfig no_ckpt_on_close on\nPRAGMA wal_autocheckpoint=0;\n")
	for range 600 {
		commits.WriteString("INSERT INTO ledger (amount, pad) VALUES (abs(random() % 1000) + 1, randomblob(3000));\n")
	}
	commits.WriteString("PRAGMA wal_checkpoint(PASSIVE);\n")
	sqlite(t, db, commits.String())
	if info, err := os.Stat(db + "-wal"); err != nil || info.Size() < 4<<20 {
		t.Fatalf("main's WAL is not several chunks long, so this proves nothing: %v", err)
	}
	writeFile(t, repo.privateConfig(), sqliteConfig(rel))
	wt := repo.AddWorktree(t, "feat-torn")

	cmd := exec.Command(berthBin, "add")
	cmd.Dir = wt
	cmd.Env = append(repo.Env(), "BERTH_TEST_SQLITE_NO_COW=1", "BERTH_TEST_SQLITE_PAUSE=2s")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "add to start copying main's WAL", func() bool { return fileExistsAt(filepath.Join(wt, rel) + "-wal") })
	ckpt, _ := trySQLite(t, db, "PRAGMA wal_checkpoint(TRUNCATE);")
	if err := cmd.Wait(); err != nil {
		t.Fatalf("berth add: %v\n%s", err, out.String())
	}

	berthDB := filepath.Join(wt, rel)
	checkConsistent(t, "the berth's byte copy", berthDB)
	if dump(t, berthDB) != dump(t, db) {
		t.Errorf("the berth's byte copy differs from main's, though main only checkpointed (%s)", ckpt)
	}
}

// SQLite keeps a database's WAL beside the file a symlink resolves to, so a listed
// database that is a symlink in main still has its WAL there.
func TestSQLiteSymlinkedDatabaseIsCopiedWithItsWAL(t *testing.T) {
	repo := NewRepo(t)
	ignoreSQLite(t, repo)
	rel := "database/app.sqlite"
	real := filepath.Join(repo.Dir, "store", "app.sqlite")
	makeLedger(t, real, "wal", 50, 100)
	if err := os.MkdirAll(filepath.Join(repo.Dir, "database"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "store", "app.sqlite"), filepath.Join(repo.Dir, rel)); err != nil {
		t.Fatal(err)
	}
	leaveWAL(t, real)
	writeFile(t, repo.privateConfig(), sqliteConfig(rel))
	wt := repo.AddWorktree(t, "feat-symlinked")

	mustBerth(t, repo, wt, "add", "--wait")

	// real, not db: SQLite on Windows looks for the WAL beside the path it was given and
	// doesn't follow the symlink to find it, so reading through db there would miss it.
	if got, want := ledgerRows(t, filepath.Join(wt, rel)), ledgerRows(t, real); got != want {
		t.Errorf("the berth's copy holds %d rows, not main's %d, so main's WAL was left out", got, want)
	}
}

// Without copy-on-write, a rollback-journal database is locked for as long as its bytes
// take to copy, and main's app sees errors rather than a brief wait; add says so first.
func TestSQLiteByteCopyAnnouncesMainsLockedDatabase(t *testing.T) {
	repo := NewRepo(t)
	ignoreSQLite(t, repo)
	rel := "database/app.sqlite"
	makeLedger(t, filepath.Join(repo.Dir, rel), "delete", 50, 100)
	writeFile(t, repo.privateConfig(), sqliteConfig(rel))
	wt := repo.AddWorktree(t, "feat-announce")

	cmd := exec.Command(berthBin, "add")
	cmd.Dir = wt
	cmd.Env = append(repo.Env(), "BERTH_TEST_SQLITE_NO_COW=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("berth add: %v\n%s", err, out)
	}

	if !strings.Contains(string(out), rel) || !strings.Contains(string(out), "database is locked") {
		t.Errorf("add does not say that main's writes to %s, and reads behind them, fail with database is locked during the copy:\n%s", rel, out)
	}
}
