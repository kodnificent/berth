//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func snapshotServer(t *testing.T) (*Repo, *hostMySQL) {
	t.Helper()
	repo, m := dumpServer(t)
	writeFile(t, repo.privateConfig(), snapshotConfig())
	return repo, m
}

func snapshotConfig() string {
	return seedConfig(`central = "snapshot"`, `reports = "snapshot"`, `"1" = "fresh"`)
}

func mainContents(t *testing.T, m *hostMySQL) map[string]string {
	t.Helper()
	return map[string]string{"central": m.contents(t, "app_central"), "reports": m.contents(t, "app_reports")}
}

// changeMain moves main on in both snapshot databases, with key unique per call.
func changeMain(t *testing.T, m *hostMySQL, key string) {
	t.Helper()
	m.sql(t, "INSERT INTO app_central.kept VALUES ('"+key+"', 4); UPDATE app_reports.daily SET total = total + 1 WHERE day < '2024-02-01'")
}

func expectFromSnapshot(t *testing.T, repo *Repo, m *hostMySQL, wt string, want map[string]string, when string) {
	t.Helper()
	p := berthPrefix(t, wt)
	for key, sum := range want {
		if e := dbState(t, repo, nil, wt, key); e.Status != "ready" || e.Strategy != "snapshot" || e.Target != p+key {
			t.Errorf("%s, database %s: status %q strategy %q target %q, want ready snapshot in %s%s", when, key, e.Status, e.Strategy, e.Target, p, key)
		}
		if got := m.contents(t, p+key); got != sum {
			t.Errorf("%s, the berth's %s%s doesn't hold the snapshot's data:\nsnapshot:\n%s\nberth:\n%s", when, p, key, sum, got)
		}
	}
}

func expectNoSnapshotFailure(t *testing.T, repo *Repo, wt, key string, res Result, when string) {
	t.Helper()
	if res.Exit != 4 {
		t.Errorf("%s, berth add --wait exited %d, want 4 (state clone failed)\n%s%s", when, res.Exit, res.Stdout, res.Stderr)
	}
	e := dbState(t, repo, nil, wt, key)
	if e.Status != "failed" {
		t.Errorf("%s, database %s is %q, want failed: %+v", when, key, e.Status, e)
	}
	if !strings.Contains(e.Error, "berth snapshot") {
		t.Errorf("%s, database %s's failure says %q, want it to name berth snapshot", when, key, e.Error)
	}
}

func TestBerthHoldsMainsDataAtTheSnapshotNotAtAdd(t *testing.T) {
	repo, m := snapshotServer(t)
	mustBerth(t, repo, repo.Dir, "snapshot")
	want := mainContents(t, m)
	changeMain(t, m, "after-snapshot")
	wt := repo.AddWorktree(t, "feat-snap")

	mustBerth(t, repo, wt, "add", "--wait")

	expectFromSnapshot(t, repo, m, wt, want, "after add --wait")
}

func TestChangesToMainReachTheBerthOnlyAfterTheNextSnapshotAndReset(t *testing.T) {
	repo, m := snapshotServer(t)
	mustBerth(t, repo, repo.Dir, "snapshot")
	first := mainContents(t, m)
	wt := repo.AddWorktree(t, "feat-refresh")
	mustBerth(t, repo, wt, "add", "--wait")
	changeMain(t, m, "after-first")
	expectFromSnapshot(t, repo, m, wt, first, "after main changed")

	writeBerthRows(t, m, berthPrefix(t, wt))
	mustBerth(t, repo, wt, "reset")
	mustBerth(t, repo, wt, "status", "--wait")
	expectFromSnapshot(t, repo, m, wt, first, "after reset with no new snapshot")

	mustBerth(t, repo, repo.Dir, "snapshot")
	second := mainContents(t, m)
	expectFromSnapshot(t, repo, m, wt, first, "after the next snapshot and before reset")

	mustBerth(t, repo, wt, "reset")
	mustBerth(t, repo, wt, "status", "--wait")
	expectFromSnapshot(t, repo, m, wt, second, "after the next snapshot and reset")
}

// An age of at least a minute and at most a few, in the forms a duration is usually
// written, since "just now" is an honest age for anything younger.
var minuteOldAge = regexp.MustCompile(`(?i)\b([1-4]\s*m(in(ute)?s?)?(\s*\d+\s*s)?|([6-9]\d|[12]\d\d)\s*s(ec(ond)?s?)?|an? minute)\b`)

func TestStatusShowsTheSnapshotsAgeForEachSnapshotDatabase(t *testing.T) {
	repo, _ := snapshotServer(t)
	mustBerth(t, repo, repo.Dir, "snapshot")
	// The berth is made after this wait, so an age counted from add reads under a minute.
	time.Sleep(65 * time.Second)
	wt := repo.AddWorktree(t, "feat-age")
	mustBerth(t, repo, wt, "add", "--wait")

	lineOf := func(human, key string) string {
		return regexp.MustCompile(`(?m)^.*database \S*` + key + `\b.*$`).FindString(human)
	}
	human := mustBerth(t, repo, wt, "status").Stdout
	for _, key := range []string{"central", "reports"} {
		if line := lineOf(human, key); !minuteOldAge.MatchString(line) {
			t.Errorf("status shows no age of about a minute for snapshot database %s, line %q:\n%s", key, line, human)
		}
	}
	if line := lineOf(human, "1"); minuteOldAge.MatchString(line) {
		t.Errorf("status shows a snapshot age for the fresh database, line %q", line)
	}

	raw := mustBerth(t, repo, wt, "status", "--json").Stdout
	var s struct {
		State []map[string]any `json:"state"`
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("berth status --json: %v\n%s", err, raw)
	}
	// The JSON schema names no field for the age, so any non-empty field about it will do.
	hasAge := func(entry map[string]any) bool {
		for k, v := range entry {
			if strings.Contains(k, "snapshot") || k == "age" || strings.HasPrefix(k, "age_") || strings.HasSuffix(k, "_age") {
				if v != nil && !reflect.ValueOf(v).IsZero() {
					return true
				}
			}
		}
		return false
	}
	seen := map[string]bool{}
	for _, e := range s.State {
		if e["kind"] != "database" {
			continue
		}
		name, _ := e["name"].(string)
		seen[name] = hasAge(e)
	}
	if !seen["central"] || !seen["reports"] {
		t.Errorf("status --json gives no snapshot age for every snapshot database, have %v:\n%s", seen, raw)
	}
	if seen["1"] {
		t.Errorf("status --json gives a snapshot age for the fresh database:\n%s", raw)
	}
}

func TestAddWithNoSnapshotFailsThatDatabaseNamingBerthSnapshot(t *testing.T) {
	repo, m := dumpServer(t)
	writeFile(t, repo.privateConfig(), seedConfig(`central = "snapshot"`, `reports = "dump"`, `"1" = "fresh"`))
	wantDump := m.contents(t, "app_reports")
	wt := repo.AddWorktree(t, "feat-none")

	expectNoSnapshotFailure(t, repo, wt, "central", repo.Berth(t, wt, "add", "--wait"), "with no snapshot yet")

	if e := dbState(t, repo, nil, wt, "reports"); e.Status != "ready" {
		t.Errorf("the dump beside the failed snapshot database is %q, want ready: %+v", e.Status, e)
	}
	if got := m.contents(t, berthPrefix(t, wt)+"reports"); got != wantDump {
		t.Errorf("the dump beside the failed snapshot database differs from main's:\nmain:\n%s\nberth:\n%s", wantDump, got)
	}

	mustBerth(t, repo, repo.Dir, "snapshot")
	want := map[string]string{"central": m.contents(t, "app_central")}
	mustBerth(t, repo, wt, "add", "--wait")
	expectFromSnapshot(t, repo, m, wt, want, "after following the message and adding again")
}

func TestDatabaseConfiguredAfterTheSnapshotFailsNamingBerthSnapshot(t *testing.T) {
	repo, m := dumpServer(t)
	writeFile(t, repo.privateConfig(), seedConfig(`central = "snapshot"`))
	mustBerth(t, repo, repo.Dir, "snapshot")
	want := map[string]string{"central": m.contents(t, "app_central")}
	writeFile(t, repo.privateConfig(), seedConfig(`central = "snapshot"`, `reports = "snapshot"`))
	wt := repo.AddWorktree(t, "feat-later")

	expectNoSnapshotFailure(t, repo, wt, "reports", repo.Berth(t, wt, "add", "--wait"), "with reports configured after the snapshot")

	expectFromSnapshot(t, repo, m, wt, want, "beside the database with no snapshot")
}

func TestSnapshotOutsideMainExitsOneAndSaysToRunItInMain(t *testing.T) {
	repo, _ := snapshotServer(t)
	wt := repo.AddWorktree(t, "feat-outside")
	refused := func(when string) {
		t.Helper()
		for _, args := range [][]string{{"snapshot"}, {"snapshot", "--json"}} {
			res := repo.Berth(t, wt, args...)
			if res.Exit != 1 {
				t.Errorf("%s, berth %v exited %d, want 1\n%s%s", when, args, res.Exit, res.Stdout, res.Stderr)
			}
			if !strings.Contains(res.Stdout+res.Stderr, "main") {
				t.Errorf("%s, berth %v doesn't say to run it in main:\n%s%s", when, args, res.Stdout, res.Stderr)
			}
		}
	}

	refused("in a worktree with no berth")
	expectNoSnapshotFailure(t, repo, wt, "central", repo.Berth(t, wt, "add", "--wait"), "after the refused snapshot")
	refused("in a worktree with a berth")
	expectNoSnapshotFailure(t, repo, wt, "central", repo.Berth(t, wt, "add", "--wait"), "after the second refused snapshot")
}

func TestSnapshotDryRunListsTheDatabasesAndTakesNothing(t *testing.T) {
	repo, m := snapshotServer(t)
	databases := m.databases(t)
	before := mainContents(t, m)

	for _, args := range [][]string{{"snapshot", "--dry-run"}, {"snapshot", "-n"}, {"snapshot", "--dry-run", "--json"}} {
		res := mustBerth(t, repo, repo.Dir, args...)
		for _, db := range []string{"central", "reports"} {
			if !strings.Contains(res.Stdout, db) {
				t.Errorf("berth %v doesn't list %s:\n%s", args, db, res.Stdout)
			}
		}
		for _, db := range []string{"app_1", "app_2", "shop"} {
			if strings.Contains(res.Stdout, db) {
				t.Errorf("berth %v lists %s, which isn't configured as snapshot:\n%s", args, db, res.Stdout)
			}
		}
	}
	if res := mustBerth(t, repo, repo.Dir, "snapshot", "--dry-run", "--json"); !json.Valid([]byte(res.Stdout)) {
		t.Errorf("berth snapshot --dry-run --json isn't JSON:\n%s", res.Stdout)
	}

	if got := m.databases(t); !reflect.DeepEqual(got, databases) {
		t.Errorf("the dry run changed the server's databases from %v to %v", databases, got)
	}
	if got := mainContents(t, m); !reflect.DeepEqual(got, before) {
		t.Errorf("the dry run changed main's data:\nbefore:\n%v\nafter:\n%v", before, got)
	}
	wt := repo.AddWorktree(t, "feat-dry")
	expectNoSnapshotFailure(t, repo, wt, "central", repo.Berth(t, wt, "add", "--wait"), "after only a dry run")
}

func TestSnapshotDryRunLeavesTheExistingSnapshotInPlace(t *testing.T) {
	repo, m := snapshotServer(t)
	mustBerth(t, repo, repo.Dir, "snapshot")
	want := mainContents(t, m)
	changeMain(t, m, "after-snapshot")

	mustBerth(t, repo, repo.Dir, "snapshot", "--dry-run")

	wt := repo.AddWorktree(t, "feat-dry-kept")
	mustBerth(t, repo, wt, "add", "--wait")
	expectFromSnapshot(t, repo, m, wt, want, "after a dry run with main changed")
}

func stateBytes(t *testing.T, repo *Repo) int64 {
	t.Helper()
	var total int64
	err := filepath.WalkDir(repo.StateDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return total
}

// Far above a lock file or a record, far below what the seeded central writes, even compressed.
const partialSnapshotBytes = 64 << 10

// killSnapshotPartway kills berth snapshot once the snapshot has written data. It relies
// on berth snapshot honouring BERTH_TEST_CLONE_DELAY as the clone process does: waiting
// once a database's data is written and before that data replaces the previous snapshot.
func killSnapshotPartway(t *testing.T, repo *Repo) {
	t.Helper()
	before := stateBytes(t, repo)
	cmd := exec.Command(berthBin, "snapshot")
	cmd.Dir, cmd.Env = repo.Dir, append(repo.Env(), cloneDelay+"=120s")
	ownGroup(cmd)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// The whole group, so a helper the command started can't finish the snapshot after it.
	killGroup := func() error { return killGroupOf(cmd) }
	t.Cleanup(func() { _ = killGroup() })
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()

	waitFor(t, time.Minute, "berth snapshot to write part of the new snapshot", func() bool {
		select {
		case <-done:
			t.Fatalf("berth snapshot exited %d before it could be killed partway; it must honour %s:\n%s", cmd.ProcessState.ExitCode(), cloneDelay, out.String())
		default:
		}
		return stateBytes(t, repo) >= before+partialSnapshotBytes
	})
	if err := killGroup(); err != nil {
		t.Fatalf("kill -9 berth snapshot: %v", err)
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("berth snapshot still running 30s after kill -9:\n%s", out.String())
	}
}

func TestKilledSnapshotLeavesThePreviousUsableAndTheNextSnapshotRecovers(t *testing.T) {
	repo, m := snapshotServer(t)
	mustBerth(t, repo, repo.Dir, "snapshot")
	previous := mainContents(t, m)
	changeMain(t, m, "after-previous")

	killSnapshotPartway(t, repo)

	before := repo.AddWorktree(t, "feat-after-kill")
	mustBerth(t, repo, before, "add", "--wait")
	expectFromSnapshot(t, repo, m, before, previous, "after a snapshot was killed partway")

	mustBerth(t, repo, repo.Dir, "snapshot")
	current := mainContents(t, m)
	after := repo.AddWorktree(t, "feat-recovered")
	mustBerth(t, repo, after, "add", "--wait")
	expectFromSnapshot(t, repo, m, after, current, "after the next snapshot")
}

func TestKilledFirstSnapshotLeavesNothingToCloneAndTheNextSnapshotRecovers(t *testing.T) {
	repo, m := snapshotServer(t)

	killSnapshotPartway(t, repo)

	wt := repo.AddWorktree(t, "feat-torn")
	expectNoSnapshotFailure(t, repo, wt, "central", repo.Berth(t, wt, "add", "--wait"), "after the first snapshot was killed partway")

	mustBerth(t, repo, repo.Dir, "snapshot")
	want := mainContents(t, m)
	mustBerth(t, repo, wt, "add", "--wait")
	expectFromSnapshot(t, repo, m, wt, want, "after the next snapshot")
}

func TestRemovingTheLastBerthKeepsTheSnapshot(t *testing.T) {
	repo, m := snapshotServer(t)
	mustBerth(t, repo, repo.Dir, "snapshot")
	want := mainContents(t, m)
	wt := repo.AddWorktree(t, "feat-gone")
	mustBerth(t, repo, wt, "add", "--wait")
	changeMain(t, m, "after-snapshot")

	mustBerth(t, repo, wt, "remove")

	next := repo.AddWorktree(t, "feat-next")
	mustBerth(t, repo, next, "add", "--wait")
	expectFromSnapshot(t, repo, m, next, want, "after the only berth was removed")
}

func TestPruneKeepsTheSnapshot(t *testing.T) {
	repo, m := snapshotServer(t)
	mustBerth(t, repo, repo.Dir, "snapshot")
	want := mainContents(t, m)
	wt := repo.AddWorktree(t, "feat-orphan")
	mustBerth(t, repo, wt, "add", "--wait")
	changeMain(t, m, "after-snapshot")
	repo.Git(t, "worktree", "remove", "--force", wt)

	if res := repo.BerthWith(t, []string{"DB_USERNAME=" + m.User, "DB_PASSWORD=" + m.Password}, repo.Dir, "prune"); res.Exit != 0 {
		t.Fatalf("berth prune: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}

	next := repo.AddWorktree(t, "feat-next")
	mustBerth(t, repo, next, "add", "--wait")
	expectFromSnapshot(t, repo, m, next, want, "after the only berth was pruned")
}

func TestSnapshotsAreKeptPerRepository(t *testing.T) {
	repo, m := snapshotServer(t)
	other := NewRepo(t)
	other.ShareState(repo)
	pointMainAt(t, other, m, "DB_DATABASE_PREFIX=app_", "DB_DATABASE=app_central")
	writeFile(t, other.privateConfig(), snapshotConfig())

	mustBerth(t, repo, repo.Dir, "snapshot")
	wantRepo := mainContents(t, m)
	changeMain(t, m, "between-repositories")
	mustBerth(t, other, other.Dir, "snapshot")
	wantOther := mainContents(t, m)

	wt := repo.AddWorktree(t, "feat-first")
	mustBerth(t, repo, wt, "add", "--wait")
	expectFromSnapshot(t, repo, m, wt, wantRepo, "in the repository snapshotted first")
	otherWt := other.AddWorktree(t, "feat-second")
	mustBerth(t, other, otherWt, "add", "--wait")
	expectFromSnapshot(t, other, m, otherWt, wantOther, "in the repository snapshotted second")
}

func TestSnapshotAddsNothingToTheMainWorktree(t *testing.T) {
	repo, _ := snapshotServer(t)
	files := func() []string {
		var out []string
		err := filepath.WalkDir(repo.Dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == ".git" {
				return filepath.SkipDir
			}
			out = append(out, path)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := files()

	mustBerth(t, repo, repo.Dir, "snapshot")

	if got := files(); !reflect.DeepEqual(got, before) {
		t.Errorf("berth snapshot changed the main worktree's files:\nbefore: %v\nafter:  %v", before, got)
	}
}

func TestSnapshotUnderWriteLoadHoldsWholeTransactionsAndMainKeepsWriting(t *testing.T) {
	repo, m := snapshotServer(t)
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
	started := atoi(t, m.sql(t, "SELECT MAX(id) FROM app_central.a"))

	mustBerth(t, repo, repo.Dir, "snapshot")

	if after := atoi(t, m.sql(t, "SELECT MAX(id) FROM app_central.a")); after <= started {
		t.Errorf("main stopped taking writes during the snapshot: max id %d, %d before it", after, started)
	}
	wt := repo.AddWorktree(t, "feat-load")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)
	m.checkTables(t, p+"central")
	a, b := count(p+"central", "a"), count(p+"central", "b")
	if a == 0 || a != b {
		t.Errorf("the berth holds %d rows in a and %d in b; every transaction wrote one to each, so a torn snapshot shows as a mismatch", a, b)
	}
}

func TestAddFromASnapshotFinishesWhileMainsTablesAreLocked(t *testing.T) {
	repo, m := snapshotServer(t)
	mustBerth(t, repo, repo.Dir, "snapshot")
	want := mainContents(t, m)
	lock := m.command("LOCK TABLES app_central.users WRITE, app_reports.daily WRITE; SELECT SLEEP(600)")
	if err := lock.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Process.Kill(); _ = lock.Wait() })
	waitFor(t, time.Minute, "main's tables to be locked", func() bool {
		return m.sql(t, "SHOW OPEN TABLES WHERE In_use > 0 AND `Database` IN ('app_central', 'app_reports')") != ""
	})
	wt := repo.AddWorktree(t, "feat-locked")

	// A clone that reads main's tables would wait on the lock for as long as it is held.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	add := exec.CommandContext(ctx, berthBin, "add", "--wait")
	add.Dir, add.Env = wt, repo.Env()
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("berth add --wait with main's tables locked: %v (timed out: %v)\n%s", err, ctx.Err() != nil, out)
	}

	expectFromSnapshot(t, repo, m, wt, want, "with main's tables locked throughout")
}
