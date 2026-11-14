//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostgresFreshDatabaseStartsEmpty(t *testing.T) {
	repo, m := prefixedPostgres(t, postgres18)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)

	if got := m.sqlIn(t, p+"central", "SELECT COUNT(*) FROM pg_tables WHERE schemaname = 'public'"); got != "0" {
		t.Errorf("the berth's fresh %scentral holds %s tables, want none of main's", p, got)
	}
	if got := blockValue(t, wt, "DB_DATABASE"); got != p+"central" {
		t.Errorf("the berth's DB_DATABASE = %q, want its own %scentral", got, p)
	}
}

func TestPostgresSkipDatabaseIsLeftAlone(t *testing.T) {
	repo := NewRepo(t)
	m := startHostPostgres(t, repo, "", postgres18)
	m.create(t, "myapp", "reports")
	pointMainAtPostgres(t, repo, m, "DB_DATABASE=myapp", "REPORTS_DB=reports")
	writeFile(t, repo.privateConfig(), unpublishedDB+`[state.postgres]
from = "env:DB_*"

[state.postgres.databases]
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

func TestPostgresNoneDatabaseIsSharedWarnedAndNeverDropped(t *testing.T) {
	repo := NewRepo(t)
	m := startHostPostgres(t, repo, "", postgres18)
	m.create(t, "myapp")
	pointMainAtPostgres(t, repo, m, "DB_DATABASE=myapp")
	writeFile(t, repo.privateConfig(), unpublishedDB+`[state.postgres]
from = "env:DB_*"

[state.postgres.databases]
myapp = "none"
`)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")

	if got := readDotEnv(t, filepath.Join(wt, ".env"))["DB_DATABASE"]; got != "myapp" {
		t.Errorf("the berth's DB_DATABASE = %q, want main's myapp", got)
	}
	doctor := repo.Berth(t, wt, "doctor", "--json")
	var d struct {
		Findings []struct{ Level, Location, Message string } `json:"findings"`
	}
	if err := json.Unmarshal([]byte(doctor.Stdout), &d); err != nil {
		t.Fatalf("berth doctor --json: %v\n%s", err, doctor.Stdout)
	}
	warned := false
	for _, f := range d.Findings {
		warned = warned || f.Level == "warning" && strings.Contains(f.Location+" "+f.Message, "myapp")
	}
	if !warned {
		t.Errorf("doctor gives no warning naming myapp, which the berth shares with main:\n%s", doctor.Stdout)
	}

	removeJSON(t, repo, wt)
	m.mustHave(t, "after removing a berth that used main's database through none", "myapp")
}

func TestPostgresCommandRunsWithTheServerSettingsAndFillsTheBerthsDatabase(t *testing.T) {
	repo, m := prefixedPostgres(t, postgres18)
	dir := t.TempDir()
	// Forward slashes, since the command runs unquoted, where a backslash of a Windows
	// path would escape the next character.
	envFile, script := filepath.Join(dir, "env"), filepath.ToSlash(filepath.Join(dir, "fill.sh"))
	writeFile(t, script, fmt.Sprintf(`#!/bin/sh
env > '%s'
docker exec -e PGPASSWORD=hostroot %s psql -h 127.0.0.1 -U postgres -d "$BERTH_TARGET_DB" -v ON_ERROR_STOP=1 -c "CREATE TABLE seeded (id INT)" || exit 1
`, envFile, m.Container))
	chmod(t, script, 0o755)
	writeFile(t, repo.privateConfig(), unpublishedDB+`[state.postgres]
from = "env:DB_*"
prefix = "env:DB_DATABASE_PREFIX"

[state.postgres.databases]
`+commandEntry("central", script)+"\n")
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)

	if got := m.sqlIn(t, p+"central", "SELECT COUNT(*) FROM pg_tables WHERE tablename = 'seeded'"); got != "1" {
		t.Errorf("the berth's %scentral has %s tables named seeded after the command ran, want 1", p, got)
	}
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("the command never saved its environment: %v", err)
	}
	for _, want := range []string{
		"BERTH_SOURCE_DB=app_central", "BERTH_TARGET_DB=" + p + "central",
		"BERTH_DB_HOST=127.0.0.1", fmt.Sprintf("BERTH_DB_PORT=%d", m.Port),
		"BERTH_DB_USER=" + m.User, "BERTH_DB_PASSWORD=" + m.Password,
	} {
		if !strings.Contains(string(data), want+"\n") {
			t.Errorf("the command's environment has no %s", want)
		}
	}
}

func TestDatabaseURLLoginIsNeverStoredOrPrinted(t *testing.T) {
	repo := NewRepo(t)
	m := startHostPostgres(t, repo, "", postgres18)
	m.create(t, "myapp")
	mainURL := fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/myapp?sslmode=disable", m.User, m.Password, m.Port)
	env := readDotEnv(t, filepath.Join(repo.Dir, ".env"))
	env["DATABASE_URL"] = mainURL
	writeDotEnv(t, filepath.Join(repo.Dir, ".env"), env)
	writeFile(t, repo.privateConfig(), `[state.postgres]
from = "env:DATABASE_URL"

[state.postgres.databases]
myapp = "fresh"
`)
	wt := repo.AddWorktree(t, "feat-x")
	add := mustBerth(t, repo, wt, "add", "--wait")
	outputs := map[string]string{"add": add.Stdout + add.Stderr}
	for _, args := range [][]string{
		{"status"}, {"status", "--json"}, {"list"}, {"list", "--json"}, {"env", "--json"},
		{"doctor"}, {"doctor", "--json"}, {"reset", "--json"}, {"remove", "--dry-run", "--json"},
	} {
		res := repo.Berth(t, wt, args...)
		outputs[strings.Join(args, " ")] = res.Stdout + res.Stderr
	}
	// An exported URL shadows the berth's, and both are shown as shadowing.
	shadow := repo.BerthWith(t, []string{"DATABASE_URL=" + mainURL}, wt, "status", "--json")
	outputs["status --json with DATABASE_URL exported"] = shadow.Stdout + shadow.Stderr

	check := func(where, content string) {
		for _, s := range []string{m.Password, m.User} {
			if strings.Contains(content, s) {
				t.Errorf("%s contains the credential %q", where, s)
			}
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

func TestBerthEnvPrintsTheAppsURLWhileEnvJSONAndStatusRedactIt(t *testing.T) {
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
	appURL := blockEnv(t, filepath.Join(wt, ".env"))["DATABASE_URL"]

	// eval "$(berth env)" must give the shell the URL the app reads from .env.
	if out := mustBerth(t, repo, wt, "env").Stdout; !strings.Contains(out, "export DATABASE_URL='"+appURL+"'") {
		t.Errorf("berth env does not export the berth's DATABASE_URL %q:\n%s", appURL, out)
	}
	for _, args := range [][]string{{"env", "--json"}, {"status"}, {"status", "--json"}} {
		out := mustBerth(t, repo, wt, args...).Stdout
		if strings.Contains(out, m.Password) {
			t.Errorf("berth %s prints the URL's password", strings.Join(args, " "))
		}
		if !strings.Contains(out, "127.0.0.1:"+fmt.Sprint(m.Port)+"/myapp__feat_x") {
			t.Errorf("berth %s does not show the berth's URL with its login taken out:\n%s", strings.Join(args, " "), out)
		}
	}
}
