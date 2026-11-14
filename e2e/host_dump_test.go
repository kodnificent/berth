//go:build e2e

package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const dumpConfig = unpublishedDB + `[state.mysql]
from = "env:DB_*"
prefix = "env:DB_DATABASE_PREFIX"
default = "skip"

[state.mysql.databases]
central = "dump"
reports = "dump"
"1" = "fresh"
`

const dumpSeed = `SET SESSION cte_max_recursion_depth = 100000;
CREATE TABLE app_central.users (
  id INT PRIMARY KEY,
  email VARCHAR(191) CHARACTER SET utf8mb4 NOT NULL,
  balance DECIMAL(12,2) NOT NULL,
  joined DATETIME(6) NOT NULL,
  note TEXT NULL,
  avatar VARBINARY(32) NOT NULL,
  UNIQUE KEY (email)
);
INSERT INTO app_central.users
  WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 20000)
  SELECT n, CONCAT('user', n, '@example.test ', CONVERT(0xC3A9F09F9880 USING utf8mb4)), n * 1.25,
    TIMESTAMP('2024-01-01') + INTERVAL n SECOND,
    IF(n % 7 = 0, NULL, CONCAT('it''s a \\ note ', REPEAT('x', n % 300))), UNHEX(SHA2(n, 256))
  FROM seq;
CREATE TABLE app_central.orders (
  id INT PRIMARY KEY AUTO_INCREMENT,
  user_id INT NOT NULL,
  total BIGINT NOT NULL,
  FOREIGN KEY (user_id) REFERENCES app_central.users (id)
);
INSERT INTO app_central.orders (user_id, total)
  WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 20000)
  SELECT n, n * 100 FROM seq;
CREATE TABLE app_central.kept (k VARCHAR(20) PRIMARY KEY, v INT);
INSERT INTO app_central.kept VALUES ('a', 1), ('b', 2), ('c', 3);
CREATE TABLE app_reports.daily (day DATE PRIMARY KEY, total BIGINT NOT NULL);
INSERT INTO app_reports.daily
  WITH RECURSIVE seq(n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM seq WHERE n < 999)
  SELECT DATE('2024-01-01') + INTERVAL n DAY, n * 3 FROM seq;
CREATE TABLE app_1.seeded (id INT PRIMARY KEY);
INSERT INTO app_1.seeded VALUES (1);
CREATE TABLE app_2.seeded (id INT PRIMARY KEY);
INSERT INTO app_2.seeded VALUES (2);
CREATE TABLE shop.items (id INT PRIMARY KEY);
INSERT INTO shop.items VALUES (1), (2)`

// dumpServer is a repo whose main uses a host server holding app_central and
// app_reports, both with data and copied by dump, app_1 (fresh) and app_2 (skip by
// default) with a table each, and shop outside the prefix.
func dumpServer(t *testing.T) (*Repo, *hostMySQL) {
	t.Helper()
	repo := NewRepo(t)
	m := startHostMySQL(t, repo, "")
	m.create(t, "app_central", "app_reports", "app_1", "app_2", "shop")
	m.sql(t, dumpSeed)
	pointMainAt(t, repo, m, "DB_DATABASE_PREFIX=app_", "DB_DATABASE=app_central")
	writeFile(t, repo.privateConfig(), dumpConfig)
	return repo, m
}

func (m *hostMySQL) baseTables(t *testing.T, db string) []string {
	t.Helper()
	var out []string
	for _, tb := range strings.Fields(m.sql(t, "SELECT table_name FROM information_schema.tables WHERE table_schema = '"+db+"' AND table_type = 'BASE TABLE' ORDER BY table_name")) {
		out = append(out, "`"+db+"`.`"+tb+"`")
	}
	return out
}

// contents describes db's tables, their columns and each table's CHECKSUM TABLE, with
// the database's own name left out so a copy compares equal to its source.
func (m *hostMySQL) contents(t *testing.T, db string) string {
	t.Helper()
	cols := m.sql(t, "SELECT table_name, column_name, column_type, is_nullable FROM information_schema.columns WHERE table_schema = '"+db+"' ORDER BY table_name, ordinal_position")
	sums := ""
	if tables := m.baseTables(t, db); len(tables) > 0 {
		sums = strings.ReplaceAll(m.sql(t, "CHECKSUM TABLE "+strings.Join(tables, ", ")), db+".", "")
	}
	return cols + "\n" + sums
}

// checkTables runs CHECK TABLE on every table in db, what mysqlcheck does, and fails on
// any table not reported OK.
func (m *hostMySQL) checkTables(t *testing.T, db string) {
	t.Helper()
	tables := m.baseTables(t, db)
	if len(tables) == 0 {
		t.Fatalf("database %s has no tables to check", db)
	}
	for _, line := range strings.Split(m.sql(t, "CHECK TABLE "+strings.Join(tables, ", ")), "\n") {
		if f := strings.Split(line, "\t"); len(f) != 4 || f[2] != "status" || f[3] != "OK" {
			t.Errorf("CHECK TABLE in %s: %s", db, line)
		}
	}
}

type dbClone struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Source   string   `json:"source"`
	Target   string   `json:"target"`
	Strategy string   `json:"strategy"`
	Status   string   `json:"status"`
	PID      int      `json:"pid"`
	Progress *float64 `json:"progress"`
	Error    string   `json:"error"`
}

// dbState is the status entry of the berth's database for key, from status --json run
// with env added to the repo's.
func dbState(t *testing.T, repo *Repo, env []string, dir, key string) dbClone {
	t.Helper()
	res := repo.BerthWith(t, env, dir, "status", "--json")
	var s struct {
		State []dbClone `json:"state"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &s); err != nil {
		t.Fatalf("berth status --json: exit %d: %v\n%s%s", res.Exit, err, res.Stdout, res.Stderr)
	}
	for _, e := range s.State {
		if e.Kind == "database" && e.Name == key {
			return e
		}
	}
	t.Fatalf("status lists no database %s:\n%s", key, res.Stdout)
	return dbClone{}
}

// writeProbe commits one statement at a time on main's server from a single session and
// times each from this side, so a lock that holds main's writes shows as a slow commit.
type writeProbe struct {
	mu        sync.Mutex
	writes    int
	slowest   time.Duration
	firstSlow time.Time
	err       error
	stop      chan struct{}
	done      chan struct{}
	cmd       *exec.Cmd
}

const slowWrite = time.Second

func probeWrites(t *testing.T, m *hostMySQL, stmt string) *writeProbe {
	t.Helper()
	cmd := exec.Command("docker", "exec", "-i", "-e", "MYSQL_PWD="+hostMySQLRootPassword, m.Container,
		"mysql", "-h127.0.0.1", "-uroot", "-N", "-B", "--unbuffered")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &writeProbe{stop: make(chan struct{}), done: make(chan struct{}), cmd: cmd}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	go func() {
		defer close(p.done)
		sc := bufio.NewScanner(stdout)
		for {
			select {
			case <-p.stop:
				_ = stdin.Close()
				return
			default:
			}
			start := time.Now()
			if _, err := fmt.Fprintf(stdin, "%s; SELECT 'ok';\n", stmt); err != nil {
				p.fail(err)
				return
			}
			if !sc.Scan() {
				p.fail(fmt.Errorf("the probe's session ended: %v", sc.Err()))
				return
			}
			took := time.Since(start)
			p.mu.Lock()
			p.writes++
			if took > p.slowest {
				p.slowest = took
			}
			if took > slowWrite && p.firstSlow.IsZero() {
				p.firstSlow = start
			}
			p.mu.Unlock()
			time.Sleep(50 * time.Millisecond)
		}
	}()
	return p
}

func (p *writeProbe) fail(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

// finish stops the probe and returns how many writes it committed, the slowest, and when
// the first slow one began.
func (p *writeProbe) finish(t *testing.T) (int, time.Duration, time.Time) {
	t.Helper()
	close(p.stop)
	select {
	case <-p.done:
	case <-time.After(30 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
		t.Errorf("a write on main was still waiting 30s after the copy ended")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		t.Fatalf("writing on main: %v", p.err)
	}
	return p.writes, p.slowest, p.firstSlow
}

// After add --wait, each dump database has main's tables with main's row
// checksums.
func TestDumpCopiesMainsTablesAndRows(t *testing.T) {
	repo, m := dumpServer(t)
	want := map[string]string{"central": m.contents(t, "app_central"), "reports": m.contents(t, "app_reports")}
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")

	p := berthPrefix(t, wt)
	for key, sum := range want {
		e := dbState(t, repo, nil, wt, key)
		if e.Status != "ready" || e.Strategy != "dump" || e.Target != p+key || e.Source != "app_"+key {
			t.Errorf("database %s after add --wait: status %q strategy %q source %q target %q, want ready dump from app_%s into %s%s", key, e.Status, e.Strategy, e.Source, e.Target, key, p, key)
		}
		if got := m.contents(t, p+key); got != sum {
			t.Errorf("the berth's %s%s differs from main's app_%s:\nmain:\n%s\nberth:\n%s", p, key, key, sum, got)
		}
	}
}

// The "State is consistent" and "The source is never harmed" guarantees:
// copying while main commits pairs of rows gives a copy that passes CHECK TABLE and holds
// whole transactions only, and main's rows not being written keep their checksums.
func TestDumpUnderWriteLoadIsConsistentAndLeavesMainUnchanged(t *testing.T) {
	repo, m := dumpServer(t)
	m.sql(t, `CREATE TABLE app_central.a (id BIGINT PRIMARY KEY AUTO_INCREMENT, pad CHAR(200) NOT NULL DEFAULT 'padding');
CREATE TABLE app_central.b (id BIGINT PRIMARY KEY AUTO_INCREMENT, pad CHAR(200) NOT NULL DEFAULT 'padding')`)

	writer := exec.Command("docker", "exec", m.Container, "sh", "-c",
		`yes "START TRANSACTION; INSERT INTO a () VALUES (); INSERT INTO b () VALUES (); COMMIT;" | MYSQL_PWD=`+hostMySQLRootPassword+` mysql -h127.0.0.1 -uroot app_central`)
	if err := writer.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Process.Kill(); _ = writer.Wait() }()
	count := func(db, table string) int { return atoi(t, m.sql(t, "SELECT COUNT(*) FROM `"+db+"`."+table)) }
	waitFor(t, time.Minute, "main's writer to commit rows", func() bool { return count("app_central", "a") > 200 })

	settled := atoi(t, m.sql(t, "SELECT MAX(id) FROM app_central.a"))
	unwritten := func() string {
		return m.sql(t, fmt.Sprintf("SELECT COUNT(*), BIT_XOR(CRC32(CONCAT(id, pad))) FROM app_central.a WHERE id <= %d", settled)) + " " +
			m.sql(t, "CHECKSUM TABLE app_central.users, app_central.orders, app_central.kept") + " " +
			m.contents(t, "app_reports") + m.contents(t, "app_1") + m.contents(t, "app_2") + m.contents(t, "shop")
	}
	before := unwritten()

	wt := repo.AddWorktree(t, "feat-load")
	mustBerth(t, repo, wt, "add", "--wait")
	if after := count("app_central", "a"); after <= settled {
		t.Fatalf("main stopped taking writes during the copy: %d rows, %d before", after, settled)
	}

	p := berthPrefix(t, wt)
	m.checkTables(t, p+"central")
	a, b := count(p+"central", "a"), count(p+"central", "b")
	t.Logf("berth holds %d pairs of rows; main kept writing during the copy", a)
	if a == 0 || a != b {
		t.Errorf("berth holds %d rows in a and %d in b; every transaction wrote one to each, so a torn copy shows as a mismatch", a, b)
	}
	if got := unwritten(); got != before {
		t.Errorf("main's rows not being written changed during the copy:\nbefore:\n%s\nafter:\n%s", before, got)
	}
}

// Main's InnoDB tables take writes throughout the copy.
func TestDumpNeverLocksMainsInnoDBTablesAgainstWrites(t *testing.T) {
	repo, m := dumpServer(t)
	m.sql(t, "CREATE TABLE app_central.ticks (id BIGINT PRIMARY KEY AUTO_INCREMENT, at DATETIME(6) NOT NULL)")
	wt := repo.AddWorktree(t, "feat-ticks")

	probe := probeWrites(t, m, "INSERT INTO app_central.ticks (at) VALUES (NOW(6))")
	start := time.Now()
	if res := berthWithDelay(t, repo, wt, "6s", "add", "--wait"); res.Exit != 0 {
		probe.finish(t)
		t.Fatalf("berth add --wait: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	took := time.Since(start)
	writes, slowest, _ := probe.finish(t)
	t.Logf("main committed %d writes during a %s add --wait, the slowest in %s", writes, took.Round(time.Millisecond), slowest.Round(time.Millisecond))
	if writes < 10 {
		t.Errorf("main committed only %d writes during a %s copy, too few to show it was never locked", writes, took)
	}
	if slowest > slowWrite {
		t.Errorf("a write to main's InnoDB table waited %s during the copy, want none held up", slowest)
	}
	if e := dbState(t, repo, nil, wt, "central"); e.Status != "ready" {
		t.Errorf("central after add --wait is %q, want ready: %+v", e.Status, e)
	}
}

// A table that can't be copied consistently without a pause, here MyISAM,
// is announced before main's writes to it are held, and they flow again after the copy.
func TestDumpAnnouncesAPauseBeforeItHoldsMainsWrites(t *testing.T) {
	repo, m := dumpServer(t)
	m.sql(t, "CREATE TABLE app_central.legacy_log (id BIGINT PRIMARY KEY AUTO_INCREMENT, line VARCHAR(100)) ENGINE=MyISAM; INSERT INTO app_central.legacy_log (line) VALUES ('first'), ('second')")
	wt := repo.AddWorktree(t, "feat-legacy")

	probe := probeWrites(t, m, "INSERT INTO app_central.legacy_log (line) VALUES ('probe')")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	add := exec.Command(berthBin, "add", "--wait")
	add.Dir, add.Env = wt, append(repo.Env(), cloneDelay+"=5s")
	add.Stdout, add.Stderr = w, w
	if err := add.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	var announcedAt time.Time
	var output strings.Builder
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		output.WriteString(line + "\n")
		if announcedAt.IsZero() && strings.Contains(line, "pause") && (strings.Contains(line, "legacy_log") || strings.Contains(line, "app_central")) {
			announcedAt = time.Now()
		}
	}
	addErr := add.Wait()
	_, _, firstSlow := probe.finish(t)
	if addErr != nil {
		t.Fatalf("berth add --wait: %v\n%s", addErr, output.String())
	}

	if announcedAt.IsZero() {
		t.Fatalf("berth add never said main's MyISAM table legacy_log needs a pause for the copy:\n%s", output.String())
	}
	if !firstSlow.IsZero() && announcedAt.After(firstSlow.Add(500*time.Millisecond)) {
		t.Errorf("main's writes to legacy_log were held from %s, before add announced the pause at %s", firstSlow.Format(time.StampMilli), announcedAt.Format(time.StampMilli))
	}
	after := probeWrites(t, m, "INSERT INTO app_central.legacy_log (line) VALUES ('after')")
	time.Sleep(time.Second)
	if writes, slowest, _ := after.finish(t); writes == 0 || slowest > slowWrite {
		t.Errorf("after the copy, writes to main's legacy_log: %d committed, slowest %s, want them flowing again", writes, slowest)
	}
	m.checkTables(t, berthPrefix(t, wt)+"central")
}

// Add returns while the copy runs, and status shows its progress, in JSON
// and in text, until status --wait sees it ready.
func TestAddReturnsWhileDumpingAndStatusShowsProgress(t *testing.T) {
	repo, m := dumpServer(t)
	want := m.contents(t, "app_central")
	wt := repo.AddWorktree(t, "feat-bg")

	start := time.Now()
	if res := berthWithDelay(t, repo, wt, "15s", "add"); res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("berth add took %s with a 15s copy, want it to return while the copy runs", took)
	}
	var e dbClone
	waitFor(t, 10*time.Second, "the copy's progress to pass 0", func() bool {
		e = dbState(t, repo, nil, wt, "central")
		if e.Status != "cloning" {
			t.Fatalf("central is %q while its copy runs, want cloning: %+v", e.Status, e)
		}
		return e.Progress != nil && *e.Progress > 0
	})
	if *e.Progress >= 1 {
		t.Errorf("progress %v while still cloning, want below 1", *e.Progress)
	}
	if human := mustBerth(t, repo, wt, "status").Stdout; !regexp.MustCompile(`database \S*central\s+cloning \(\d+%`).MatchString(human) {
		t.Errorf("human status shows no progress for central's copy:\n%s", human)
	}

	mustBerth(t, repo, wt, "status", "--wait")
	if e := dbState(t, repo, nil, wt, "central"); e.Status != "ready" {
		t.Fatalf("after status --wait, central is %q, want ready: %+v", e.Status, e)
	}
	if got := m.contents(t, berthPrefix(t, wt)+"central"); got != want {
		t.Errorf("after the background copy the berth's central differs from main's:\nmain:\n%s\nberth:\n%s", want, got)
	}
}

// kill -9 of the copy reads as failed, never ready, and add again leaves the
// database matching main's.
func TestKilledDumpFailsAndAddRecovers(t *testing.T) {
	repo, m := dumpServer(t)
	want := m.contents(t, "app_central")
	wt := repo.AddWorktree(t, "feat-kill")

	if res := berthWithDelay(t, repo, wt, "120s", "add"); res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	var pid int
	waitFor(t, 30*time.Second, "the copy's process to start", func() bool {
		e := dbState(t, repo, nil, wt, "central")
		if e.Status == "ready" {
			t.Fatalf("central reported ready while its copy runs: %+v", e)
		}
		pid = e.PID
		return e.Status == "cloning" && pid > 0
	})

	if err := killNow(pid); err != nil {
		t.Fatalf("kill -9 %d: %v", pid, err)
	}
	waitFor(t, 10*time.Second, "status to report the killed copy as failed", func() bool {
		e := dbState(t, repo, nil, wt, "central")
		if e.Status == "ready" {
			t.Fatalf("central reported ready after its copy was killed mid-way: %+v", e)
		}
		return e.Status == "failed"
	})
	if res := repo.Berth(t, wt, "status", "--wait"); res.Exit == 0 {
		t.Errorf("status --wait exited 0 with central's copy killed\n%s", res.Stdout)
	}

	mustBerth(t, repo, wt, "add", "--wait")
	if e := dbState(t, repo, nil, wt, "central"); e.Status != "ready" {
		t.Fatalf("after add again, central is %q, want ready: %+v", e.Status, e)
	}
	if got := m.contents(t, berthPrefix(t, wt)+"central"); got != want {
		t.Errorf("after recovery the berth's central differs from main's:\nmain:\n%s\nberth:\n%s", want, got)
	}
}

// Add --wait whose copy is killed exits 4 instead of waiting forever.
func TestAddWaitExitsFourWhenItsDumpIsKilled(t *testing.T) {
	repo, _ := dumpServer(t)
	wt := repo.AddWorktree(t, "feat-wait-fail")
	add := exec.Command(berthBin, "add", "--wait")
	add.Dir, add.Env = wt, append(repo.Env(), cloneDelay+"=120s")
	var out strings.Builder
	add.Stdout, add.Stderr = &out, &out
	if err := add.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- add.Wait() }()
	var pid int
	waitFor(t, 30*time.Second, "the copy's process to start", func() bool {
		select {
		case <-done:
			t.Fatalf("berth add --wait exited %d before its copy could be killed:\n%s", add.ProcessState.ExitCode(), out.String())
		default:
		}
		res := repo.Berth(t, wt, "status", "--json")
		var s struct {
			State []dbClone `json:"state"`
		}
		if json.Unmarshal([]byte(res.Stdout), &s) != nil {
			return false
		}
		for _, e := range s.State {
			if e.Kind == "database" && e.Name == "central" && e.Status == "cloning" && e.PID > 0 {
				pid = e.PID
			}
		}
		return pid > 0
	})
	if err := killNow(pid); err != nil {
		t.Fatalf("kill -9 %d: %v", pid, err)
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = add.Process.Kill()
		t.Fatalf("berth add --wait still waiting 30s after its copy was killed:\n%s", out.String())
	}
	if code := add.ProcessState.ExitCode(); code != 4 {
		t.Errorf("berth add --wait exited %d after its copy failed, want 4:\n%s", code, out.String())
	}
}

// A copy that would leave less than min_free is warned about by add,
// naming the database and its size, and runs anyway; a low threshold gives no warning.
func TestDumpWarnsAboutDiskBeforeCopying(t *testing.T) {
	repo, _ := dumpServer(t)
	writeFile(t, repo.privateConfig(), dumpConfig+"\n[clone]\nmin_free = \"1MB\"\n")
	roomy := repo.AddWorktree(t, "feat-roomy")
	if res := mustBerth(t, repo, roomy, "add", "--wait"); strings.Contains(res.Stderr, "free") {
		t.Errorf("berth add warned about disk with a 1MB threshold:\n%s", res.Stderr)
	}

	writeFile(t, repo.privateConfig(), dumpConfig+"\n[clone]\nmin_free = \"100000TB\"\n")
	wt := repo.AddWorktree(t, "feat-full")
	res := berthWithDelay(t, repo, wt, "5s", "add")
	if res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	warned := false
	for _, l := range strings.Split(res.Stderr, "\n") {
		if strings.Contains(l, "central") && strings.Contains(l, "free") && sizeInMessage.MatchString(l) {
			warned = true
		}
	}
	if !warned {
		t.Errorf("berth add gave no disk warning naming central and its size before its copy:\n%s", res.Stderr)
	}
	mustBerth(t, repo, wt, "status", "--wait")
	if e := dbState(t, repo, nil, wt, "central"); e.Status != "ready" {
		t.Errorf("after the warning central is %q, want the copy to run anyway: %+v", e.Status, e)
	}
}

// With no compose project and Docker unreachable, add copies every dump
// database and creates the fresh one.
func TestDumpRunsWithoutComposeOrDocker(t *testing.T) {
	repo, m := dumpServer(t)
	repo.Git(t, "rm", "-q", "compose.yml")
	repo.Git(t, "commit", "-q", "-m", "no compose project")
	writeFile(t, repo.privateConfig(), strings.TrimPrefix(dumpConfig, unpublishedDB))
	want := map[string]string{"central": m.contents(t, "app_central"), "reports": m.contents(t, "app_reports")}
	wt := repo.AddWorktree(t, "feat-nodocker")
	noDocker := deadDocker(t)

	if res := repo.BerthWith(t, noDocker, wt, "add", "--wait"); res.Exit != 0 {
		t.Fatalf("add --wait with no compose project and Docker unreachable: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	p := berthPrefix(t, wt)
	for key, sum := range want {
		if e := dbState(t, repo, noDocker, wt, key); e.Status != "ready" {
			t.Errorf("database %s without Docker is %q, want ready: %+v", key, e.Status, e)
		}
		if got := m.contents(t, p+key); got != sum {
			t.Errorf("without Docker the berth's %s%s differs from main's app_%s:\nmain:\n%s\nberth:\n%s", p, key, key, sum, got)
		}
	}
	if !m.has(t, p+"1") || m.tables(t, p+"1") != "0" {
		t.Errorf("without Docker the fresh database %s1 is missing or not empty: %v", p, m.databases(t))
	}
}

// The password berth reads from main's .env is in no clone state, clone
// log or output, while a copy runs, after it finishes and after one is killed.
func TestDumpKeepsThePasswordOutOfStateLogsAndOutput(t *testing.T) {
	repo, m := dumpServer(t)
	wt := repo.AddWorktree(t, "feat-secret")
	outputs := map[string]string{}
	run := func(env []string, args ...string) Result {
		res := repo.BerthWith(t, env, wt, args...)
		outputs[fmt.Sprintf("%d: berth %s", len(outputs), strings.Join(args, " "))] = res.Stdout + res.Stderr
		return res
	}
	scan := func(when string) {
		err := filepath.WalkDir(repo.StateDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(data), m.Password) {
				t.Errorf("%s, %s contains the database password", when, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	slow := []string{cloneDelay + "=8s"}

	if res := run(slow, "add"); res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	run(nil, "status")
	run(nil, "status", "--json")
	scan("while the copy runs")
	run(nil, "status", "--wait")
	run(nil, "status", "--json")
	scan("after the copy")

	run(nil, "reset", "--dry-run")
	run(nil, "reset", "--dry-run", "--json")
	if res := run([]string{cloneDelay + "=120s"}, "reset", "--json"); res.Exit != 0 {
		t.Fatalf("berth reset: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	var pid int
	waitFor(t, 30*time.Second, "reset's copy to start", func() bool {
		e := dbState(t, repo, nil, wt, "central")
		pid = e.PID
		return e.Status == "cloning" && pid > 0
	})
	if err := killNow(pid); err != nil {
		t.Fatalf("kill -9 %d: %v", pid, err)
	}
	waitFor(t, 10*time.Second, "the killed copy to read as failed", func() bool {
		return dbState(t, repo, nil, wt, "central").Status == "failed"
	})
	run(nil, "status")
	run(nil, "status", "--json")
	run(nil, "status", "--wait")
	run(nil, "doctor")
	scan("after a copy was killed")

	for cmd, out := range outputs {
		if strings.Contains(out, m.Password) {
			t.Errorf("%s prints the database password", cmd)
		}
	}
}
