//go:build e2e

package e2e

import (
	"path/filepath"
	"strings"
	"testing"
)

const exactDBConfig = unpublishedDB + `[state.mysql]
from = "env:DB_*"

[state.mysql.databases]
myapp = "fresh"
`

// exactServer is a repo whose main uses a host server holding myapp and reports, named
// in main's .env, with exactDBConfig, which lists myapp without a prefix.
func exactServer(t *testing.T) (*Repo, *hostMySQL) {
	t.Helper()
	repo := NewRepo(t)
	m := startHostMySQL(t, repo, "")
	m.create(t, "myapp", "reports")
	pointMainAt(t, repo, m, "DB_DATABASE=myapp", "REPORTS_DB=reports")
	writeFile(t, repo.privateConfig(), exactDBConfig)
	return repo, m
}

func blockValue(t *testing.T, wt, key string) string {
	t.Helper()
	return blockEnv(t, filepath.Join(wt, ".env"))[key]
}

// The guarantee without a prefix: berths feat, feat-x and feat-x-y get exact names of
// their own, none another's or a source's, and remove of each leaves the rest.
func TestExactDatabaseNamesAreTheirOwn(t *testing.T) {
	repo, m := exactServer(t)
	names := []string{"feat", "feat-x", "feat-x-y"}
	wts, got := map[string]string{}, map[string]string{}
	seen := map[string]string{"myapp": "main", "reports": "main"}
	for _, n := range names {
		wts[n] = repo.AddWorktree(t, n)
		mustBerth(t, repo, wts[n], "add", "--wait")
		got[n] = blockValue(t, wts[n], "DB_DATABASE")
		if owner, ok := seen[got[n]]; ok || got[n] == "" {
			t.Errorf("berth %s got database %q, which %s already has", n, got[n], owner)
		}
		seen[got[n]] = n
		mustHave(t, m, "after adding "+n, got[n])
	}
	for i, n := range names {
		removeJSON(t, repo, wts[n])
		if m.has(t, got[n]) {
			t.Errorf("after removing %s, its database %s remains", n, got[n])
		}
		mustHave(t, m, "after removing "+n, "myapp", "reports")
		for _, other := range names[i+1:] {
			mustHave(t, m, "after removing "+n, got[other])
		}
	}
}

// An exact name the server already has is passed over on first add, and never dropped.
func TestExactNameOnTheServerIsPassedOver(t *testing.T) {
	repo, m := exactServer(t)
	m.create(t, "myapp__feat_x")
	m.sql(t, "CREATE TABLE myapp__feat_x.kept (id INT PRIMARY KEY)")
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")
	db := blockValue(t, wt, "DB_DATABASE")
	if db == "" || db == "myapp__feat_x" || !strings.HasPrefix(db, "myapp__") || !m.has(t, db) {
		t.Errorf("berth feat-x got database %q, want one of its own other than myapp__feat_x", db)
	}
	removeJSON(t, repo, wt)
	if !m.has(t, "myapp__feat_x") || m.tables(t, "myapp__feat_x") != "1" {
		t.Errorf("myapp__feat_x, made by hand, was touched")
	}
}

// A database listed after the berth was made gets, on the next add, a name nothing on
// the server holds, and remove never drops the one that was there.
func TestNewlyListedDatabasePassesOverATakenName(t *testing.T) {
	repo, m := exactServer(t)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")
	m.create(t, "reports__feat_x")
	m.sql(t, "CREATE TABLE reports__feat_x.kept (id INT PRIMARY KEY)")

	writeFile(t, repo.privateConfig(), exactDBConfig+"reports = \"fresh\"\n")
	mustBerth(t, repo, wt, "add", "--wait")
	db := blockValue(t, wt, "REPORTS_DB")
	if db == "" || db == "reports__feat_x" || !m.has(t, db) {
		t.Errorf("the newly listed reports got %q, want a database of the berth's own other than reports__feat_x", db)
	}
	removeJSON(t, repo, wt)
	if !m.has(t, "reports__feat_x") || m.tables(t, "reports__feat_x") != "1" {
		t.Errorf("reports__feat_x, made by hand, was touched")
	}
}
