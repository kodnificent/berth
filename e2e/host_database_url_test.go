//go:build e2e

package e2e

import (
	"fmt"
	"net/url"
	"path/filepath"
	"testing"
)

func expectBerthURL(t *testing.T, wt, scheme, user, password string, port int, database string) {
	t.Helper()
	raw := blockEnv(t, filepath.Join(wt, ".env"))["DATABASE_URL"]
	u, err := url.Parse(raw)
	if err != nil || raw == "" {
		t.Fatalf("the managed block's DATABASE_URL = %q, want a URL: %v", raw, err)
	}
	gotPassword, _ := u.User.Password()
	if u.Scheme != scheme || u.Hostname() != "127.0.0.1" || u.Port() != fmt.Sprint(port) || u.User.Username() != user || gotPassword != password {
		t.Errorf("the berth's DATABASE_URL = %q, want main's %s server 127.0.0.1:%d and login", raw, scheme, port)
	}
	if u.Path != "/"+database {
		t.Errorf("the berth's DATABASE_URL names database %q, want %q", u.Path, "/"+database)
	}
}

func TestDatabaseURLKeyPointsAMySQLBerthAtItsOwnDatabase(t *testing.T) {
	repo := NewRepo(t)
	m := startHostMySQL(t, repo, "")
	m.create(t, "myapp")
	env := readDotEnv(t, filepath.Join(repo.Dir, ".env"))
	env["DATABASE_URL"] = fmt.Sprintf("mysql://%s:%s@127.0.0.1:%d/myapp", m.User, m.Password, m.Port)
	writeDotEnv(t, filepath.Join(repo.Dir, ".env"), env)
	writeFile(t, repo.privateConfig(), `[state.mysql]
from = "env:DATABASE_URL"

[state.mysql.databases]
myapp = "fresh"
`)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")

	expectBerthURL(t, wt, "mysql", m.User, m.Password, m.Port, "myapp__feat_x")
	mustHave(t, m, "after add", "myapp", "myapp__feat_x")
}

func TestDatabaseURLKeyPointsAPostgresBerthAtItsOwnDatabase(t *testing.T) {
	repo := NewRepo(t)
	m := startHostPostgres(t, repo, "", postgres18)
	m.create(t, "myapp")
	env := readDotEnv(t, filepath.Join(repo.Dir, ".env"))
	env["DATABASE_URL"] = fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/myapp", m.User, m.Password, m.Port)
	writeDotEnv(t, filepath.Join(repo.Dir, ".env"), env)
	writeFile(t, repo.privateConfig(), `[state.postgres]
from = "env:DATABASE_URL"

[state.postgres.databases]
myapp = "fresh"
`)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")

	expectBerthURL(t, wt, "postgres", m.User, m.Password, m.Port, "myapp__feat_x")
	m.mustHave(t, "after add", "myapp", "myapp__feat_x")
}
