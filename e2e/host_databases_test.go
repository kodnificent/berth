//go:build e2e

package e2e

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type databaseEntry struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Volume   string `json:"volume"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	Strategy string `json:"strategy"`
	Status   string `json:"status"`
}

func databaseEntries(t *testing.T, repo *Repo, dir string) []databaseEntry {
	t.Helper()
	res := mustBerth(t, repo, dir, "status", "--json")
	var s struct {
		State []databaseEntry `json:"state"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &s); err != nil {
		t.Fatalf("berth status --json: %v\n%s", err, res.Stdout)
	}
	var out []databaseEntry
	for _, e := range s.State {
		if e.Kind == "database" {
			out = append(out, e)
		}
	}
	return out
}

// With prefix set, add creates each fresh database empty under the berth's
// prefix, and the managed block sets the prefix key and DB_DATABASE to the berth's.
func TestAddCreatesFreshDatabasesUnderTheBerthsPrefix(t *testing.T) {
	repo, m := prefixedServer(t)
	writeFile(t, repo.privateConfig(), unpublishedDB+`[state.mysql]
from = "env:DB_*"
prefix = "env:DB_DATABASE_PREFIX"

[state.mysql.databases]
central = "fresh"
"*" = "fresh"
`)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")

	if got := strings.Join(m.under(t, "app_feat_x__"), ","); got != "app_feat_x__1,app_feat_x__central" {
		t.Errorf("databases under app_feat_x__ = %q, want app_feat_x__1,app_feat_x__central; server has %v", got, m.databases(t))
	}
	for _, db := range []string{"app_feat_x__central", "app_feat_x__1"} {
		if m.has(t, db) && m.tables(t, db) != "0" {
			t.Errorf("fresh database %s has %s tables, want none", db, m.tables(t, db))
		}
	}
	env := blockEnv(t, filepath.Join(wt, ".env"))
	if env["DB_DATABASE_PREFIX"] != "app_feat_x__" || env["DB_DATABASE"] != "app_feat_x__central" {
		t.Errorf("managed block sets DB_DATABASE_PREFIX=%q DB_DATABASE=%q, want app_feat_x__ and app_feat_x__central", env["DB_DATABASE_PREFIX"], env["DB_DATABASE"])
	}
	if m.sql(t, "SELECT COUNT(*) FROM app_central.users") != "1" {
		t.Errorf("main's app_central lost its data after add")
	}
}

// Without prefix, each listed database becomes one named from it and the
// berth's name, and the key naming it in main's .env gets that name.
func TestAddNamesListedDatabasesWithoutAPrefix(t *testing.T) {
	repo := NewRepo(t)
	m := startHostMySQL(t, repo, "")
	m.create(t, "myapp", "reports")
	pointMainAt(t, repo, m, "DB_DATABASE=myapp", "REPORTS_DB=reports")
	writeFile(t, repo.privateConfig(), unpublishedDB+`[state.mysql]
from = "env:DB_*"

[state.mysql.databases]
myapp = "fresh"
reports = "fresh"
`)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")

	mustHave(t, m, "after add", "myapp__feat_x", "reports__feat_x", "myapp", "reports")
	env := blockEnv(t, filepath.Join(wt, ".env"))
	if env["DB_DATABASE"] != "myapp__feat_x" || env["REPORTS_DB"] != "reports__feat_x" {
		t.Errorf("managed block sets DB_DATABASE=%q REPORTS_DB=%q, want myapp__feat_x and reports__feat_x", env["DB_DATABASE"], env["REPORTS_DB"])
	}
}

// The "A berth's database names are its own" guarantee: berths feat, feat-x
// and feat-x-y on one server get prefixes none of which begins another's or a source
// database's name, and remove of each leaves the others' and main's databases.
func TestBerthDatabaseNamesAreTheirOwn(t *testing.T) {
	repo, m := prefixedServer(t)
	main := []string{"app_central", "app_1", "shop"}
	names := []string{"feat", "feat-x", "feat-x-y"}
	wts := map[string]string{}
	prefixes := map[string]string{}
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
		for _, src := range main {
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
		mustHave(t, m, "after removing "+n, main...)
		for _, other := range names[i+1:] {
			mustHave(t, m, "after removing "+n, prefixes[other]+"central", prefixes[other]+"1")
		}
	}
}

// A 200-character branch name gives database names of at most 64
// characters that are still unique per berth.
func TestLongBranchGivesShortUniqueDatabaseNames(t *testing.T) {
	repo, m := prefixedServer(t)
	before := map[string]bool{}
	for _, n := range m.databases(t) {
		before[n] = true
	}
	long := strings.Repeat("very-long-branch-", 12)
	var wts []string
	for _, end := range []string{"-one", "-two"} {
		branch := long[:200-len(end)] + end
		wt := repo.AddWorktree(t, branch)
		mustBerth(t, repo, wt, "add", "--wait")
		wts = append(wts, wt)
	}

	var created []string
	for _, n := range m.databases(t) {
		if !before[n] {
			created = append(created, n)
			if len(n) > 64 {
				t.Errorf("database name %q is %d characters, want at most 64", n, len(n))
			}
		}
	}
	if len(created) != 4 {
		t.Errorf("two berths with two databases each created %d databases, want 4 distinct: %v", len(created), created)
	}
	a, b := berthPrefix(t, wts[0]), berthPrefix(t, wts[1])
	if strings.HasPrefix(a, b) || strings.HasPrefix(b, a) {
		t.Errorf("prefixes %q and %q of two long-branch berths begin one another", a, b)
	}
	dbA := blockEnv(t, filepath.Join(wts[0], ".env"))["DB_DATABASE"]
	dbB := blockEnv(t, filepath.Join(wts[1], ".env"))["DB_DATABASE"]
	if dbA == "" || dbA == dbB || !m.has(t, dbA) || !m.has(t, dbB) {
		t.Errorf("long-branch berths set DB_DATABASE to %q and %q, want two distinct databases on the server: %v", dbA, dbB, m.databases(t))
	}
}

// Skip creates nothing and writes nothing for that database.
func TestSkipDatabaseIsLeftAlone(t *testing.T) {
	repo := NewRepo(t)
	m := startHostMySQL(t, repo, "")
	m.create(t, "myapp", "reports")
	pointMainAt(t, repo, m, "DB_DATABASE=myapp", "REPORTS_DB=reports")
	writeFile(t, repo.privateConfig(), unpublishedDB+`[state.mysql]
from = "env:DB_*"

[state.mysql.databases]
myapp = "fresh"
reports = "skip"
`)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")

	if got := strings.Join(m.databases(t), ","); got != "myapp,myapp__feat_x,reports" {
		t.Errorf("databases after add = %q, want only myapp__feat_x added", got)
	}
	if v, ok := blockEnv(t, filepath.Join(wt, ".env"))["REPORTS_DB"]; ok {
		t.Errorf("managed block sets REPORTS_DB=%q for a skipped database", v)
	}
}

// None writes the source's name and doctor warns that state is shared.
func TestNoneDatabaseUsesTheSourceAndDoctorWarns(t *testing.T) {
	repo := NewRepo(t)
	m := startHostMySQL(t, repo, "")
	m.create(t, "myapp")
	pointMainAt(t, repo, m, "DB_DATABASE=myapp")
	writeFile(t, repo.privateConfig(), unpublishedDB+`[state.mysql]
from = "env:DB_*"

[state.mysql.databases]
myapp = "none"
`)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")

	if got := strings.Join(m.databases(t), ","); got != "myapp" {
		t.Errorf("databases after add = %q, want only main's myapp", got)
	}
	if got := readDotEnv(t, filepath.Join(wt, ".env"))["DB_DATABASE"]; got != "myapp" {
		t.Errorf("the berth's DB_DATABASE = %q, want the source's myapp", got)
	}

	res := repo.Berth(t, wt, "doctor", "--json")
	var d struct {
		Findings []struct {
			Level    string `json:"level"`
			Location string `json:"location"`
			Message  string `json:"message"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &d); err != nil {
		t.Fatalf("berth doctor --json: %v\n%s", err, res.Stdout)
	}
	for _, f := range d.Findings {
		if f.Level == "warning" && strings.Contains(f.Location+" "+f.Message, "myapp") {
			return
		}
	}
	t.Errorf("doctor gives no warning naming myapp, which the berth shares with main:\n%s", res.Stdout)
}

// A database the app creates under the berth's prefix after add is listed
// by remove --dry-run and dropped by remove; one outside the prefix is never touched.
func TestRemoveDropsDatabasesTheAppCreatedUnderThePrefix(t *testing.T) {
	repo, m := prefixedServer(t)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)
	tenant := p + "42"
	m.create(t, tenant, "app_other", "feat_x__42")

	dry := removeJSON(t, repo, wt, "--dry-run")
	for _, db := range []string{p + "central", p + "1", tenant} {
		if !dry.has("database", db) {
			t.Errorf("remove --dry-run does not list database %s: %v", db, dry.items())
		}
	}
	for _, db := range []string{"app_other", "feat_x__42", "app_central", "app_1", "shop"} {
		if dry.has("database", db) {
			t.Errorf("remove --dry-run lists database %s, which is outside the berth's prefix", db)
		}
	}
	mustHave(t, m, "after remove --dry-run", tenant, p+"central")

	got := removeJSON(t, repo, wt)
	if !got.has("database", tenant) {
		t.Errorf("remove does not report dropping %s: %v", tenant, got.items())
	}
	if left := m.under(t, p); len(left) > 0 {
		t.Errorf("databases under the berth's prefix remain after remove: %v", left)
	}
	mustHave(t, m, "after remove", "app_other", "feat_x__42", "app_central", "app_1", "shop")
}

// Remove with the server unreachable deletes nothing of the berth's records,
// exits 1 and says why; once the server is back, running it again finishes the job.
func TestRemoveWithServerDownKeepsTheBerthUntilRetried(t *testing.T) {
	repo, m := prefixedServer(t)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)

	m.stop(t)
	res := repo.Berth(t, wt, "remove")
	if res.Exit != 1 {
		t.Errorf("remove with the server down: exit %d, want 1\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stderr, strconv.Itoa(m.Port)) {
		t.Errorf("remove with the server down doesn't name the server 127.0.0.1:%d it couldn't reach:\n%s", m.Port, res.Stderr)
	}
	if strings.Contains(res.Stdout+res.Stderr, m.Password) {
		t.Errorf("remove's failure prints the database password")
	}
	if n := len(databaseEntries(t, repo, wt)); n != 2 {
		t.Errorf("after the failed remove, status lists %d databases, want the berth still recording its 2", n)
	}

	m.start(t)
	mustHave(t, m, "after the failed remove", p+"central", p+"1")
	removeJSON(t, repo, wt)
	if left := m.under(t, p); len(left) > 0 {
		t.Errorf("databases under the berth's prefix remain after the retried remove: %v", left)
	}
	mustHave(t, m, "after the retried remove", "app_central", "app_1", "shop")
	if res := repo.Berth(t, wt, "status", "--json"); res.Exit == 0 || !strings.Contains(res.Stdout, "no_berth") {
		t.Errorf("status after the retried remove: exit %d, want no_berth\n%s", res.Exit, res.Stdout)
	}
}

// Status --json lists each database with kind, source, target, strategy and
// status, and each volume entry carries target as well as volume.
func TestStatusListsDatabasesAndVolumeTargets(t *testing.T) {
	repo, _ := prefixedServer(t)
	fillCache(t, repo)
	writeFile(t, repo.privateConfig(), unpublishedDB+`[compose.volumes]
cache = "clone"

[state.mysql]
from = "env:DB_*"
prefix = "env:DB_DATABASE_PREFIX"

[state.mysql.databases]
central = "fresh"
"*" = "fresh"
`)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")

	res := mustBerth(t, repo, wt, "status", "--json")
	var s struct {
		State []databaseEntry `json:"state"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &s); err != nil {
		t.Fatalf("berth status --json: %v\n%s", err, res.Stdout)
	}
	got := map[string]databaseEntry{}
	var cache *databaseEntry
	for i, e := range s.State {
		switch e.Kind {
		case "database":
			got[e.Source] = e
		case "volume":
			if e.Name == "cache" {
				cache = &s.State[i]
			}
		}
	}
	for src, target := range map[string]string{"app_central": "app_feat_x__central", "app_1": "app_feat_x__1"} {
		e, ok := got[src]
		if !ok {
			t.Errorf("status lists no database with source %s:\n%s", src, res.Stdout)
			continue
		}
		if e.Target != target || e.Strategy != "fresh" || e.Status != "ready" {
			t.Errorf("database %s: target %q strategy %q status %q, want %s fresh ready", src, e.Target, e.Strategy, e.Status, target)
		}
	}
	if len(got) != 2 {
		t.Errorf("status lists %d databases, want 2:\n%s", len(got), res.Stdout)
	}
	if cache == nil {
		t.Fatalf("status lists no cache volume:\n%s", res.Stdout)
	}
	if cache.Volume == "" || cache.Target != cache.Volume {
		t.Errorf("cache volume entry has volume %q and target %q, want both naming the berth's volume", cache.Volume, cache.Target)
	}
}

// An unknown key, a bad pattern, or a from naming keys main's .env doesn't
// set exits 5 naming the key, and creates no berth and no database.
func TestInvalidMySQLConfigExitsFive(t *testing.T) {
	repo, m := prefixedServer(t)
	withBerth := repo.AddWorktree(t, "with-berth")
	mustBerth(t, repo, withBerth, "add", "--wait")
	fresh := repo.AddWorktree(t, "fresh")
	before := strings.Join(m.databases(t), ",")

	cases := []struct {
		name, content string
		key           []string
		status        bool
	}{
		{"unknown key", "[state.mysql]\nfrom = \"env:DB_*\"\nprefx = \"env:DB_DATABASE_PREFIX\"\n", []string{"prefx"}, true},
		{"bad pattern", "[state.mysql]\nfrom = \"env:DB_*\"\nprefix = \"env:DB_DATABASE_PREFIX\"\n\n[state.mysql.databases]\n\"tenant/*\" = \"fresh\"\n", []string{"tenant/*"}, true},
		{"from naming unset keys", "[state.mysql]\nfrom = \"env:SHOP_DB_*\"\nprefix = \"env:DB_DATABASE_PREFIX\"\n", []string{"from", "SHOP_DB_"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			writeFile(t, repo.privateConfig(), unpublishedDB+c.content)
			runs := [][2]string{{fresh, "add"}}
			if c.status {
				runs = append(runs, [2]string{withBerth, "status"})
			}
			for _, run := range runs {
				res := repo.Berth(t, run[0], run[1])
				if res.Exit != 5 {
					t.Errorf("berth %s: exit %d, want 5\nstdout:\n%s\nstderr:\n%s", run[1], res.Exit, res.Stdout, res.Stderr)
				}
				for _, want := range append([]string{repo.privateConfig()}, c.key...) {
					if !strings.Contains(res.Stderr, want) {
						t.Errorf("berth %s: message %q does not name %s", run[1], res.Stderr, want)
					}
				}
			}
			if got := berthsAt(list(t, repo), fresh); len(got) != 0 {
				t.Errorf("add with invalid config recorded a berth: %+v", got)
			}
			if got := strings.Join(m.databases(t), ","); got != before {
				t.Errorf("add with invalid config changed the server's databases from %q to %q", before, got)
			}
		})
	}
}

// No credential berth reads is written to the registry, the managed block
// or any output.
func TestCredentialsAreNeverWrittenOrPrinted(t *testing.T) {
	repo, m := prefixedServer(t)
	wt := repo.AddWorktree(t, "feat-x")
	add := mustBerth(t, repo, wt, "add", "--wait")
	outputs := map[string]string{"add": add.Stdout + add.Stderr}
	for _, args := range [][]string{
		{"status"}, {"status", "--json"}, {"list"}, {"list", "--json"},
		{"doctor"}, {"doctor", "--json"}, {"remove", "--dry-run"}, {"remove", "--dry-run", "--json"},
	} {
		res := repo.Berth(t, wt, args...)
		outputs[strings.Join(args, " ")] = res.Stdout + res.Stderr
	}

	secrets := []string{m.Password, m.User}
	check := func(where, content string) {
		for _, s := range secrets {
			if strings.Contains(content, s) {
				t.Errorf("%s contains the credential %q", where, s)
			}
		}
	}
	block := managedBlock(t, filepath.Join(wt, ".env"))
	check("the managed block", block)
	for _, key := range []string{"DB_USERNAME=", "DB_PASSWORD="} {
		if strings.Contains(block, key) {
			t.Errorf("the managed block sets %s", strings.TrimSuffix(key, "="))
		}
	}
	err := filepath.WalkDir(repo.StateDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		check(path, string(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	res := repo.Berth(t, wt, "remove")
	outputs["remove"] = res.Stdout + res.Stderr
	for cmd, out := range outputs {
		check("berth "+cmd, out)
	}
}

// A prefix the server already holds databases under, including one left
// by a removed berth, is passed over for the next free one.
func TestPrefixInUseOnTheServerIsPassedOver(t *testing.T) {
	repo, m := prefixedServer(t)
	m.create(t, "app_feat_x__legacy")
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")
	expectFreePrefix(t, m, berthPrefix(t, wt), "app_feat_x__")
	if m.tables(t, "app_feat_x__legacy") != "0" || !m.has(t, "app_feat_x__legacy") {
		t.Errorf("app_feat_x__legacy was touched by add")
	}

	left := repo.AddWorktree(t, "feat-y")
	mustBerth(t, repo, left, "add", "--wait")
	if p := berthPrefix(t, left); p != "app_feat_y__" {
		t.Fatalf("berth feat-y on a server with nothing under app_feat_y__ got prefix %q", p)
	}
	removeJSON(t, repo, left)
	m.create(t, "app_feat_y__late")
	mustBerth(t, repo, left, "add", "--wait")
	expectFreePrefix(t, m, berthPrefix(t, left), "app_feat_y__")
	mustHave(t, m, "after the second add", "app_feat_y__late")
}

func expectFreePrefix(t *testing.T, m *hostMySQL, got, taken string) {
	t.Helper()
	if got == taken || !strings.HasPrefix(got, "app_") || !strings.HasSuffix(got, "__") {
		t.Errorf("prefix = %q, want one built from app_ and the berth's name, other than the taken %q", got, taken)
	}
	want := got + "1," + got + "central"
	if under := strings.Join(m.under(t, got), ","); under != want {
		t.Errorf("databases under the berth's prefix %q = %q, want only its own %q", got, under, want)
	}
}

// After the config's [state.mysql] is changed or deleted, remove still
// drops exactly the databases the record names and those under the recorded prefix,
// with main's .env unreadable.
func TestRemoveWorksFromTheRecordAlone(t *testing.T) {
	repo, m := prefixedServer(t)
	a, b := repo.AddWorktree(t, "feat-a"), repo.AddWorktree(t, "feat-b")
	mustBerth(t, repo, a, "add", "--wait")
	mustBerth(t, repo, b, "add", "--wait")
	pa, pb := berthPrefix(t, a), berthPrefix(t, b)
	m.create(t, pa+"7")
	mainEnv := filepath.Join(repo.Dir, ".env")
	t.Cleanup(func() { _ = os.Chmod(mainEnv, 0o644) })
	main := []string{"app_central", "app_1", "shop"}

	writeFile(t, repo.privateConfig(), unpublishedDB+`[state.mysql]
from = "env:DB_*"
prefix = "env:TENANT_PREFIX"

[state.mysql.databases]
other = "fresh"
`)
	chmod(t, mainEnv, 0)
	got := removeJSON(t, repo, a)
	chmod(t, mainEnv, 0o644)
	for _, db := range []string{pa + "central", pa + "1", pa + "7"} {
		if !got.has("database", db) {
			t.Errorf("remove with changed config does not report dropping %s: %v", db, got.items())
		}
	}
	if left := m.under(t, pa); len(left) > 0 {
		t.Errorf("after remove with changed config and unreadable .env, databases under %s remain: %v", pa, left)
	}
	mustHave(t, m, "after removing feat-a", append(main, pb+"central", pb+"1")...)

	if err := os.Remove(repo.privateConfig()); err != nil {
		t.Fatal(err)
	}
	chmod(t, mainEnv, 0)
	removeJSON(t, repo, b)
	chmod(t, mainEnv, 0o644)
	if left := m.under(t, pb); len(left) > 0 {
		t.Errorf("after remove with [state.mysql] deleted and unreadable .env, databases under %s remain: %v", pb, left)
	}
	mustHave(t, m, "after removing feat-b", main...)
}

// With main's .env pointing at another server, reset exits 1 and drops
// nothing.
func TestResetRefusesWhenMainNamesAnotherServer(t *testing.T) {
	repo, m := prefixedServer(t)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)
	m.sql(t, "CREATE TABLE `"+p+"central`.work (id INT PRIMARY KEY); INSERT INTO `"+p+"central`.work VALUES (7)")

	other := startHostMySQL(t, repo, "2")
	pointMainAt(t, repo, other)
	res := repo.Berth(t, wt, "reset")
	if res.Exit != 1 {
		t.Errorf("reset with main's .env naming another server: exit %d, want 1\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	if got := m.sql(t, "SELECT id FROM `"+p+"central`.work"); got != "7" {
		t.Errorf("the berth's database lost its data after the refused reset: %q", got)
	}
	mustHave(t, m, "after the refused reset", p+"1")
	if got := other.databases(t); len(got) != 0 {
		t.Errorf("the refused reset created databases on the other server: %v", got)
	}
}

// A berth registered before v0.3 is removed with no database dropped.
func TestBerthFromBeforeDatabasesIsRemovedWithoutDropping(t *testing.T) {
	repo, m := prefixedServer(t)
	writeFile(t, repo.privateConfig(), unpublishedDB)
	wt := repo.AddWorktree(t, "feat-old")
	mustBerth(t, repo, wt, "add", "--wait")
	id := stateOf(t, repo, wt).ID
	writePreDatabaseRecord(t, repo, id)

	writeFile(t, repo.privateConfig(), hostDBConfig)
	m.create(t, "app_feat_old__central", "app_feat_old__1")
	before := strings.Join(m.databases(t), ",")

	got := removeJSON(t, repo, wt)
	for _, it := range got.Removed {
		if it.Kind == "database" {
			t.Errorf("remove of a berth made before v0.3 reports dropping database %s", it.Name)
		}
	}
	if after := strings.Join(m.databases(t), ","); after != before {
		t.Errorf("remove of a berth made before v0.3 changed the server's databases from %q to %q", before, after)
	}
}

// writePreDatabaseRecord rewrites a berth's registry record to hold only the fields
// records had before berth managed databases.
func writePreDatabaseRecord(t *testing.T, repo *Repo, id string) {
	t.Helper()
	path := filepath.Join(repo.StateDir, "berths", id+".json")
	var rec map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readString(t, path)), &rec); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	known := map[string]bool{}
	for _, k := range strings.Fields("id repository common_dir admin_dir worktree created_at name host compose_project ports compose_file files sizes volumes images builds cloned env_separator") {
		known[k] = true
	}
	var dropped []string
	for k := range rec {
		if !known[k] && !strings.Contains(k, "use") {
			dropped = append(dropped, k)
			delete(rec, k)
		}
	}
	sort.Strings(dropped)
	t.Logf("dropped record fields newer than v0.3: %v", dropped)
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data)+"\n")
}

// [env.set] or [ports] setting DB_DATABASE or the prefix key exits 5 naming
// the key.
func TestEnvSetOrPortsOnADatabaseKeyExitsFive(t *testing.T) {
	repo, m := prefixedServer(t)
	wt := repo.AddWorktree(t, "feat-x")
	before := strings.Join(m.databases(t), ",")
	cases := []struct{ name, extra, key string }{
		{"env.set DB_DATABASE", "\n[env.set]\nDB_DATABASE = \"mine\"\n", "DB_DATABASE"},
		{"env.set prefix key", "\n[env.set]\nDB_DATABASE_PREFIX = \"mine_\"\n", "DB_DATABASE_PREFIX"},
		{"ports DB_DATABASE", "\n[ports]\ntenant = { env = \"DB_DATABASE\" }\n", "DB_DATABASE"},
		{"ports prefix key", "\n[ports]\ntenant = { env = \"DB_DATABASE_PREFIX\" }\n", "DB_DATABASE_PREFIX"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			writeFile(t, repo.privateConfig(), hostDBConfig+c.extra)
			res := repo.Berth(t, wt, "add")
			if res.Exit != 5 {
				t.Errorf("add: exit %d, want 5\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
			}
			if !strings.Contains(res.Stderr, c.key) {
				t.Errorf("add: message %q does not name %s", res.Stderr, c.key)
			}
			if got := berthsAt(list(t, repo), wt); len(got) != 0 {
				t.Errorf("add with a conflicting key recorded a berth: %+v", got)
			}
			if got := strings.Join(m.databases(t), ","); got != before {
				t.Errorf("add with a conflicting key changed the server's databases from %q to %q", before, got)
			}
		})
	}
}
