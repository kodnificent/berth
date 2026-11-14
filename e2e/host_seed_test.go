//go:build e2e

package e2e

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const tenantSeed = `-- a seed file: comments; blank lines and semicolons in text must all survive

/* block ; comment */
CREATE TABLE tenants (id INT PRIMARY KEY, name VARCHAR(100) NOT NULL, note TEXT NULL);
INSERT INTO tenants VALUES (1, 'semi;colon', 'line one\nline two'), (2, 'it''s', NULL);
INSERT INTO tenants VALUES (3, 'back\\slash', '-- not a comment');
CREATE TABLE plans (id INT PRIMARY KEY AUTO_INCREMENT, tenant_id INT NOT NULL, FOREIGN KEY (tenant_id) REFERENCES tenants (id)); INSERT INTO plans (tenant_id) VALUES (1), (3);
`

func seedConfig(entries ...string) string {
	return unpublishedDB + "[state.mysql]\nfrom = \"env:DB_*\"\nprefix = \"env:DB_DATABASE_PREFIX\"\ndefault = \"skip\"\n\n[state.mysql.databases]\n" + strings.Join(entries, "\n") + "\n"
}

func seedEntry(key, path string) string {
	return fmt.Sprintf("%q = { from = \"file:%s\" }", key, path)
}

func gzipped(t *testing.T, content string) string {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// load feeds sql to a new database through the mysql client, giving what a file should
// load to, to compare with what berth loaded.
func (m *hostMySQL) load(t *testing.T, db, sql string) {
	t.Helper()
	m.create(t, db)
	cmd := exec.Command("docker", "exec", "-i", "-e", "MYSQL_PWD="+hostMySQLRootPassword, m.Container,
		"mysql", "-h127.0.0.1", "-uroot", db)
	cmd.Stdin = strings.NewReader(sql)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("loading the reference database %s: %v\n%s", db, err, out)
	}
}

func (m *hostMySQL) dump(t *testing.T, db string) string {
	t.Helper()
	out, err := exec.Command("docker", "exec", "-e", "MYSQL_PWD="+hostMySQLRootPassword, m.Container,
		"mysqldump", "-h127.0.0.1", "-uroot", "--no-tablespaces", db).Output()
	if err != nil {
		t.Fatalf("mysqldump %s: %v", db, err)
	}
	return string(out)
}

// After add --wait the database holds the file's tables and rows, none of
// main's.
func TestSeedFileHoldsExactlyItsDataAfterAddWait(t *testing.T) {
	repo, m := dumpServer(t)
	m.load(t, "ref_seed", tenantSeed)
	want := m.contents(t, "ref_seed")
	writeFile(t, filepath.Join(repo.Dir, "database/seeds/tenant.sql"), tenantSeed)
	writeFile(t, repo.privateConfig(), seedConfig(seedEntry("1", "database/seeds/tenant.sql")))
	wt := repo.AddWorktree(t, "feat-seed")

	mustBerth(t, repo, wt, "add", "--wait")

	p := berthPrefix(t, wt)
	if e := dbState(t, repo, nil, wt, "1"); e.Status != "ready" || e.Target != p+"1" {
		t.Errorf("database 1 after add --wait: status %q target %q, want ready in %s1: %+v", e.Status, e.Target, p, e)
	}
	if got := m.contents(t, p+"1"); got != want {
		t.Errorf("the berth's %s1 doesn't hold exactly the seed file's data:\nwant:\n%s\ngot:\n%s", p, want, got)
	}
}

// Real mysqldump output, with its client DELIMITER commands around a
// trigger, is what most seed files are.
func TestSeedFileFromMysqldumpLoadsWithItsTrigger(t *testing.T) {
	repo, m := dumpServer(t)
	m.sql(t, "CREATE TRIGGER app_central.kept_bi BEFORE INSERT ON app_central.kept FOR EACH ROW SET NEW.v = NEW.v + 1")
	want := m.contents(t, "app_central")
	writeFile(t, filepath.Join(repo.Dir, "database/seeds/dump.sql"), m.dump(t, "app_central"))
	writeFile(t, repo.privateConfig(), seedConfig(seedEntry("1", "database/seeds/dump.sql")))
	wt := repo.AddWorktree(t, "feat-mysqldump")

	mustBerth(t, repo, wt, "add", "--wait")

	p := berthPrefix(t, wt)
	if e := dbState(t, repo, nil, wt, "1"); e.Status != "ready" {
		t.Fatalf("database 1 from a mysqldump file is %q, want ready: %+v", e.Status, e)
	}
	if got := m.contents(t, p+"1"); got != want {
		t.Errorf("the berth's %s1 differs from the dumped database:\nwant:\n%s\ngot:\n%s", p, want, got)
	}
	if n := m.sql(t, "SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema = '"+p+"1'"); n != "1" {
		t.Errorf("the berth's %s1 has %s triggers, want the dump's 1", p, n)
	}
}

// A path is resolved in the main worktree, so a different file at the same
// path in the berth's worktree is not the one loaded.
func TestSeedFileIsReadFromTheMainWorktree(t *testing.T) {
	repo, m := dumpServer(t)
	m.load(t, "ref_seed", tenantSeed)
	want := m.contents(t, "ref_seed")
	writeFile(t, filepath.Join(repo.Dir, "database/seeds/tenant.sql"), tenantSeed)
	writeFile(t, repo.privateConfig(), seedConfig(seedEntry("1", "database/seeds/tenant.sql")))
	wt := repo.AddWorktree(t, "feat-decoy")
	writeFile(t, filepath.Join(wt, "database/seeds/tenant.sql"), "CREATE TABLE decoy (id INT PRIMARY KEY);\n")

	mustBerth(t, repo, wt, "add", "--wait")

	if got := m.contents(t, berthPrefix(t, wt)+"1"); got != want {
		t.Errorf("the berth's database doesn't hold main's seed file:\nwant:\n%s\ngot:\n%s", want, got)
	}
}

// Guarding main must not turn away an ordinary file that only mentions it
// in text or comments.
func TestSeedFileMentioningMainInTextStillLoads(t *testing.T) {
	repo, m := dumpServer(t)
	seed := `-- copied from app_central.users
/* USE app_central; */
CREATE TABLE notes (id INT PRIMARY KEY, body VARCHAR(200) NOT NULL);
INSERT INTO notes VALUES (1, 'USE app_central;'), (2, 'INSERT INTO app_central.kept VALUES (1)');
`
	m.load(t, "ref_seed", seed)
	want := m.contents(t, "ref_seed")
	writeFile(t, filepath.Join(repo.Dir, "database/seeds/notes.sql"), seed)
	writeFile(t, repo.privateConfig(), seedConfig(seedEntry("1", "database/seeds/notes.sql")))
	wt := repo.AddWorktree(t, "feat-mention")

	mustBerth(t, repo, wt, "add", "--wait")

	if e := dbState(t, repo, nil, wt, "1"); e.Status != "ready" {
		t.Fatalf("a seed that only mentions main is %q, want ready: %+v", e.Status, e)
	}
	if got := m.contents(t, berthPrefix(t, wt)+"1"); got != want {
		t.Errorf("the berth's database doesn't hold the file's data:\nwant:\n%s\ngot:\n%s", want, got)
	}
}

// The same SQL gzipped loads to the same database as the plain file.
func TestGzippedSeedFileLoadsTheSameAsThePlainOne(t *testing.T) {
	repo, m := dumpServer(t)
	writeFile(t, filepath.Join(repo.Dir, "database/seeds/plain.sql"), tenantSeed)
	writeFile(t, filepath.Join(repo.Dir, "database/seeds/zipped.sql.gz"), gzipped(t, tenantSeed))
	writeFile(t, repo.privateConfig(), seedConfig(seedEntry("1", "database/seeds/plain.sql"), seedEntry("2", "database/seeds/zipped.sql.gz")))
	wt := repo.AddWorktree(t, "feat-gzip")

	mustBerth(t, repo, wt, "add", "--wait")

	p := berthPrefix(t, wt)
	if e := dbState(t, repo, nil, wt, "2"); e.Status != "ready" {
		t.Fatalf("the gzipped seed is %q, want ready: %+v", e.Status, e)
	}
	plain, zipped := m.contents(t, p+"1"), m.contents(t, p+"2")
	if plain != zipped || m.tables(t, p+"2") != "2" {
		t.Errorf("the gzipped seed loaded differently from the plain one:\nplain:\n%s\ngzipped:\n%s", plain, zipped)
	}
}

// The missing file fails only its own database, with a message naming the
// path, while a dump and another seed finish.
func TestMissingSeedFileFailsThatDatabaseNamingThePathAndOthersFinish(t *testing.T) {
	repo, m := dumpServer(t)
	m.load(t, "ref_seed", tenantSeed)
	wantSeed := m.contents(t, "ref_seed")
	wantDump := m.contents(t, "app_central")
	writeFile(t, filepath.Join(repo.Dir, "database/seeds/tenant.sql"), tenantSeed)
	writeFile(t, repo.privateConfig(), seedConfig(
		`central = "dump"`,
		seedEntry("1", "database/seeds/absent.sql"),
		seedEntry("2", "database/seeds/tenant.sql"),
	))
	wt := repo.AddWorktree(t, "feat-missing")

	res := repo.Berth(t, wt, "add", "--wait")

	if res.Exit != 4 {
		t.Errorf("berth add --wait exited %d with a seed file missing, want 4 (state clone failed)\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	e := dbState(t, repo, nil, wt, "1")
	if e.Status != "failed" {
		t.Errorf("database 1 with its seed file missing is %q, want failed: %+v", e.Status, e)
	}
	if !strings.Contains(e.Error, "database/seeds/absent.sql") {
		t.Errorf("database 1's failure says %q, want the seed file's path", e.Error)
	}
	p := berthPrefix(t, wt)
	if e := dbState(t, repo, nil, wt, "2"); e.Status != "ready" {
		t.Errorf("the other seed is %q, want ready: %+v", e.Status, e)
	}
	if got := m.contents(t, p+"2"); got != wantSeed {
		t.Errorf("the other seed's data is wrong:\nwant:\n%s\ngot:\n%s", wantSeed, got)
	}
	if e := dbState(t, repo, nil, wt, "central"); e.Status != "ready" {
		t.Errorf("the dump is %q, want ready: %+v", e.Status, e)
	}
	if got := m.contents(t, p+"central"); got != wantDump {
		t.Errorf("the dump's data is wrong:\nwant:\n%s\ngot:\n%s", wantDump, got)
	}
}

// A file that can't be loaded to its end never reads ready, and fails
// only its own database.
func TestBrokenSeedFilesFailAndTheOthersFinish(t *testing.T) {
	repo, m := dumpServer(t)
	truncated := gzipped(t, tenantSeed)
	writeFile(t, filepath.Join(repo.Dir, "database/seeds/truncated.sql.gz"), truncated[:len(truncated)/2])
	writeFile(t, filepath.Join(repo.Dir, "database/seeds/bad.sql"), "CREATE TABLE t (id INT PRIMARY KEY);\nINSERT INTO t VALUES (1);\nTHIS IS NOT SQL;\n")
	writeFile(t, repo.privateConfig(), seedConfig(
		`central = "dump"`,
		seedEntry("1", "database/seeds/truncated.sql.gz"),
		seedEntry("2", "database/seeds/bad.sql"),
	))
	wt := repo.AddWorktree(t, "feat-broken")

	if res := repo.Berth(t, wt, "add", "--wait"); res.Exit != 4 {
		t.Errorf("berth add --wait exited %d with broken seed files, want 4\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}

	for _, key := range []string{"1", "2"} {
		if e := dbState(t, repo, nil, wt, key); e.Status != "failed" {
			t.Errorf("database %s with a broken seed is %q, want failed: %+v", key, e.Status, e)
		}
	}
	if e := dbState(t, repo, nil, wt, "central"); e.Status != "ready" {
		t.Errorf("the dump beside the broken seeds is %q, want ready: %+v", e.Status, e)
	}
	if got, main := m.contents(t, berthPrefix(t, wt)+"central"), m.contents(t, "app_central"); got != main {
		t.Errorf("the dump beside the broken seeds differs from main's:\nmain:\n%s\nberth:\n%s", main, got)
	}
}

// Reset discards what the berth did and loads the file as it is at the time
// of the reset, keeping the berth's prefix.
func TestResetLoadsTheSeedFileAgain(t *testing.T) {
	repo, m := dumpServer(t)
	path := filepath.Join(repo.Dir, "database/seeds/tenant.sql")
	writeFile(t, path, tenantSeed)
	writeFile(t, repo.privateConfig(), seedConfig(seedEntry("1", "database/seeds/tenant.sql")))
	wt := repo.AddWorktree(t, "feat-reseed")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)
	m.sql(t, "DELETE FROM `"+p+"1`.plans; CREATE TABLE `"+p+"1`.berth_only (id INT PRIMARY KEY)")
	updated := tenantSeed + "INSERT INTO tenants VALUES (4, 'added later', NULL);\n"
	writeFile(t, path, updated)
	m.load(t, "ref_seed", updated)
	want := m.contents(t, "ref_seed")

	mustBerth(t, repo, wt, "reset")
	mustBerth(t, repo, wt, "status", "--wait")

	if e := dbState(t, repo, nil, wt, "1"); e.Status != "ready" || e.Target != p+"1" {
		t.Errorf("database 1 after reset: status %q target %q, want ready in %s1: %+v", e.Status, e.Target, p, e)
	}
	if got := m.contents(t, p+"1"); got != want {
		t.Errorf("after reset the berth's database doesn't hold the file's current data:\nwant:\n%s\ngot:\n%s", want, got)
	}
	if got := berthPrefix(t, wt); got != p {
		t.Errorf("reset changed the berth's prefix from %q to %q", p, got)
	}
}

// Seeds run in the background as dump does: add returns and status shows
// progress while the file loads.
func TestAddReturnsWhileSeedingAndStatusShowsProgress(t *testing.T) {
	repo, m := dumpServer(t)
	m.load(t, "ref_seed", tenantSeed)
	want := m.contents(t, "ref_seed")
	writeFile(t, filepath.Join(repo.Dir, "database/seeds/slow.sql"), tenantSeed+"SELECT SLEEP(15);\n")
	writeFile(t, repo.privateConfig(), seedConfig(seedEntry("1", "database/seeds/slow.sql")))
	wt := repo.AddWorktree(t, "feat-bg-seed")

	start := time.Now()
	if res := repo.Berth(t, wt, "add"); res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("berth add took %s with a 15s seed, want it to return while the seed loads", took)
	}
	var e dbClone
	waitFor(t, 10*time.Second, "the seed's progress to pass 0", func() bool {
		e = dbState(t, repo, nil, wt, "1")
		if e.Status != "cloning" {
			t.Fatalf("database 1 is %q while its seed loads, want cloning: %+v", e.Status, e)
		}
		return e.Progress != nil && *e.Progress > 0
	})
	if *e.Progress >= 1 {
		t.Errorf("progress %v while still loading, want below 1", *e.Progress)
	}
	if human := mustBerth(t, repo, wt, "status").Stdout; !regexp.MustCompile(`database \S*1\s+cloning \(\d+%`).MatchString(human) {
		t.Errorf("human status shows no progress for the seed:\n%s", human)
	}

	mustBerth(t, repo, wt, "status", "--wait")
	if e := dbState(t, repo, nil, wt, "1"); e.Status != "ready" {
		t.Fatalf("after status --wait, database 1 is %q, want ready: %+v", e.Status, e)
	}
	if got := m.contents(t, berthPrefix(t, wt)+"1"); got != want {
		t.Errorf("after the background load the database doesn't hold the file's data:\nwant:\n%s\ngot:\n%s", want, got)
	}
}

// Recovery as dump: kill -9 mid-load reads as failed, never ready, and add again
// loads the file.
func TestKilledSeedFailsAndAddRecovers(t *testing.T) {
	repo, m := dumpServer(t)
	m.load(t, "ref_seed", tenantSeed)
	want := m.contents(t, "ref_seed")
	path := filepath.Join(repo.Dir, "database/seeds/slow.sql")
	writeFile(t, path, tenantSeed+"SELECT SLEEP(120);\n")
	writeFile(t, repo.privateConfig(), seedConfig(seedEntry("1", "database/seeds/slow.sql")))
	wt := repo.AddWorktree(t, "feat-kill-seed")

	if res := repo.Berth(t, wt, "add"); res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	var pid int
	waitFor(t, 30*time.Second, "the seed's process to start", func() bool {
		e := dbState(t, repo, nil, wt, "1")
		if e.Status == "ready" {
			t.Fatalf("database 1 reported ready while its seed loads: %+v", e)
		}
		pid = e.PID
		return e.Status == "cloning" && pid > 0
	})
	if err := killNow(pid); err != nil {
		t.Fatalf("kill -9 %d: %v", pid, err)
	}
	waitFor(t, 10*time.Second, "status to report the killed load as failed", func() bool {
		e := dbState(t, repo, nil, wt, "1")
		if e.Status == "ready" {
			t.Fatalf("database 1 reported ready after its load was killed mid-way: %+v", e)
		}
		return e.Status == "failed"
	})

	writeFile(t, path, tenantSeed)
	mustBerth(t, repo, wt, "add", "--wait")

	if e := dbState(t, repo, nil, wt, "1"); e.Status != "ready" {
		t.Fatalf("after add again, database 1 is %q, want ready: %+v", e.Status, e)
	}
	if got := m.contents(t, berthPrefix(t, wt)+"1"); got != want {
		t.Errorf("after recovery the database doesn't hold the file's data:\nwant:\n%s\ngot:\n%s", want, got)
	}
}
