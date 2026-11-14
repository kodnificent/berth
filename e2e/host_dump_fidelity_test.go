//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// notesOf is the notes status --json gives the berth's database key.
func notesOf(t *testing.T, repo *Repo, wt, key string) string {
	t.Helper()
	var s struct {
		State []struct {
			Kind  string   `json:"kind"`
			Name  string   `json:"name"`
			Notes []string `json:"notes"`
		} `json:"state"`
	}
	res := mustBerth(t, repo, wt, "status", "--json")
	if err := json.Unmarshal([]byte(res.Stdout), &s); err != nil {
		t.Fatalf("berth status --json: %v\n%s", err, res.Stdout)
	}
	for _, e := range s.State {
		if e.Kind == "database" && e.Name == key {
			return strings.Join(e.Notes, "\n")
		}
	}
	t.Fatalf("status lists no database %s:\n%s", key, res.Stdout)
	return ""
}

func TestMainCanAlterACopiedTableWhileTheCopyIsStillOpen(t *testing.T) {
	repo, m := dumpServer(t)
	wt := repo.AddWorktree(t, "feat-ddl")
	if res := berthWithDelay(t, repo, wt, "30s", "add"); res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	p := berthPrefix(t, wt)
	waitFor(t, 30*time.Second, "every table to be copied while the copy stays open", func() bool {
		e := dbState(t, repo, nil, wt, "central")
		return e.Status == "cloning" && e.Progress != nil && m.has(t, p+"central") && m.sql(t, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = '"+p+"central'") == "3"
	})

	start := time.Now()
	m.sql(t, "SET SESSION lock_wait_timeout = 5; ALTER TABLE app_central.kept ADD COLUMN extra INT NULL; INSERT INTO app_central.kept VALUES ('d', 4, NULL)")
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("ALTER and INSERT on main's copied table took %s while the copy was open, want them prompt", took)
	}
	if e := dbState(t, repo, nil, wt, "central"); e.Status != "cloning" {
		t.Fatalf("central is %q, want the copy still open while main was altered", e.Status)
	}
	mustBerth(t, repo, wt, "status", "--wait")
}

func TestCopiedTriggersAndViewsNeverReachMain(t *testing.T) {
	repo, m := dumpServer(t)
	m.sql(t, `CREATE TABLE app_central.audit (id INT PRIMARY KEY AUTO_INCREMENT, what VARCHAR(20));
CREATE TRIGGER app_central.kept_audit AFTER INSERT ON app_central.kept FOR EACH ROW INSERT INTO app_central.audit (what) VALUES (NEW.k);
CREATE VIEW app_central.shop_items AS SELECT id FROM shop.items`)
	wt := repo.AddWorktree(t, "feat-refs")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)

	m.sql(t, "INSERT INTO `"+p+"central`.kept VALUES ('berth', 7)")
	if n := m.sql(t, "SELECT COUNT(*) FROM app_central.audit"); n != "0" {
		t.Errorf("a write in the berth added %s rows to main's app_central.audit, want none", n)
	}
	if n := m.sql(t, "SELECT COUNT(*) FROM information_schema.views WHERE table_schema = '"+p+"central'"); n != "0" {
		t.Errorf("the berth has %s views, want shop_items, which reads main's shop, left out", n)
	}
	notes := notesOf(t, repo, wt, "central")
	if !strings.Contains(notes, "kept_audit") || !strings.Contains(notes, "shop_items") {
		t.Errorf("status notes don't name the trigger and view left out:\n%s", notes)
	}
}

func TestViewCallingAStoredFunctionIsLeftOutAndTheCopyIsReady(t *testing.T) {
	repo, m := dumpServer(t)
	m.sql(t, `CREATE FUNCTION app_central.dollars(c BIGINT) RETURNS DECIMAL(14,2) DETERMINISTIC RETURN c / 100;
USE app_central;
CREATE VIEW order_dollars AS SELECT id, dollars(total) AS d FROM orders;
CREATE VIEW plain AS SELECT id FROM orders WHERE id < 3`)
	writeFile(t, repo.privateConfig(), dumpConfig+"\n[hooks]\npost-add = \"true\"\n")
	wt := repo.AddWorktree(t, "feat-routines")
	mustBerth(t, repo, wt, "add", "--wait")
	mustBerth(t, repo, wt, "status", "--wait")

	if e := dbState(t, repo, nil, wt, "central"); e.Status != "ready" {
		t.Fatalf("central is %q with a view calling a stored function, want ready: %+v", e.Status, e)
	}
	notes := notesOf(t, repo, wt, "central")
	if !strings.Contains(notes, "order_dollars") || !strings.Contains(notes, "1 stored routine") {
		t.Errorf("status notes don't name the view left out and the routine not copied:\n%s", notes)
	}
	if n := m.sql(t, "SELECT COUNT(*) FROM information_schema.views WHERE table_schema = '"+berthPrefix(t, wt)+"central'"); n != "1" {
		t.Errorf("the berth has %s views, want plain copied", n)
	}
}

func TestDumpKeepsFloatsAndDoublesExact(t *testing.T) {
	repo, m := dumpServer(t)
	m.sql(t, `CREATE TABLE app_reports.measures (id INT PRIMARY KEY, f FLOAT, d DOUBLE);
SET SESSION sql_mode = '';
INSERT INTO app_reports.measures VALUES (1, 51.507351, 0.1 + 0.2), (2, 16777216, PI()), (3, 1.2345678, 1e308), (4, 3.4028235e38, -2.2250738585072014e-308), (5, -1.17549435e-38, 1e-300)`)
	want := m.contents(t, "app_reports")
	wt := repo.AddWorktree(t, "feat-floats")
	mustBerth(t, repo, wt, "add", "--wait")
	if got := m.contents(t, berthPrefix(t, wt)+"reports"); got != want {
		t.Errorf("the berth's floats differ from main's:\nmain:\n%s\nberth:\n%s", want, got)
	}
}

func TestDumpCopiesRowsLargerThanThirtyTwoMegabytes(t *testing.T) {
	repo, m := dumpServer(t)
	m.sql(t, `CREATE TABLE app_reports.files (id INT PRIMARY KEY, body LONGBLOB, caption LONGTEXT CHARACTER SET utf8mb4);
INSERT INTO app_reports.files VALUES (1, REPEAT(UNHEX(SHA2('berth', 256)), 34 * 1024 * 1024 / 32), REPEAT('é😀x', 2 * 1024 * 1024)), (2, 'small', 'small')`)
	want := m.contents(t, "app_reports")
	wt := repo.AddWorktree(t, "feat-large")
	mustBerth(t, repo, wt, "add", "--wait")
	if got := m.contents(t, berthPrefix(t, wt)+"reports"); got != want {
		t.Errorf("the berth's large rows differ from main's:\nmain:\n%s\nberth:\n%s", want, got)
	}
}

// sqlAsWritten runs q on m's server with its comments sent along, as an app's own
// migrations send them, rather than stripped by the client.
func (m *hostMySQL) sqlAsWritten(t *testing.T, q string) {
	t.Helper()
	out, err := exec.Command("docker", "exec", "-e", "MYSQL_PWD="+hostMySQLRootPassword, m.Container,
		"mysql", "--comments", "-h127.0.0.1", "-uroot", "-N", "-B", "-e", q).CombinedOutput()
	if err != nil {
		t.Fatalf("mysql %q: %v\n%s", q, err, out)
	}
}

func TestTriggersNamingMainAroundCommentsNeverWriteToMain(t *testing.T) {
	repo, m := dumpServer(t)
	m.sql(t, `CREATE TABLE app_central.audit (id INT PRIMARY KEY AUTO_INCREMENT, what VARCHAR(20));
CREATE TABLE shop.audit (id INT PRIMARY KEY AUTO_INCREMENT, what VARCHAR(20));
CREATE PROCEDURE app_central.p() INSERT INTO app_central.audit (what) VALUES ('p')`)
	bodies := map[string]string{
		"t_line":   "INSERT INTO app_central -- x\n .audit (what) VALUES (NEW.k)",
		"t_block":  "INSERT INTO shop/**/ .audit (what) VALUES (NEW.k)",
		"t_vtab":   "INSERT INTO shop\v.audit (what) VALUES (NEW.k)",
		"t_hash":   "INSERT INTO shop#x\n.audit (what) VALUES (NEW.k)",
		"t_quoted": "INSERT INTO `shop`/*x*/ .`audit` (what) VALUES (NEW.k)",
		"t_call":   "CALL app_central/**/.p()",
	}
	for name, body := range bodies {
		m.sqlAsWritten(t, "CREATE TRIGGER app_central."+name+" AFTER INSERT ON app_central.kept FOR EACH ROW "+body)
	}
	wt := repo.AddWorktree(t, "feat-comments")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)

	m.sql(t, "INSERT INTO `"+p+"central`.kept VALUES ('berth', 7)")
	for _, table := range []string{"app_central.audit", "shop.audit"} {
		if n := m.sql(t, "SELECT COUNT(*) FROM "+table); n != "0" {
			t.Errorf("a write in the berth added %s rows to main's %s, want none", n, table)
		}
	}
	notes := notesOf(t, repo, wt, "central")
	for name := range bodies {
		if !strings.Contains(notes, "trigger "+name+" left out") {
			t.Errorf("status notes don't say trigger %s was left out:\n%s", name, notes)
		}
	}
}

func TestMergeTableIsLeftOutAndMainsRowsStayPut(t *testing.T) {
	repo, m := dumpServer(t)
	m.sql(t, `CREATE TABLE shop.logs (id INT NOT NULL, line VARCHAR(10)) ENGINE=MyISAM;
INSERT INTO shop.logs VALUES (1, 'a'), (2, 'b');
CREATE TABLE app_central.all_logs (id INT NOT NULL, line VARCHAR(10)) ENGINE=MERGE UNION=(shop.logs) INSERT_METHOD=LAST`)
	wt := repo.AddWorktree(t, "feat-merge")
	res := mustBerth(t, repo, wt, "add", "--wait")
	for _, l := range strings.Split(res.Stderr, "\n") {
		if strings.Contains(l, "pause") {
			t.Errorf("add announced a pause for a database whose only non-InnoDB table is a MERGE table it doesn't copy: %s", l)
		}
	}
	if n := m.sql(t, "SELECT COUNT(*) FROM shop.logs"); n != "2" {
		t.Errorf("main's shop.logs holds %s rows after the copy, want its 2", n)
	}
	if m.sql(t, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = '"+berthPrefix(t, wt)+"central' AND table_name = 'all_logs'") != "0" {
		t.Errorf("the berth has a copy of the MERGE table all_logs, want it left out")
	}
	if notes := notesOf(t, repo, wt, "central"); !strings.Contains(notes, "all_logs") {
		t.Errorf("status notes don't name the MERGE table left out:\n%s", notes)
	}
}

func TestForeignKeyIntoAnotherDatabaseIsDroppedWhateverItsName(t *testing.T) {
	repo, m := dumpServer(t)
	m.sql(t, "CREATE TABLE app_central.fk (id INT PRIMARY KEY, item INT, CONSTRAINT `a\nb` FOREIGN KEY (item) REFERENCES shop.items (id)); INSERT INTO app_central.fk VALUES (1, 2)")
	wt := repo.AddWorktree(t, "feat-fk")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)

	if n := m.sql(t, "SELECT COUNT(*) FROM information_schema.referential_constraints WHERE constraint_schema = '"+p+"central' AND unique_constraint_schema <> constraint_schema"); n != "0" {
		t.Errorf("the berth's tables keep %s foreign keys into other databases, want none", n)
	}
	m.sql(t, "DELETE FROM app_central.fk")
	if out, err := m.command("DELETE FROM shop.items WHERE id = 2").CombinedOutput(); err != nil {
		t.Errorf("main can't delete its own shop.items row the berth's copy refers to: %v\n%s", err, out)
	}
}

func TestPartitionedTableIsCopied(t *testing.T) {
	repo, m := dumpServer(t)
	m.sql(t, `CREATE TABLE app_reports.parts (id INT PRIMARY KEY, v INT) PARTITION BY RANGE (id) (PARTITION p0 VALUES LESS THAN (100), PARTITION p1 VALUES LESS THAN MAXVALUE);
INSERT INTO app_reports.parts VALUES (1, 10), (150, 20), (99, 30)`)
	want := m.contents(t, "app_reports")
	wt := repo.AddWorktree(t, "feat-parts")
	mustBerth(t, repo, wt, "add", "--wait")
	if got := m.contents(t, berthPrefix(t, wt)+"reports"); got != want {
		t.Errorf("the berth's reports differ from main's with a partitioned table:\nmain:\n%s\nberth:\n%s", want, got)
	}
}

func TestTableSwappedToMergeMidCopyIsLeftOut(t *testing.T) {
	repo, m := dumpServer(t)
	m.sql(t, `CREATE TABLE app_central.a_big (id INT PRIMARY KEY, body LONGBLOB);
CREATE TABLE app_central.zz_swap (id INT NOT NULL, v INT);
INSERT INTO app_central.zz_swap VALUES (1, 1);
CREATE TABLE shop.entries (id INT NOT NULL, v INT) ENGINE=MyISAM;
INSERT INTO shop.entries VALUES (1, 1), (2, 2), (3, 3)`)
	for i := 0; i < 64; i++ {
		m.sql(t, fmt.Sprintf("INSERT INTO app_central.a_big VALUES (%d, REPEAT(UNHEX(SHA2('%d', 256)), 4 * 1024 * 1024 / 32))", i, i))
	}
	wt := repo.AddWorktree(t, "feat-swap")
	mustBerth(t, repo, wt, "add")
	p := berthPrefix(t, wt)
	berthHas := func(table string) bool {
		return m.sql(t, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = '"+p+"central' AND table_name = '"+table+"'") == "1"
	}
	waitFor(t, 30*time.Second, "the copy to start on a_big", func() bool { return berthHas("a_big") })
	m.sql(t, "DROP TABLE app_central.zz_swap; CREATE TABLE app_central.zz_swap (id INT NOT NULL, v INT) ENGINE=MERGE UNION=(shop.entries) INSERT_METHOD=LAST")
	if berthHas("zz_swap") {
		t.Fatal("the copy reached zz_swap before main swapped it; make a_big larger so the swap lands mid-copy")
	}
	mustBerth(t, repo, wt, "status", "--wait")

	if n := m.sql(t, "SELECT COUNT(*) FROM shop.entries"); n != "3" {
		t.Errorf("main's shop.entries holds %s rows after the copy, want its 3", n)
	}
	if berthHas("zz_swap") {
		t.Errorf("the berth has the MERGE table zz_swap, want it left out")
	}
	if notes := notesOf(t, repo, wt, "central"); !strings.Contains(notes, "zz_swap") {
		t.Errorf("status notes don't name zz_swap:\n%s", notes)
	}
}
