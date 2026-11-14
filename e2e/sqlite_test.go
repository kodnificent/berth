//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func sqliteBin(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Fatalf("the SQLite tests drive databases with the sqlite3 CLI, which is not on PATH: %v", err)
	}
	return bin
}

func sqlite(t *testing.T, path, sql string) string {
	t.Helper()
	cmd := exec.Command(sqliteBin(t), "-batch", "-bail", "-cmd", ".timeout 10000", path)
	cmd.Stdin = strings.NewReader(sql)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("sqlite3 %s: %v\n%s\nsql:\n%.500s", path, err, stderr.String(), sql)
	}
	return strings.TrimSpace(stdout.String())
}

// makeLedger creates a database whose every insert also adds its amount to a running
// total in another table, so a copy torn between pages breaks the sum.
func makeLedger(t *testing.T, path, journal string, rows, pad int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	sqlite(t, path, fmt.Sprintf(`PRAGMA journal_mode=%s;
CREATE TABLE ledger (id INTEGER PRIMARY KEY, amount INTEGER NOT NULL, pad BLOB);
CREATE INDEX ledger_amount ON ledger (amount);
CREATE TABLE total (v INTEGER NOT NULL);
INSERT INTO total VALUES (0);
CREATE TRIGGER ledger_total AFTER INSERT ON ledger BEGIN UPDATE total SET v = v + NEW.amount; END;
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < %d)
INSERT INTO ledger (amount, pad) SELECT i, randomblob(%d) FROM n;
`, journal, rows, pad))
}

func ledgerRows(t *testing.T, path string) int {
	t.Helper()
	n, err := strconv.Atoi(sqlite(t, path, "SELECT COUNT(*) FROM ledger;"))
	if err != nil {
		t.Fatalf("counting rows in %s: %v", path, err)
	}
	return n
}

// checkConsistent fails unless path passes SQLite's integrity check and its ledger still
// sums to its total.
func checkConsistent(t *testing.T, what, path string) {
	t.Helper()
	if got := sqlite(t, path, "PRAGMA integrity_check;"); got != "ok" {
		t.Errorf("%s fails PRAGMA integrity_check:\n%s", what, got)
	}
	if got := sqlite(t, path, "SELECT (SELECT COALESCE(SUM(amount), 0) FROM ledger) = (SELECT v FROM total);"); got != "1" {
		t.Errorf("%s holds a ledger that no longer sums to its total, a write torn by the copy", what)
	}
}

func dump(t *testing.T, path string) string {
	t.Helper()
	return sqlite(t, path, ".dump")
}

func fileSum(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ignoreSQLite commits a .gitignore entry for database files, as an app that keeps its
// SQLite database in the worktree would have.
func ignoreSQLite(t *testing.T, repo *Repo) {
	t.Helper()
	appendFile(t, filepath.Join(repo.Dir, ".gitignore"), "*.sqlite\n*.sqlite-*\n*.db\n*.db-*\n")
	repo.Git(t, "add", ".gitignore")
	repo.Git(t, "commit", "-q", "-m", "ignore sqlite files")
}

func sqliteConfig(files ...string) string {
	return "[state.sqlite]\nfiles = [\"" + strings.Join(files, "\", \"") + "\"]\n"
}

// writeLoad keeps one connection to path committing inserts until the test ends or stop
// is called, which returns what the writer printed to stderr.
func writeLoad(t *testing.T, path string) (stop func() string) {
	t.Helper()
	cmd := exec.Command(sqliteBin(t), "-batch", "-cmd", ".timeout 10000", path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		w := bufio.NewWriter(stdin)
		for {
			select {
			case <-done:
				return
			default:
			}
			fmt.Fprint(w, "INSERT INTO ledger (amount, pad) SELECT abs(random() % 1000) + 1, randomblob(1000) FROM (SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4);\n")
			if w.Flush() != nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	var once sync.Once
	stop = func() string {
		once.Do(func() {
			close(done)
			<-finished
			stdin.Close()
			_ = cmd.Wait()
		})
		return stderr.String()
	}
	t.Cleanup(func() { stop() })
	return stop
}

// stringsIn collects every string anywhere in a decoded JSON value.
func stringsIn(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, stringsIn(e)...)
		}
		return out
	case map[string]any:
		var out []string
		for _, e := range x {
			out = append(out, stringsIn(e)...)
		}
		return out
	}
	return nil
}

func mentions(v any, rel string) bool {
	for _, s := range stringsIn(v) {
		if s == rel || strings.HasSuffix(s, string(filepath.Separator)+rel) {
			return true
		}
	}
	return false
}

// After add, each listed file in the berth holds what main's held at the
// copy and passes PRAGMA integrity_check.
func TestSQLiteFilesMatchMainAfterAdd(t *testing.T) {
	repo := NewRepo(t)
	ignoreSQLite(t, repo)
	files := []string{"database/app.sqlite", "storage/cache.db"}
	makeLedger(t, filepath.Join(repo.Dir, files[0]), "delete", 300, 1500)
	makeLedger(t, filepath.Join(repo.Dir, files[1]), "wal", 300, 1500)
	writeFile(t, repo.privateConfig(), sqliteConfig(files...))

	wt := repo.AddWorktree(t, "feat-sqlite")
	mustBerth(t, repo, wt, "add", "--wait")

	for _, rel := range files {
		berthDB := filepath.Join(wt, rel)
		if !fileExistsAt(berthDB) {
			t.Errorf("listed SQLite file %s is missing from the berth", rel)
			continue
		}
		if got := sqlite(t, berthDB, "PRAGMA integrity_check;"); got != "ok" {
			t.Errorf("the berth's %s fails PRAGMA integrity_check:\n%s", rel, got)
		}
		if dump(t, berthDB) != dump(t, filepath.Join(repo.Dir, rel)) {
			t.Errorf("the berth's %s holds different data from main's", rel)
		}
	}
}

// The source is never harmed: add leaves main's database files byte for byte as they
// were.
func TestSQLiteAddLeavesMainsFilesUnchanged(t *testing.T) {
	repo := NewRepo(t)
	ignoreSQLite(t, repo)
	files := []string{"database/app.sqlite", "storage/cache.db"}
	makeLedger(t, filepath.Join(repo.Dir, files[0]), "delete", 300, 1500)
	makeLedger(t, filepath.Join(repo.Dir, files[1]), "wal", 300, 1500)
	writeFile(t, repo.privateConfig(), sqliteConfig(files...))
	before := map[string]string{}
	for _, rel := range files {
		before[rel] = fileSum(t, filepath.Join(repo.Dir, rel))
	}

	wt := repo.AddWorktree(t, "feat-source")
	mustBerth(t, repo, wt, "add", "--wait")
	if !fileExistsAt(filepath.Join(wt, files[0])) {
		t.Fatalf("add did not clone %s, so it says nothing about main's files", files[0])
	}

	for _, rel := range files {
		if got := fileSum(t, filepath.Join(repo.Dir, rel)); got != before[rel] {
			t.Errorf("add changed main's %s", rel)
		}
	}
}

// While main commits a steady stream of writes, every berth made gets a copy
// that passes the integrity check, keeps the ledger summing to its total, and holds at
// least every row main had committed before add began. Main stays consistent too.
func TestSQLiteCopyUnderWriteLoadIsNeverTorn(t *testing.T) {
	sqliteBin(t)
	for _, journal := range []string{"delete", "wal"} {
		t.Run(journal, func(t *testing.T) {
			repo := NewRepo(t)
			ignoreSQLite(t, repo)
			rel := "database/app.sqlite"
			db := filepath.Join(repo.Dir, rel)
			makeLedger(t, db, journal, 2000, 1000)
			writeFile(t, repo.privateConfig(), sqliteConfig(rel))

			stop := writeLoad(t, db)
			waitFor(t, 30*time.Second, "main's write load to commit rows", func() bool { return ledgerRows(t, db) > 2100 })

			for i := range 3 {
				committed := ledgerRows(t, db)
				wt := repo.AddWorktree(t, fmt.Sprintf("feat-load-%d", i))
				mustBerth(t, repo, wt, "add", "--wait")
				berthDB := filepath.Join(wt, rel)
				if !fileExistsAt(berthDB) {
					t.Errorf("berth %d has no %s", i, rel)
					continue
				}
				checkConsistent(t, fmt.Sprintf("berth %d's copy of %s", i, rel), berthDB)
				if got := ledgerRows(t, berthDB); got < committed {
					t.Errorf("berth %d's copy holds %d rows, fewer than the %d main had committed before add began", i, got, committed)
				}
			}

			last := ledgerRows(t, db)
			time.Sleep(time.Second)
			if ledgerRows(t, db) <= last {
				t.Fatalf("main's write load had stopped by the end of the test; writer said:\n%s", stop())
			}
			stop()
			checkConsistent(t, "main's "+rel, db)
		})
	}
}

// Reset: after the berth's copy is written to and main moves on, reset
// leaves the berth's file holding what main's holds at the reset.
func TestSQLiteResetCopiesMainAgain(t *testing.T) {
	repo := NewRepo(t)
	ignoreSQLite(t, repo)
	rel := "database/app.sqlite"
	db := filepath.Join(repo.Dir, rel)
	makeLedger(t, db, "delete", 200, 500)
	writeFile(t, repo.privateConfig(), sqliteConfig(rel))
	wt := repo.AddWorktree(t, "feat-reset")
	mustBerth(t, repo, wt, "add", "--wait")
	berthDB := filepath.Join(wt, rel)
	sqlite(t, berthDB, "CREATE TABLE berth_only (v INTEGER); INSERT INTO ledger (amount) VALUES (7);")
	sqlite(t, db, "INSERT INTO ledger (amount) VALUES (11), (13);")

	mustBerth(t, repo, wt, "reset")
	mustBerth(t, repo, wt, "status", "--wait")

	if got := sqlite(t, berthDB, "PRAGMA integrity_check;"); got != "ok" {
		t.Errorf("the berth's %s after reset fails PRAGMA integrity_check:\n%s", rel, got)
	}
	if dump(t, berthDB) != dump(t, db) {
		t.Errorf("after reset the berth's %s differs from main's at the time of the reset", rel)
	}
}

// Reset's dry run, in text and JSON, names each file it would discard and changes none.
func TestSQLiteResetDryRunListsFilesAndChangesNothing(t *testing.T) {
	repo := NewRepo(t)
	ignoreSQLite(t, repo)
	files := []string{"database/app.sqlite", "storage/cache.db"}
	for _, rel := range files {
		makeLedger(t, filepath.Join(repo.Dir, rel), "delete", 50, 100)
	}
	writeFile(t, repo.privateConfig(), sqliteConfig(files...))
	wt := repo.AddWorktree(t, "feat-dry")
	mustBerth(t, repo, wt, "add", "--wait")
	want := map[string]string{}
	for _, rel := range files {
		sqlite(t, filepath.Join(wt, rel), "INSERT INTO ledger (amount) VALUES (7);")
		want[rel] = fileSum(t, filepath.Join(wt, rel))
	}

	for _, args := range [][]string{{"reset", "--dry-run"}, {"reset", "-n"}, {"reset", "--dry-run", "--json"}} {
		res := mustBerth(t, repo, wt, args...)
		if args[len(args)-1] == "--json" {
			var plan struct {
				Discarded []any `json:"discarded"`
			}
			if err := json.Unmarshal([]byte(res.Stdout), &plan); err != nil {
				t.Fatalf("berth %v: %v\n%s", args, err, res.Stdout)
			}
			for _, rel := range files {
				if !mentions(plan.Discarded, rel) {
					t.Errorf("berth %v does not list %s as discarded:\n%s", args, rel, res.Stdout)
				}
			}
		} else {
			for _, rel := range files {
				if !strings.Contains(res.Stdout, rel) {
					t.Errorf("berth %v does not name %s:\n%s", args, rel, res.Stdout)
				}
			}
		}
		for _, rel := range files {
			if got := fileSum(t, filepath.Join(wt, rel)); got != want[rel] {
				t.Errorf("berth %v changed the berth's %s", args, rel)
			}
		}
	}
}

// Reset itself reports each file it discarded, in text and in JSON.
func TestSQLiteResetReportsFiles(t *testing.T) {
	repo := NewRepo(t)
	ignoreSQLite(t, repo)
	rel := "database/app.sqlite"
	makeLedger(t, filepath.Join(repo.Dir, rel), "delete", 50, 100)
	writeFile(t, repo.privateConfig(), sqliteConfig(rel))
	wt := repo.AddWorktree(t, "feat-report")
	mustBerth(t, repo, wt, "add", "--wait")

	text := mustBerth(t, repo, wt, "reset")
	if !strings.Contains(text.Stdout, rel) {
		t.Errorf("berth reset does not name %s:\n%s", rel, text.Stdout)
	}
	mustBerth(t, repo, wt, "status", "--wait")

	res := mustBerth(t, repo, wt, "reset", "--json")
	var got struct {
		Discarded []any `json:"discarded"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &got); err != nil {
		t.Fatalf("berth reset --json: %v\n%s", err, res.Stdout)
	}
	if !mentions(got.Discarded, rel) {
		t.Errorf("berth reset --json does not list %s as discarded:\n%s", rel, res.Stdout)
	}
}

// Remove: remove deletes the berth's copies of the listed files and nothing
// else: not main's files, not another database file in the worktree, not a listed file
// the berth never got, and not another berth's copy.
func TestSQLiteRemoveDeletesOnlyTheCopies(t *testing.T) {
	repo := NewRepo(t)
	ignoreSQLite(t, repo)
	files := []string{"database/app.sqlite", "storage/cache.db"}
	for _, rel := range files {
		makeLedger(t, filepath.Join(repo.Dir, rel), "delete", 50, 100)
	}
	lacking := "database/later.sqlite"
	writeFile(t, repo.privateConfig(), sqliteConfig(append(files, lacking)...))
	mainSums := map[string]string{}
	for _, rel := range files {
		mainSums[rel] = fileSum(t, filepath.Join(repo.Dir, rel))
	}

	wt := repo.AddWorktree(t, "feat-rm")
	mustBerth(t, repo, wt, "add", "--wait")
	other := repo.AddWorktree(t, "feat-rm-2")
	mustBerth(t, repo, other, "add", "--wait")
	for _, rel := range files {
		if !fileExistsAt(filepath.Join(wt, rel)) {
			t.Fatalf("add did not clone %s into the berth", rel)
		}
	}
	unlisted := filepath.Join(wt, "database", "scratch.sqlite")
	makeLedger(t, unlisted, "delete", 5, 10)
	userMade := filepath.Join(wt, lacking)
	makeLedger(t, userMade, "delete", 5, 10)

	got := removeJSON(t, repo, wt)

	for _, rel := range files {
		if fileExistsAt(filepath.Join(wt, rel)) {
			t.Errorf("the berth's %s still exists after remove", rel)
		}
		reported := false
		for _, it := range got.Removed {
			if it.Name == rel || strings.HasSuffix(it.Name, string(filepath.Separator)+rel) {
				reported = true
			}
		}
		if !reported {
			t.Errorf("remove does not report deleting %s: %v", rel, got.items())
		}
		if sum := fileSum(t, filepath.Join(repo.Dir, rel)); sum != mainSums[rel] {
			t.Errorf("removing the berth changed main's %s", rel)
		}
		if !fileExistsAt(filepath.Join(other, rel)) {
			t.Errorf("removing one berth deleted the other berth's %s", rel)
		}
	}
	if !fileExistsAt(unlisted) {
		t.Errorf("remove deleted database/scratch.sqlite, which no config lists and berth never made")
	}
	if !fileExistsAt(userMade) {
		t.Errorf("remove deleted %s, which main lacked so berth never made it", lacking)
	}
}

// A listed file main lacks is reported by add, and the berth gets none.
func TestSQLiteFileMainLacksIsReported(t *testing.T) {
	repo := NewRepo(t)
	ignoreSQLite(t, repo)
	present, lacking := "database/app.sqlite", "database/missing.sqlite"
	makeLedger(t, filepath.Join(repo.Dir, present), "delete", 20, 10)
	writeFile(t, repo.privateConfig(), sqliteConfig(present, lacking))

	wt := repo.AddWorktree(t, "feat-lacks")
	res := mustBerth(t, repo, wt, "add", "--wait")

	if !strings.Contains(res.Stdout+res.Stderr, lacking) {
		t.Errorf("add does not report that main has no %s:\nstdout:\n%s\nstderr:\n%s", lacking, res.Stdout, res.Stderr)
	}
	if _, err := os.Lstat(filepath.Join(wt, lacking)); !os.IsNotExist(err) {
		t.Errorf("the berth has a %s though main has none (err %v)", lacking, err)
	}
}

// Status lists each listed SQLite file, in text and in JSON.
func TestSQLiteStatusListsEachFile(t *testing.T) {
	repo := NewRepo(t)
	ignoreSQLite(t, repo)
	files := []string{"database/app.sqlite", "storage/cache.db"}
	for _, rel := range files {
		makeLedger(t, filepath.Join(repo.Dir, rel), "delete", 20, 10)
	}
	writeFile(t, repo.privateConfig(), sqliteConfig(files...))
	wt := repo.AddWorktree(t, "feat-status")
	mustBerth(t, repo, wt, "add", "--wait")

	text := mustBerth(t, repo, wt, "status")
	res := mustBerth(t, repo, wt, "status", "--json")
	var s struct {
		State []any `json:"state"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &s); err != nil {
		t.Fatalf("berth status --json: %v\n%s", err, res.Stdout)
	}
	for _, rel := range files {
		if !strings.Contains(text.Stdout, rel) {
			t.Errorf("berth status does not list %s:\n%s", rel, text.Stdout)
		}
		if !mentions(s.State, rel) {
			t.Errorf("berth status --json lists no state for %s:\n%s", rel, res.Stdout)
		}
	}
}
