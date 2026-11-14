//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var postgresMain = []string{"app_central", "app_1", "shop"}

func TestPostgresBerthPrefixesAreTheirOwnAndRemoveLeavesTheRest(t *testing.T) {
	for _, version := range []string{postgres13, postgres18} {
		t.Run(version, func(t *testing.T) {
			repo, m := prefixedPostgres(t, version)
			names := []string{"feat", "feat-x", "feat-x-y"}
			wts, prefixes := map[string]string{}, map[string]string{}
			for _, n := range names {
				wts[n] = repo.AddWorktree(t, n)
				mustBerth(t, repo, wts[n], "add", "--wait")
				prefixes[n] = berthPrefix(t, wts[n])
			}
			for _, a := range names {
				for _, b := range names {
					if a != b && strings.HasPrefix(prefixes[b], prefixes[a]) {
						t.Errorf("berth %s's prefix %q begins berth %s's prefix %q", a, prefixes[a], b, prefixes[b])
					}
				}
				for _, src := range postgresMain {
					if strings.HasPrefix(src, prefixes[a]) {
						t.Errorf("berth %s's prefix %q begins main's database %s", a, prefixes[a], src)
					}
				}
				want := prefixes[a] + "1," + prefixes[a] + "central"
				if got := strings.Join(m.under(t, prefixes[a]), ","); got != want {
					t.Errorf("databases under berth %s's prefix = %q, want only its own %q", a, got, want)
				}
			}

			for i, n := range names {
				removeJSON(t, repo, wts[n])
				if left := m.under(t, prefixes[n]); len(left) > 0 {
					t.Errorf("after removing %s, its databases remain: %v", n, left)
				}
				m.mustHave(t, "after removing "+n, postgresMain...)
				for _, other := range names[i+1:] {
					m.mustHave(t, "after removing "+n, prefixes[other]+"central", prefixes[other]+"1")
				}
			}
		})
	}
}

func TestPostgresRemoveDropsADatabaseTheAppIsConnectedTo(t *testing.T) {
	for _, version := range []string{postgres13, postgres18} {
		t.Run(version, func(t *testing.T) {
			repo, m := prefixedPostgres(t, version)
			wt := repo.AddWorktree(t, "feat-x")
			mustBerth(t, repo, wt, "add", "--wait")
			p := berthPrefix(t, wt)
			m.hold(t, p+"central", "berth_app")

			removeJSON(t, repo, wt)
			if left := m.under(t, p); len(left) > 0 {
				t.Errorf("after remove with the app connected, the berth's databases remain: %v", left)
			}
		})
	}
}

func TestPostgresRemoveEndsNoSessionOnAnotherDatabase(t *testing.T) {
	for _, version := range []string{postgres13, postgres18} {
		t.Run(version, func(t *testing.T) {
			repo, m := prefixedPostgres(t, version)
			wt, other := repo.AddWorktree(t, "feat-x"), repo.AddWorktree(t, "feat-y")
			mustBerth(t, repo, wt, "add", "--wait")
			mustBerth(t, repo, other, "add", "--wait")
			m.hold(t, berthPrefix(t, wt)+"central", "berth_app")
			m.hold(t, "app_central", "main_app")
			otherDB := berthPrefix(t, other) + "central"
			m.hold(t, otherDB, "other_berth_app")

			removeJSON(t, repo, wt)
			if got := m.sessions(t, "app_central", "main_app"); got != "1" {
				t.Errorf("after remove, main's session on app_central: %s open, want 1", got)
			}
			if got := m.sessions(t, otherDB, "other_berth_app"); got != "1" {
				t.Errorf("after remove, another berth's session on %s: %s open, want 1", otherDB, got)
			}
		})
	}
}

func TestPostgresRemoveDropsWhatTheAppCreatedUnderThePrefixOnly(t *testing.T) {
	repo, m := prefixedPostgres(t, postgres18)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)
	tenant := p + "42"
	m.create(t, tenant, "app_other", "feat_x__42")

	got := removeJSON(t, repo, wt)
	if !got.has("database", tenant) {
		t.Errorf("remove does not report dropping %s: %v", tenant, got.items())
	}
	if left := m.under(t, p); len(left) > 0 {
		t.Errorf("databases under the berth's prefix remain after remove: %v", left)
	}
	m.mustHave(t, "after remove", "app_other", "feat_x__42", "app_central", "app_1", "shop")
}

// addLongBranches adds two berths whose 200-character branch names differ only at the
// end, so a cut that drops the end alone would give both the same name.
func addLongBranches(t *testing.T, repo *Repo) []string {
	t.Helper()
	long := strings.Repeat("very-long-branch-", 12)
	var wts []string
	for _, end := range []string{"-one", "-two"} {
		wt := repo.AddWorktree(t, long[:200-len(end)]+end)
		mustBerth(t, repo, wt, "add", "--wait")
		wts = append(wts, wt)
	}
	return wts
}

func expectLongNamesCutAndUnique(t *testing.T, m *hostPostgres, before []string, wts []string) {
	t.Helper()
	existing := map[string]bool{}
	for _, n := range before {
		existing[n] = true
	}
	var created []string
	for _, n := range m.databases(t) {
		if !existing[n] {
			created = append(created, n)
			if len(n) > 63 {
				t.Errorf("database name %q is %d bytes, want at most 63", n, len(n))
			}
		}
	}
	if len(created) != 2 {
		t.Errorf("two berths with one database each created %d databases, want 2 distinct: %v", len(created), created)
	}
	a, b := blockValue(t, wts[0], "DB_DATABASE"), blockValue(t, wts[1], "DB_DATABASE")
	if a == "" || a == b {
		t.Errorf("long-branch berths set DB_DATABASE to %q and %q, want two distinct names", a, b)
	}
	// Postgres silently truncates a longer identifier, so a name berth wrote past 63 bytes
	// shows up here as one the server does not hold.
	m.mustHave(t, "after adding long-branch berths", a, b)
}

func TestPostgresLongExactNameIsCutAndStaysUnique(t *testing.T) {
	repo := NewRepo(t)
	m := startHostPostgres(t, repo, "", postgres18)
	m.create(t, "myapp")
	pointMainAtPostgres(t, repo, m, "DB_DATABASE=myapp")
	writeFile(t, repo.privateConfig(), unpublishedDB+`[state.postgres]
from = "env:DB_*"

[state.postgres.databases]
myapp = "fresh"
`)
	before := m.databases(t)
	expectLongNamesCutAndUnique(t, m, before, addLongBranches(t, repo))
}

func TestPostgresLongPrefixedNameIsCutAndStaysUnique(t *testing.T) {
	repo := NewRepo(t)
	m := startHostPostgres(t, repo, "", postgres18)
	m.create(t, "application_tenant_central_reporting")
	pointMainAtPostgres(t, repo, m, "DB_DATABASE_PREFIX=application_tenant_", "DB_DATABASE=application_tenant_central_reporting")
	writeFile(t, repo.privateConfig(), unpublishedDB+`[state.postgres]
from = "env:DB_*"
prefix = "env:DB_DATABASE_PREFIX"

[state.postgres.databases]
central_reporting = "fresh"
`)
	before := m.databases(t)
	expectLongNamesCutAndUnique(t, m, before, addLongBranches(t, repo))
}

func TestPostgresRefusesMySQLOnlyStrategiesExitingFive(t *testing.T) {
	repo, m := prefixedPostgres(t, postgres18)
	writeFile(t, filepath.Join(repo.Dir, "seed.sql"), "SELECT 1;\n")
	fresh := repo.AddWorktree(t, "fresh")
	before := strings.Join(m.databases(t), ",")

	cases := []struct{ name, entry, key string }{
		{"dump", `central = "dump"`, "central"},
		{"file seed", `seeded = { from = "file:seed.sql" }`, "seeded"},
		{"snapshot", `reports = "snapshot"`, "reports"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			writeFile(t, repo.privateConfig(), unpublishedDB+`[state.postgres]
from = "env:DB_*"
prefix = "env:DB_DATABASE_PREFIX"

[state.postgres.databases]
`+c.entry+"\n")
			res := repo.Berth(t, fresh, "add")
			if res.Exit != 5 {
				t.Errorf("add: exit %d, want 5\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
			}
			if !strings.Contains(res.Stderr, c.key) {
				t.Errorf("add: message %q does not name the key %s", res.Stderr, c.key)
			}
			if got := berthsAt(list(t, repo), fresh); len(got) != 0 {
				t.Errorf("add with a refused strategy recorded a berth: %+v", got)
			}
			if got := strings.Join(m.databases(t), ","); got != before {
				t.Errorf("add with a refused strategy changed the server's databases from %q to %q", before, got)
			}
		})
	}
}

func TestPostgresRemoveWorksWithMainsEnvDeleted(t *testing.T) {
	repo, m := prefixedPostgres(t, postgres18)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)
	if err := os.Remove(filepath.Join(repo.Dir, ".env")); err != nil {
		t.Fatal(err)
	}

	removeJSON(t, repo, wt)
	if left := m.under(t, p); len(left) > 0 {
		t.Errorf("after remove with main's .env deleted, the berth's databases remain: %v", left)
	}
	m.mustHave(t, "after remove", postgresMain...)
}

func TestPostgresPruneWorksWithMainsEnvDeleted(t *testing.T) {
	repo, m := prefixedPostgres(t, postgres18)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)
	repo.Git(t, "worktree", "remove", "--force", wt)
	if err := os.Remove(filepath.Join(repo.Dir, ".env")); err != nil {
		t.Fatal(err)
	}

	// An orphan's .env went with its worktree, so the shell is the only place left for the login.
	res := repo.BerthWith(t, []string{"DB_USERNAME=" + m.User, "DB_PASSWORD=" + m.Password}, repo.Dir, "prune", "--json")
	if res.Exit != 0 {
		t.Fatalf("prune with main's .env deleted: exit %d\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	if left := m.under(t, p); len(left) > 0 {
		t.Errorf("after prune with main's .env deleted, the orphan's databases remain: %v", left)
	}
	m.mustHave(t, "after prune", postgresMain...)
}

func TestPostgresResetRefusesWhenMainNamesAnotherServer(t *testing.T) {
	repo, m := prefixedPostgres(t, postgres18)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)
	m.sqlIn(t, p+"central", "CREATE TABLE work (id INT PRIMARY KEY); INSERT INTO work VALUES (7)")

	other := startHostPostgres(t, repo, "2", postgres18)
	pointMainAtPostgres(t, repo, other)
	res := repo.Berth(t, wt, "reset")
	if res.Exit != 1 {
		t.Errorf("reset with main's .env naming another server: exit %d, want 1\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	if got := m.sqlIn(t, p+"central", "SELECT id FROM work"); got != "7" {
		t.Errorf("the berth's database lost its data after the refused reset: %q", got)
	}
	m.mustHave(t, "after the refused reset", p+"1")
	if got := other.databases(t); len(got) != 0 {
		t.Errorf("the refused reset created databases on the other server: %v", got)
	}
}
