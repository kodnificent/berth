//go:build e2e

package e2e

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// commandEntry is the one place the config shape of the command strategy is written.
func commandEntry(key, script string) string {
	return fmt.Sprintf("%q = { command = %q }", key, script)
}

type commandScript struct {
	path, runsFile string
}

// writeCommand writes a script that counts its runs, does extra, then fills the target
// through root's login in the server container, so it relies on no variable beyond
// BERTH_TARGET_DB and on no mysql client on the machine.
func writeCommand(t *testing.T, m *hostMySQL, extra string) *commandScript {
	t.Helper()
	dir := t.TempDir()
	// Forward slashes, since the path goes into a script unquoted, where a backslash of
	// a Windows path would escape the next character.
	c := &commandScript{path: filepath.ToSlash(filepath.Join(dir, "copy.sh")), runsFile: filepath.Join(dir, "runs")}
	script := fmt.Sprintf(`#!/bin/sh
echo run >> '%[1]s'
%[2]s
docker exec -e MYSQL_PWD=%[3]s %[4]s mysql -h127.0.0.1 -uroot -e "CREATE DATABASE IF NOT EXISTS $BERTH_TARGET_DB; DROP TABLE IF EXISTS $BERTH_TARGET_DB.seeded; CREATE TABLE $BERTH_TARGET_DB.seeded (id INT PRIMARY KEY, run INT NOT NULL); INSERT INTO $BERTH_TARGET_DB.seeded VALUES (1, $(wc -l < '%[1]s'))" || exit 1
`, c.runsFile, extra, hostMySQLRootPassword, m.Container)
	if err := os.WriteFile(c.path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return c
}

func (c *commandScript) runs(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(c.runsFile)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

// addWithEnvCapture runs a command database through add --wait with a script that saves
// the environment it was given, and returns that environment by variable.
func addWithEnvCapture(t *testing.T, repo *Repo, m *hostMySQL, wt string) map[string]string {
	t.Helper()
	envFile := filepath.Join(t.TempDir(), "env")
	c := writeCommand(t, m, "env > '"+envFile+"'")
	writeFile(t, repo.privateConfig(), seedConfig(commandEntry("central", c.path)))
	mustBerth(t, repo, wt, "add", "--wait")
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("the command never saved its environment: %v", err)
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			env[k] = v
		}
	}
	return env
}

// serverVars finds the variables holding the server's settings by their values.
func serverVars(t *testing.T, env map[string]string, m *hostMySQL) map[string]string {
	t.Helper()
	wanted := []struct{ what, value, nameHas string }{
		{"host", "127.0.0.1", "HOST"},
		{"port", strconv.Itoa(m.Port), "PORT"},
		{"user", m.User, ""},
		{"password", m.Password, ""},
	}
	names := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names)
	found := map[string]string{}
	for _, w := range wanted {
		for _, k := range names {
			if env[k] == w.value && strings.Contains(strings.ToUpper(k), w.nameHas) {
				found[w.what] = k
				break
			}
		}
		if found[w.what] == "" {
			t.Errorf("no variable in the command's environment holds the server's %s %q", w.what, w.value)
		}
	}
	return found
}

var pathInText = regexp.MustCompile(`(?:~?/|[A-Za-z]:\\)[^\s"'<>,;()]+`)

// logNamed returns a file that text names and that contains every marker.
func logNamed(t *testing.T, text string, markers ...string) string {
	t.Helper()
	for _, p := range pathInText.FindAllString(text, -1) {
		if rest, ok := strings.CutPrefix(p, "~/"); ok {
			home, err := os.UserHomeDir()
			if err != nil {
				t.Fatal(err)
			}
			p = filepath.Join(home, rest)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		all := true
		for _, marker := range markers {
			all = all && strings.Contains(string(data), marker)
		}
		if all {
			return p
		}
	}
	return ""
}

// The script sees the source and target databases and the server's host,
// port, user and password.
func TestCommandRunsWithTheDocumentedVariablesSet(t *testing.T) {
	repo, m := dumpServer(t)
	wt := repo.AddWorktree(t, "feat-vars")

	env := addWithEnvCapture(t, repo, m, wt)

	p := berthPrefix(t, wt)
	if got := env["BERTH_SOURCE_DB"]; got != "app_central" {
		t.Errorf("BERTH_SOURCE_DB = %q, want main's app_central", got)
	}
	if got := env["BERTH_TARGET_DB"]; got != p+"central" {
		t.Errorf("BERTH_TARGET_DB = %q, want the berth's %scentral", got, p)
	}
	serverVars(t, env, m)
}

// "the documented variables" holds only if the names the script gets are
// written down where a user looks.
func TestCommandServerVariablesAreDocumented(t *testing.T) {
	repo, m := dumpServer(t)
	wt := repo.AddWorktree(t, "feat-var-docs")
	vars := serverVars(t, addWithEnvCapture(t, repo, m, wt), m)

	var docs strings.Builder
	for _, root := range []string{"../docs/design.md", "../docs/agents", "../site/src/content/docs/docs"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			docs.Write(data)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for what, name := range vars {
		if !strings.Contains(docs.String(), name) {
			t.Errorf("the variable %s, holding the server's %s, is in none of the design, the agent guides or the site docs", name, what)
		}
	}
}

// The script runs exactly once per copy, so a second add on a ready database
// does not run it over the data it made.
func TestCommandRunsOnceAcrossRepeatedAdds(t *testing.T) {
	repo, m := dumpServer(t)
	c := writeCommand(t, m, "")
	writeFile(t, repo.privateConfig(), seedConfig(commandEntry("central", c.path)))
	wt := repo.AddWorktree(t, "feat-once")

	mustBerth(t, repo, wt, "add", "--wait")
	if n := c.runs(t); n != 1 {
		t.Fatalf("after add --wait the script ran %d times, want 1", n)
	}

	mustBerth(t, repo, wt, "add", "--wait")
	if n := c.runs(t); n != 1 {
		t.Errorf("a second add --wait on a ready database ran the script again: %d runs, want 1", n)
	}
	p := berthPrefix(t, wt)
	if got := m.sql(t, "SELECT run FROM `"+p+"central`.seeded"); got != "1" {
		t.Errorf("the target holds the marker of run %q, want 1", got)
	}
}

// Exit 0 reads ready and a non-zero exit reads failed, independently per
// database, and add --wait reports the failure.
func TestCommandExitStatusDecidesReadyOrFailed(t *testing.T) {
	repo, m := dumpServer(t)
	good := writeCommand(t, m, "")
	bad := writeCommand(t, m, "exit 3")
	writeFile(t, repo.privateConfig(), seedConfig(commandEntry("central", good.path), commandEntry("reports", bad.path)))
	wt := repo.AddWorktree(t, "feat-exit")

	res := repo.Berth(t, wt, "add", "--wait")

	if res.Exit != 4 {
		t.Errorf("berth add --wait exited %d with a command failing, want 4 (state clone failed)\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	p := berthPrefix(t, wt)
	if e := dbState(t, repo, nil, wt, "central"); e.Status != "ready" || e.Strategy != "command" || e.Target != p+"central" {
		t.Errorf("central with its script exiting 0: status %q strategy %q target %q, want ready command in %scentral", e.Status, e.Strategy, e.Target, p)
	}
	if e := dbState(t, repo, nil, wt, "reports"); e.Status != "failed" || e.Strategy != "command" {
		t.Errorf("reports with its script exiting 3: status %q strategy %q, want failed command: %+v", e.Status, e.Strategy, e)
	}
}

// A command that writes the wrong rows is the script's business, but a
// failing one must never read ready.
func TestCommandFailureAfterPartialWorkNeverReadsReady(t *testing.T) {
	repo, m := dumpServer(t)
	c := writeCommand(t, m, fmt.Sprintf(`docker exec -e MYSQL_PWD=%s %s mysql -h127.0.0.1 -uroot -e "CREATE DATABASE IF NOT EXISTS $BERTH_TARGET_DB; CREATE TABLE IF NOT EXISTS $BERTH_TARGET_DB.partial (id INT)"
exit 1`, hostMySQLRootPassword, m.Container))
	writeFile(t, repo.privateConfig(), seedConfig(commandEntry("central", c.path)))
	wt := repo.AddWorktree(t, "feat-partial")

	repo.Berth(t, wt, "add", "--wait")

	if e := dbState(t, repo, nil, wt, "central"); e.Status != "failed" {
		t.Errorf("a script that wrote some tables and exited 1 reads %q, want failed: %+v", e.Status, e)
	}
}

// Reset runs the script again, once, and the database holds that run's
// result.
func TestResetRunsTheCommandAgain(t *testing.T) {
	repo, m := dumpServer(t)
	c := writeCommand(t, m, "")
	writeFile(t, repo.privateConfig(), seedConfig(commandEntry("central", c.path)))
	wt := repo.AddWorktree(t, "feat-rerun")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)

	mustBerth(t, repo, wt, "reset")
	mustBerth(t, repo, wt, "status", "--wait")

	if n := c.runs(t); n != 2 {
		t.Errorf("after reset the script ran %d times in all, want 2", n)
	}
	if e := dbState(t, repo, nil, wt, "central"); e.Status != "ready" || e.Target != p+"central" {
		t.Errorf("central after reset: status %q target %q, want ready in %scentral: %+v", e.Status, e.Target, p, e)
	}
	if got := m.sql(t, "SELECT run FROM `"+p+"central`.seeded"); got != "2" {
		t.Errorf("after reset the target holds the marker of run %q, want 2", got)
	}
}

// What the script prints, on either stream and whether it succeeds or not,
// is in a file status names.
func TestCommandOutputGoesToALogStatusNames(t *testing.T) {
	repo, m := dumpServer(t)
	good := writeCommand(t, m, `echo "out-marker-good $BERTH_TARGET_DB"; echo "err-marker-good" >&2`)
	bad := writeCommand(t, m, `echo "out-marker-bad"; echo "err-marker-bad" >&2; exit 2`)
	writeFile(t, repo.privateConfig(), seedConfig(commandEntry("central", good.path), commandEntry("reports", bad.path)))
	wt := repo.AddWorktree(t, "feat-log")
	repo.Berth(t, wt, "add", "--wait")

	text := repo.Berth(t, wt, "status").Stdout + repo.Berth(t, wt, "status", "--json").Stdout

	if logNamed(t, text, "out-marker-good", "err-marker-good") == "" {
		t.Errorf("status names no file holding the successful script's stdout and stderr:\n%s", text)
	}
	if logNamed(t, text, "out-marker-bad", "err-marker-bad") == "" {
		t.Errorf("status names no file holding the failing script's stdout and stderr:\n%s", text)
	}
}

// berth's output and files carry no password, across a command that
// succeeds, one that fails and a seed that fails. The scripts print only their markers,
// so anything the scan finds came from berth.
func TestCommandAndSeedKeepThePasswordOutOfBerthsOutput(t *testing.T) {
	repo, m := dumpServer(t)
	good := writeCommand(t, m, `echo "copying $BERTH_TARGET_DB"`)
	bad := writeCommand(t, m, `echo "failing"; exit 5`)
	writeFile(t, filepath.Join(repo.Dir, "database/seeds/bad.sql"), "THIS IS NOT SQL;\n")
	writeFile(t, repo.privateConfig(), seedConfig(
		commandEntry("central", good.path),
		commandEntry("reports", bad.path),
		seedEntry("1", "database/seeds/bad.sql"),
	))
	wt := repo.AddWorktree(t, "feat-cred")

	outputs := map[string]string{}
	run := func(args ...string) {
		res := repo.Berth(t, wt, args...)
		outputs[fmt.Sprintf("%d: berth %s", len(outputs), strings.Join(args, " "))] = res.Stdout + res.Stderr
	}
	run("add", "--wait")
	run("status")
	run("status", "--json")
	run("reset", "--dry-run")
	run("reset")
	run("status", "--wait")
	run("status", "--json")
	run("doctor")

	if e := dbState(t, repo, nil, wt, "1"); e.Status != "failed" {
		t.Fatalf("the seed with bad SQL is %q, want failed, so this test checks a failure's output: %+v", e.Status, e)
	}
	for cmd, out := range outputs {
		if strings.Contains(out, m.Password) {
			t.Errorf("%s prints the database password", cmd)
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
		if strings.Contains(string(data), m.Password) {
			t.Errorf("%s contains the database password", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Commands run in the background as dump does: add returns and status shows the
// database cloning while the script runs.
func TestAddReturnsWhileTheCommandRunsAndStatusShowsIt(t *testing.T) {
	repo, m := dumpServer(t)
	c := writeCommand(t, m, "sleep 15")
	writeFile(t, repo.privateConfig(), seedConfig(commandEntry("central", c.path)))
	wt := repo.AddWorktree(t, "feat-bg-cmd")

	start := time.Now()
	if res := repo.Berth(t, wt, "add"); res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("berth add took %s with a 15s script, want it to return while the script runs", took)
	}
	if e := dbState(t, repo, nil, wt, "central"); e.Status != "cloning" {
		t.Fatalf("central is %q while its script runs, want cloning: %+v", e.Status, e)
	}
	if human := mustBerth(t, repo, wt, "status").Stdout; !regexp.MustCompile(`database \S*central\s+cloning`).MatchString(human) {
		t.Errorf("human status doesn't show the running command:\n%s", human)
	}

	mustBerth(t, repo, wt, "status", "--wait")
	if e := dbState(t, repo, nil, wt, "central"); e.Status != "ready" {
		t.Errorf("after status --wait, central is %q, want ready: %+v", e.Status, e)
	}
}

// Recovery as dump: kill -9 while the script runs reads as failed, never ready,
// and add again runs it. The first run only sleeps, then fails, so a script orphaned by
// the kill can't write over the recovery's result.
func TestKilledCommandFailsAndAddRunsItAgain(t *testing.T) {
	repo, m := dumpServer(t)
	dir := t.TempDir()
	gate := filepath.Join(dir, "second-run")
	c := writeCommand(t, m, fmt.Sprintf(`[ -f '%[1]s' ] || { touch '%[1]s'; sleep 60; exit 1; }`, gate))
	writeFile(t, repo.privateConfig(), seedConfig(commandEntry("central", c.path)))
	wt := repo.AddWorktree(t, "feat-kill-cmd")

	if res := repo.Berth(t, wt, "add"); res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	var pid int
	waitFor(t, 30*time.Second, "the command's clone process to start", func() bool {
		e := dbState(t, repo, nil, wt, "central")
		if e.Status == "ready" {
			t.Fatalf("central reported ready while its script runs: %+v", e)
		}
		pid = e.PID
		return e.Status == "cloning" && pid > 0
	})
	waitFor(t, 30*time.Second, "the script to reach its first-run branch", func() bool {
		_, err := os.Stat(gate)
		return err == nil
	})
	if err := killNow(pid); err != nil {
		t.Fatalf("kill -9 %d: %v", pid, err)
	}
	waitFor(t, 10*time.Second, "status to report the killed command as failed", func() bool {
		e := dbState(t, repo, nil, wt, "central")
		if e.Status == "ready" {
			t.Fatalf("central reported ready after its command was killed mid-way: %+v", e)
		}
		return e.Status == "failed"
	})

	mustBerth(t, repo, wt, "add", "--wait")

	if e := dbState(t, repo, nil, wt, "central"); e.Status != "ready" {
		t.Fatalf("after add again, central is %q, want ready: %+v", e.Status, e)
	}
	if n := c.runs(t); n != 2 {
		t.Errorf("the script ran %d times in all, want 2: the killed run and the recovery", n)
	}
}
