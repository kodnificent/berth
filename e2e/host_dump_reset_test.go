//go:build e2e

package e2e

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// writeBerthRows changes the berth's copy of central so a reset that leaves it in place
// shows.
func writeBerthRows(t *testing.T, m *hostMySQL, prefix string) {
	t.Helper()
	db := "`" + prefix + "central`"
	m.sql(t, "INSERT INTO "+db+".kept VALUES ('berth', 9); DELETE FROM "+db+".orders WHERE id <= 100; CREATE TABLE "+db+".berth_only (id INT PRIMARY KEY)")
}

// Reset drops the berth's dump databases and copies them again from main as
// main is at the time of the reset, keeping the berth's prefix, names and ports.
func TestResetRecopiesDumpDatabasesFromMain(t *testing.T) {
	repo, m := dumpServer(t)
	wt := repo.AddWorktree(t, "feat-reset")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)
	before := identityOf(t, repo, wt)
	envBefore := blockEnv(t, filepath.Join(wt, ".env"))
	writeBerthRows(t, m, p)
	// Main moves on after the berth was made, so a reset to main as it was at add fails.
	m.sql(t, "INSERT INTO app_central.kept VALUES ('main', 4); UPDATE app_reports.daily SET total = total + 1 WHERE day < '2024-02-01'")
	want := map[string]string{"central": m.contents(t, "app_central"), "reports": m.contents(t, "app_reports")}

	mustBerth(t, repo, wt, "reset")
	mustBerth(t, repo, wt, "status", "--wait")

	for key, sum := range want {
		if e := dbState(t, repo, nil, wt, key); e.Status != "ready" || e.Target != p+key {
			t.Errorf("database %s after reset is %q in %q, want ready in %s%s: %+v", key, e.Status, e.Target, p, key, e)
		}
		if got := m.contents(t, p+key); got != sum {
			t.Errorf("after reset the berth's %s%s differs from main's app_%s at the time of the reset:\nmain:\n%s\nberth:\n%s", p, key, key, sum, got)
		}
	}
	if got := berthPrefix(t, wt); got != p {
		t.Errorf("reset changed the berth's prefix from %q to %q", p, got)
	}
	envAfter := blockEnv(t, filepath.Join(wt, ".env"))
	for _, k := range []string{"DB_DATABASE", "DB_DATABASE_PREFIX"} {
		if envAfter[k] != envBefore[k] {
			t.Errorf("reset changed %s in the managed block from %q to %q", k, envBefore[k], envAfter[k])
		}
	}
	after := identityOf(t, repo, wt)
	if after.ID != before.ID || after.Name != before.Name || after.Host != before.Host || string(after.Ports) != string(before.Ports) {
		t.Errorf("reset changed the berth's identity or ports:\nbefore: %+v %s\nafter:  %+v %s", before, before.Ports, after, after.Ports)
	}
	if under := strings.Join(m.under(t, p), ","); under != p+"1,"+p+"central,"+p+"reports" {
		t.Errorf("after reset, databases under %s = %q, want the berth's central, reports and 1", p, under)
	}
}

// Reset leaves main's databases as they were.
func TestResetOfDumpDatabasesLeavesMainUnchanged(t *testing.T) {
	repo, m := dumpServer(t)
	wt := repo.AddWorktree(t, "feat-source")
	mustBerth(t, repo, wt, "add", "--wait")
	writeBerthRows(t, m, berthPrefix(t, wt))
	main := []string{"app_central", "app_reports", "app_1", "app_2", "shop"}
	before := map[string]string{}
	for _, db := range main {
		before[db] = m.contents(t, db)
	}

	mustBerth(t, repo, wt, "reset")
	mustBerth(t, repo, wt, "status", "--wait")

	for _, db := range main {
		if got := m.contents(t, db); got != before[db] {
			t.Errorf("reset changed main's %s:\nbefore:\n%s\nafter:\n%s", db, before[db], got)
		}
	}
}

// kill -9 during reset's copy reads as failed naming berth reset,
// never ready, and reset again leaves the database matching main's.
func TestKilledDumpResetFailsAndResetRecovers(t *testing.T) {
	repo, m := dumpServer(t)
	want := m.contents(t, "app_central")
	wt := repo.AddWorktree(t, "feat-kill-reset")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)
	writeBerthRows(t, m, p)

	if res := berthWithDelay(t, repo, wt, "120s", "reset"); res.Exit != 0 {
		t.Fatalf("berth reset: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	var pid int
	waitFor(t, 30*time.Second, "reset's copy to start", func() bool {
		e := dbState(t, repo, nil, wt, "central")
		if e.Status == "ready" {
			t.Fatalf("central reported ready while reset's copy runs: %+v", e)
		}
		pid = e.PID
		return e.Status == "cloning" && pid > 0
	})
	if err := killNow(pid); err != nil {
		t.Fatalf("kill -9 %d: %v", pid, err)
	}
	var e dbClone
	waitFor(t, 10*time.Second, "status to report the killed reset as failed", func() bool {
		e = dbState(t, repo, nil, wt, "central")
		if e.Status == "ready" {
			t.Fatalf("central reported ready after reset's copy was killed mid-way: %+v", e)
		}
		return e.Status == "failed"
	})
	if !strings.Contains(e.Error, "berth reset") {
		t.Errorf("the killed reset's failure says %q, want recovery text naming berth reset", e.Error)
	}

	mustBerth(t, repo, wt, "reset")
	mustBerth(t, repo, wt, "status", "--wait")
	if e := dbState(t, repo, nil, wt, "central"); e.Status != "ready" {
		t.Fatalf("after reset again, central is %q, want ready: %+v", e.Status, e)
	}
	if got := m.contents(t, p+"central"); got != want {
		t.Errorf("after recovery the berth's central differs from main's:\nmain:\n%s\nberth:\n%s", want, got)
	}
}

// A fresh database gets none of main's tables from add or reset, a skip
// database gets no copy at all, and neither source changes.
func TestFreshAndSkipDatabasesAreNotCopied(t *testing.T) {
	repo, m := dumpServer(t)
	sources := map[string]string{"app_1": m.contents(t, "app_1"), "app_2": m.contents(t, "app_2")}
	wt := repo.AddWorktree(t, "feat-mixed")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)

	expect := func(when string) {
		t.Helper()
		if !m.has(t, p+"1") {
			t.Errorf("%s, the fresh database %s1 is missing: %v", when, p, m.databases(t))
		} else if n := m.tables(t, p+"1"); n != "0" {
			t.Errorf("%s, the fresh database %s1 has %s tables, want none of main's app_1", when, p, n)
		}
		if e := dbState(t, repo, nil, wt, "1"); e.Strategy != "fresh" || e.Status != "ready" {
			t.Errorf("%s, database 1 has strategy %q status %q, want fresh ready", when, e.Strategy, e.Status)
		}
		if m.has(t, p+"2") {
			t.Errorf("%s, the skipped app_2 has a berth copy %s2", when, p)
		}
		for db, sum := range sources {
			if got := m.contents(t, db); got != sum {
				t.Errorf("%s, main's %s changed:\nbefore:\n%s\nafter:\n%s", when, db, sum, got)
			}
		}
	}
	expect("after add")

	mustBerth(t, repo, wt, "reset")
	mustBerth(t, repo, wt, "status", "--wait")
	expect("after reset")
}

// In a project with no clone volumes, reset --dry-run in text and JSON
// lists the berth's databases and touches none, and reset --json lists what it discarded.
func TestResetListsDatabasesWithNoVolumes(t *testing.T) {
	repo, m := dumpServer(t)
	wt := repo.AddWorktree(t, "feat-list")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)
	writeBerthRows(t, m, p)
	changed := m.contents(t, p+"central")
	want := []string{"database " + p + "1", "database " + p + "central", "database " + p + "reports"}

	discarded := func(args ...string) []string {
		t.Helper()
		res := mustBerth(t, repo, wt, args...)
		var got struct {
			Discarded []struct{ Kind, Name string } `json:"discarded"`
		}
		if err := json.Unmarshal([]byte(res.Stdout), &got); err != nil {
			t.Fatalf("berth %v: %v\n%s", args, err, res.Stdout)
		}
		var out []string
		for _, d := range got.Discarded {
			out = append(out, d.Kind+" "+d.Name)
		}
		slices.Sort(out)
		return out
	}

	for _, args := range [][]string{{"reset", "--dry-run"}, {"reset", "-n"}} {
		res := mustBerth(t, repo, wt, args...)
		for _, db := range []string{p + "1", p + "central", p + "reports"} {
			if !strings.Contains(res.Stdout, db) {
				t.Errorf("berth %v doesn't list the berth's database %s:\n%s", args, db, res.Stdout)
			}
		}
	}
	if got := discarded("reset", "--dry-run", "--json"); !slices.Equal(got, want) {
		t.Errorf("reset --dry-run --json lists %v, want %v", got, want)
	}
	if got := m.contents(t, p+"central"); got != changed {
		t.Errorf("reset --dry-run changed the berth's central:\nbefore:\n%s\nafter:\n%s", changed, got)
	}

	if got := discarded("reset", "--json"); !slices.Equal(got, want) {
		t.Errorf("reset --json lists %v as discarded, want %v", got, want)
	}
	mustBerth(t, repo, wt, "status", "--wait")
	if got, main := m.contents(t, p+"central"), m.contents(t, "app_central"); got != main {
		t.Errorf("after reset the berth's central differs from main's:\nmain:\n%s\nberth:\n%s", main, got)
	}
}
