//go:build e2e

package e2e

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSnapshotNamesAHeldTableBeforeHoldingItAndKeepsItsRows(t *testing.T) {
	repo, m := snapshotServer(t)
	m.sql(t, "CREATE TABLE app_central.legacy_log (id BIGINT PRIMARY KEY AUTO_INCREMENT, line VARCHAR(100)) ENGINE=MyISAM; INSERT INTO app_central.legacy_log (line) VALUES ('first'), ('second')")
	want := mainContents(t, m)

	res := mustBerth(t, repo, repo.Dir, "snapshot")

	if !strings.Contains(res.Stderr, "legacy_log") || !strings.Contains(res.Stderr, "MyISAM") || !strings.Contains(res.Stderr, "pause") {
		t.Errorf("berth snapshot didn't name the table whose writes it holds:\n%s", res.Stderr)
	}
	wt := repo.AddWorktree(t, "feat-held")
	mustBerth(t, repo, wt, "add", "--wait")
	expectFromSnapshot(t, repo, m, wt, want, "after a snapshot that held a MyISAM table")
}

func TestSnapshotLeavesOutWhatReachesAnotherDatabaseAndNamesIt(t *testing.T) {
	repo, m := snapshotServer(t)
	m.sql(t, `CREATE TABLE shop.audit (id INT PRIMARY KEY AUTO_INCREMENT, what VARCHAR(20));
CREATE TRIGGER app_central.reaches_shop AFTER INSERT ON app_central.kept FOR EACH ROW INSERT INTO shop.audit (what) VALUES (NEW.k);
CREATE VIEW app_central.shop_items AS SELECT id FROM shop.items;
CREATE TABLE app_central.child (id INT PRIMARY KEY, item INT, CONSTRAINT to_shop FOREIGN KEY (item) REFERENCES shop.items (id));
CREATE TABLE shop.entries (id INT PRIMARY KEY) ENGINE=MyISAM;
INSERT INTO shop.entries VALUES (1);
CREATE TABLE app_central.merged (id INT PRIMARY KEY) ENGINE=MRG_MyISAM UNION=(shop.entries)`)

	res := mustBerth(t, repo, repo.Dir, "snapshot")

	for _, name := range []string{"reaches_shop", "shop_items", "merged", "child"} {
		if !strings.Contains(res.Stdout, name) {
			t.Errorf("berth snapshot doesn't report %s as left out:\n%s", name, res.Stdout)
		}
	}
	wt := repo.AddWorktree(t, "feat-guards")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)
	notes := notesOf(t, repo, wt, "central")
	for _, name := range []string{"reaches_shop", "shop_items", "merged", "child"} {
		if !strings.Contains(notes, name) {
			t.Errorf("status notes don't name %s:\n%s", name, notes)
		}
	}
	m.sql(t, "INSERT INTO `"+p+"central`.kept VALUES ('berth', 7)")
	if n := m.sql(t, "SELECT COUNT(*) FROM shop.audit"); n != "0" {
		t.Errorf("a write in the berth added %s rows to shop.audit, want none", n)
	}
	if n := m.sql(t, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = '"+p+"central' AND table_name IN ('shop_items', 'merged')"); n != "0" {
		t.Errorf("the berth has %s of the view and MERGE table that reach shop, want none", n)
	}
	if n := m.sql(t, "SELECT COUNT(*) FROM information_schema.referential_constraints WHERE constraint_schema = '"+p+"central' AND table_name = 'child'"); n != "0" {
		t.Errorf("the berth's child table kept %s foreign keys into shop, want none", n)
	}
}

func TestSnapshotRestoresRowsLargerThanThirtyTwoMegabytes(t *testing.T) {
	repo, m := snapshotServer(t)
	m.sql(t, `CREATE TABLE app_reports.files (id INT PRIMARY KEY, body LONGBLOB, caption LONGTEXT CHARACTER SET utf8mb4);
INSERT INTO app_reports.files VALUES (1, REPEAT(UNHEX(SHA2('berth', 256)), 34 * 1024 * 1024 / 32), REPEAT('é😀x', 2 * 1024 * 1024)), (2, 'small', 'small')`)
	want := map[string]string{"reports": m.contents(t, "app_reports")}
	mustBerth(t, repo, repo.Dir, "snapshot")
	wt := repo.AddWorktree(t, "feat-large-snap")

	mustBerth(t, repo, wt, "add", "--wait")

	expectFromSnapshot(t, repo, m, wt, want, "after restoring rows over 32 MB")
}

func TestSnapshotFilesArePrivateAndHoldNoLogin(t *testing.T) {
	repo, m := snapshotServer(t)

	mustBerth(t, repo, repo.Dir, "snapshot")

	root := filepath.Join(repo.StateDir, "snapshots")
	seen := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !d.IsDir() {
			seen++
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(data), m.Password) || strings.Contains(string(data), m.User) {
				t.Errorf("%s contains the database login", path)
			}
		}
		expectPrivate(t, path, info)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen == 0 {
		t.Errorf("berth snapshot wrote no files under %s", root)
	}
}

func writeBerthRowsTo(t *testing.T, m *hostMySQL, db string) {
	t.Helper()
	m.sql(t, "CREATE TABLE `"+db+"`.marker (id INT PRIMARY KEY); INSERT INTO `"+db+"`.marker VALUES (1)")
}

func hasMarker(t *testing.T, m *hostMySQL, db string) bool {
	t.Helper()
	return m.sql(t, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = '"+db+"' AND table_name = 'marker'") == "1"
}

func TestResetRefusesBeforeDroppingWhenThereIsNoSnapshot(t *testing.T) {
	repo, m := snapshotServer(t)
	wt := repo.AddWorktree(t, "feat-no-snap")
	repo.Berth(t, wt, "add", "--wait")
	fresh := berthPrefix(t, wt) + "1"
	writeBerthRowsTo(t, m, fresh)

	for _, args := range [][]string{{"reset"}, {"reset", "--dry-run"}} {
		res := repo.Berth(t, wt, args...)
		if res.Exit != 1 {
			t.Errorf("berth %v with no snapshot exited %d, want 1\n%s%s", args, res.Exit, res.Stdout, res.Stderr)
		}
		if !strings.Contains(res.Stdout+res.Stderr, "berth snapshot") {
			t.Errorf("berth %v doesn't name berth snapshot:\n%s%s", args, res.Stdout, res.Stderr)
		}
	}

	if !hasMarker(t, m, fresh) {
		t.Errorf("reset dropped the berth's fresh database %s before refusing", fresh)
	}
}

func TestResetRefusesBeforeDroppingWhenTheSnapshotLacksADatabase(t *testing.T) {
	repo, m := dumpServer(t)
	writeFile(t, repo.privateConfig(), seedConfig(`central = "snapshot"`, `"1" = "fresh"`))
	mustBerth(t, repo, repo.Dir, "snapshot")
	writeFile(t, repo.privateConfig(), seedConfig(`central = "snapshot"`, `reports = "snapshot"`, `"1" = "fresh"`))
	wt := repo.AddWorktree(t, "feat-lacking")
	repo.Berth(t, wt, "add", "--wait")
	fresh := berthPrefix(t, wt) + "1"
	writeBerthRowsTo(t, m, fresh)

	res := repo.Berth(t, wt, "reset")

	if res.Exit != 1 || !strings.Contains(res.Stdout+res.Stderr, "berth snapshot") {
		t.Errorf("berth reset exited %d, want 1 naming berth snapshot:\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	if !hasMarker(t, m, fresh) {
		t.Errorf("reset dropped %s before refusing", fresh)
	}
}

func TestResetRefusesBeforeDroppingWhenASeedFileIsMissing(t *testing.T) {
	repo, m := dumpServer(t)
	writeFile(t, filepath.Join(repo.Dir, "seed.sql"), "CREATE TABLE seeded (id INT PRIMARY KEY);\n")
	writeFile(t, repo.privateConfig(), seedConfig(seedEntry("central", "seed.sql"), `"1" = "fresh"`))
	wt := repo.AddWorktree(t, "feat-seed-gone")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)
	writeBerthRowsTo(t, m, p+"central")
	if err := os.Remove(filepath.Join(repo.Dir, "seed.sql")); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"reset"}, {"reset", "--dry-run"}} {
		res := repo.Berth(t, wt, args...)
		if res.Exit != 1 || !strings.Contains(res.Stdout+res.Stderr, "seed.sql") {
			t.Errorf("berth %v exited %d, want 1 naming the file:\n%s%s", args, res.Exit, res.Stdout, res.Stderr)
		}
	}

	if !hasMarker(t, m, p+"central") {
		t.Errorf("reset dropped %scentral before refusing", p)
	}
}

func TestARestoreUsesOneGenerationWhileASnapshotReplacesIt(t *testing.T) {
	repo, m := snapshotServer(t)
	mustBerth(t, repo, repo.Dir, "snapshot")
	want := mainContents(t, m)
	changeMain(t, m, "second-generation")
	wt := repo.AddWorktree(t, "feat-one-gen")
	add := exec.Command(berthBin, "add", "--wait")
	add.Dir, add.Env = wt, append(repo.Env(), cloneDelay+"=20s")
	if err := add.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = add.Process.Kill(); _ = add.Wait() })
	waitFor(t, time.Minute, "the clone process to restore central", func() bool {
		data, err := os.ReadFile(filepath.Join(wt, ".env"))
		if err != nil || !strings.Contains(string(data), "# >>> berth") {
			return false
		}
		target := berthPrefix(t, wt) + "central"
		return m.has(t, target) && m.contents(t, target) == want["central"]
	})

	mustBerth(t, repo, repo.Dir, "snapshot")

	if err := add.Wait(); err != nil {
		t.Fatalf("berth add --wait: %v", err)
	}
	expectFromSnapshot(t, repo, m, wt, want, "with the snapshot replaced after central was restored")
}

func moveRepoAndRepair(t *testing.T, repo *Repo, wt string) {
	t.Helper()
	moved := filepath.Join(filepath.Dir(repo.Dir), "moved-repo")
	if err := os.Rename(repo.Dir, moved); err != nil {
		t.Fatal(err)
	}
	repo.Dir = moved
	repo.Git(t, "worktree", "repair", wt)
}

func TestResetInAMovedRepositoryRefusesBeforeDropping(t *testing.T) {
	repo, m := snapshotServer(t)
	mustBerth(t, repo, repo.Dir, "snapshot")
	wt := repo.AddWorktree(t, "feat-moved")
	mustBerth(t, repo, wt, "add", "--wait")
	central := berthPrefix(t, wt) + "central"
	writeBerthRowsTo(t, m, central)
	moveRepoAndRepair(t, repo, wt)

	res := repo.Berth(t, wt, "reset")

	if res.Exit != 1 || !strings.Contains(res.Stdout+res.Stderr, "berth snapshot") {
		t.Errorf("berth reset after the move exited %d, want 1 naming berth snapshot:\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	if !hasMarker(t, m, central) {
		t.Errorf("reset dropped %s before refusing", central)
	}
}

func TestSnapshotAtAMovedRepositorysNewPathServesItsResets(t *testing.T) {
	repo, m := snapshotServer(t)
	mustBerth(t, repo, repo.Dir, "snapshot")
	wt := repo.AddWorktree(t, "feat-moved-snap")
	mustBerth(t, repo, wt, "add", "--wait")
	moveRepoAndRepair(t, repo, wt)
	changeMain(t, m, "after-move")

	mustBerth(t, repo, repo.Dir, "snapshot")
	want := mainContents(t, m)
	mustBerth(t, repo, wt, "reset")
	mustBerth(t, repo, wt, "status", "--wait")

	expectFromSnapshot(t, repo, m, wt, want, "after a snapshot at the moved repository's new path")
}

func TestSnapshotOfADroppedDatabaseFailsAndKeepsThePrevious(t *testing.T) {
	repo, m := snapshotServer(t)
	mustBerth(t, repo, repo.Dir, "snapshot")
	want := mainContents(t, m)
	m.sql(t, "DROP DATABASE app_reports")

	res := repo.Berth(t, repo.Dir, "snapshot")

	if res.Exit != 4 || !strings.Contains(res.Stdout+res.Stderr, "app_reports") {
		t.Errorf("berth snapshot with app_reports gone exited %d, want 4 naming it:\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	wt := repo.AddWorktree(t, "feat-kept")
	mustBerth(t, repo, wt, "add", "--wait")
	expectFromSnapshot(t, repo, m, wt, want, "after a snapshot refused for a dropped database")
}

func TestFirstSnapshotOfADatabaseMainDoesNotHaveSucceedsEmpty(t *testing.T) {
	repo, m := snapshotServer(t)
	m.sql(t, "DROP DATABASE app_reports")

	mustBerth(t, repo, repo.Dir, "snapshot")

	wt := repo.AddWorktree(t, "feat-first-empty")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)
	if !m.has(t, p+"reports") {
		t.Errorf("the berth has no %sreports, want it empty", p)
	}
	if n := m.tables(t, p+"reports"); n != "0" {
		t.Errorf("the berth's %sreports has %s tables, want none", p, n)
	}
}

func TestLeftoverGenerationWarnsAndTheNewSnapshotIsCurrent(t *testing.T) {
	repo, m := snapshotServer(t)
	mustBerth(t, repo, repo.Dir, "snapshot")
	generations, err := filepath.Glob(filepath.Join(repo.StateDir, "snapshots", "*", "gen-*"))
	if err != nil || len(generations) != 1 {
		t.Fatalf("generations after the first snapshot: %v %v", generations, err)
	}
	old := generations[0]
	blockRemovalOf(t, old)
	changeMain(t, m, "second")

	res := repo.Berth(t, repo.Dir, "snapshot")
	want := mainContents(t, m)

	if res.Exit != 0 {
		t.Fatalf("berth snapshot exited %d though the new snapshot is current:\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "removed") {
		t.Errorf("berth snapshot doesn't warn about the leftover:\n%s", res.Stderr)
	}
	wt := repo.AddWorktree(t, "feat-leftover")
	mustBerth(t, repo, wt, "add", "--wait")
	expectFromSnapshot(t, repo, m, wt, want, "after a snapshot whose old generation couldn't be removed")
}

func TestOneDatabaseFailingKeepsThePreviousSnapshotForBoth(t *testing.T) {
	repo, m := snapshotServer(t)
	mustBerth(t, repo, repo.Dir, "snapshot")
	want := mainContents(t, m)
	changeMain(t, m, "after-previous")
	// reports sorts after central, so central is read and written before this fails.
	m.sql(t, "CREATE TABLE app_reports.broken (id INT PRIMARY KEY) ENGINE=MyISAM; FLUSH TABLES app_reports.broken")
	mustDocker(t, "exec", m.Container, "sh", "-c", "rm /var/lib/mysql/app_reports/broken*.MYD")

	res := repo.Berth(t, repo.Dir, "snapshot")

	if res.Exit != 4 {
		t.Errorf("berth snapshot with one database failing exited %d, want 4:\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	wt := repo.AddWorktree(t, "feat-partial")
	mustBerth(t, repo, wt, "add", "--wait")
	expectFromSnapshot(t, repo, m, wt, want, "after a snapshot in which one database failed")
}
