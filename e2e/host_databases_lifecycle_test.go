//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// reset drops every database of the berth's, tenants the app made included, and makes
// its fresh ones again empty, leaving everything outside the prefix alone.
func TestResetEmptiesTheBerthsDatabasesAndDropsItsTenants(t *testing.T) {
	repo, m := prefixedServer(t)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)
	m.sql(t, "CREATE TABLE `"+p+"central`.work (id INT PRIMARY KEY)")
	m.create(t, p+"42", "app_other")

	res := mustBerth(t, repo, wt, "reset", "--json")
	var got struct {
		Discarded []struct{ Kind, Name string } `json:"discarded"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &got); err != nil {
		t.Fatalf("berth reset --json: %v\n%s", err, res.Stdout)
	}
	var listed []string
	for _, d := range got.Discarded {
		listed = append(listed, d.Kind+" "+d.Name)
	}
	if want := "database " + p + "1,database " + p + "42,database " + p + "central"; strings.Join(listed, ",") != want {
		t.Errorf("reset discarded %v, want %s", listed, want)
	}
	if under := strings.Join(m.under(t, p), ","); under != p+"1,"+p+"central" {
		t.Errorf("after reset, databases under %s = %q, want only the fresh ones made again", p, under)
	}
	if n := m.tables(t, p+"central"); n != "0" {
		t.Errorf("after reset, %scentral has %s tables, want none", p, n)
	}
	mustHave(t, m, "after reset", "app_other", "app_central", "app_1", "shop")
	if m.sql(t, "SELECT COUNT(*) FROM app_central.users") != "1" {
		t.Errorf("main's app_central lost its data after reset")
	}
}

// prune drops an orphan's databases with the login the shell exports, since the
// orphan's .env went with its worktree, and keeps the orphan until it has one.
func TestPruneDropsAnOrphansDatabasesWithTheShellsLogin(t *testing.T) {
	repo, m := prefixedServer(t)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)
	repo.Git(t, "worktree", "remove", "--force", wt)

	prune := func(env ...string) (int, string) {
		cmd := exec.Command(berthBin, "prune", "--json")
		cmd.Dir, cmd.Env = repo.Dir, append(repo.Env(), env...)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), out.String()
		} else if err != nil {
			t.Fatal(err)
		}
		return 0, out.String()
	}
	if code, out := prune(); code != 1 || !strings.Contains(out, "DB_USERNAME") {
		t.Errorf("prune with no login for the orphan's server: exit %d, want 1 naming DB_USERNAME\n%s", code, out)
	}
	mustHave(t, m, "after prune had no login", p+"central", p+"1")

	if code, out := prune("DB_USERNAME="+m.User, "DB_PASSWORD="+m.Password); code != 0 {
		t.Fatalf("prune with the login exported: exit %d\n%s", code, out)
	}
	if left := m.under(t, p); len(left) > 0 {
		t.Errorf("after prune, the orphan's databases remain: %v", left)
	}
	mustHave(t, m, "after prune", "app_central", "app_1", "shop")
}

// doctor reports a value in the berth's .env that still names one of main's databases
// the berth has its own of.
func TestDoctorReportsEnvStillNamingMainsDatabase(t *testing.T) {
	repo, _ := prefixedServer(t)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")
	if res := repo.Berth(t, wt, "doctor"); res.Exit != 0 {
		t.Fatalf("doctor right after add: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	env := filepath.Join(wt, ".env")
	writeFile(t, env, "LEGACY_DB=app_central\n"+readString(t, env))

	res := repo.Berth(t, wt, "doctor")
	if res.Exit == 0 || !strings.Contains(res.Stdout, "LEGACY_DB") || !strings.Contains(res.Stdout, "app_central") {
		t.Errorf("doctor with LEGACY_DB=app_central in the berth: exit %d, want a problem naming the key and app_central\n%s", res.Exit, res.Stdout)
	}
}
