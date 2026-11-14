//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// runningScript starts a command database whose script says it began, then waits, and
// returns the script's pid, as soon as the script has written it.
func runningScript(t *testing.T, repo *Repo, m *hostMySQL, wt, extra string) int {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "pid")
	c := writeCommand(t, m, fmt.Sprintf(`echo early-marker; %s; echo $$ > '%s'; sleep 300`, extra, pidFile))
	writeFile(t, repo.privateConfig(), seedConfig(commandEntry("central", c.path)))
	if res := repo.Berth(t, wt, "add"); res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		data, _ := os.ReadFile(pidFile)
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
			t.Cleanup(func() { killScriptGroup(pid) })
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatal("the command's script never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func killClone(t *testing.T, repo *Repo, wt string) {
	t.Helper()
	pid := dbState(t, repo, nil, wt, "central").PID
	if pid <= 0 {
		t.Fatal("status names no clone process to kill")
	}
	if err := killNow(pid); err != nil {
		t.Fatalf("kill -9 %d: %v", pid, err)
	}
}

func TestKilledCommandsScriptIsGoneByTheTimeStatusReadsFailed(t *testing.T) {
	repo, m := dumpServer(t)
	wt := repo.AddWorktree(t, "feat-orphan")
	script := runningScript(t, repo, m, wt, "true")

	killClone(t, repo, wt)
	waitFor(t, 15*time.Second, "status to report the killed command as failed", func() bool {
		return dbState(t, repo, nil, wt, "central").Status == "failed"
	})

	if alive(script) {
		t.Errorf("the script (pid %d) is still running while status reads failed", script)
	}
}

func TestRecoveryStopsTheOldScriptBeforeRunningItAgain(t *testing.T) {
	repo, m := dumpServer(t)
	wt := repo.AddWorktree(t, "feat-overlap")
	dir := t.TempDir()
	marker := filepath.Join(dir, "second-run")
	overlap := filepath.Join(dir, "overlapped")
	oldPid := filepath.Join(dir, "old-pid")
	extra := fmt.Sprintf(`if [ -f '%[1]s' ]; then kill -0 "$(cat '%[2]s')" 2>/dev/null && touch '%[3]s'; echo finished; exit 0; fi; touch '%[1]s'; echo $$ > '%[2]s'`, marker, oldPid, overlap)
	script := runningScript(t, repo, m, wt, extra)

	killClone(t, repo, wt)
	mustBerth(t, repo, wt, "add", "--wait")

	if alive(script) {
		t.Errorf("the first run's script (pid %d) is still running after the rerun", script)
	}
	if _, err := os.Stat(overlap); err == nil {
		t.Error("the rerun started while the first run's script was still running")
	}
}

func TestKilledCommandStillNamesItsLogInStatus(t *testing.T) {
	repo, m := dumpServer(t)
	wt := repo.AddWorktree(t, "feat-killed-log")
	runningScript(t, repo, m, wt, "true")

	killClone(t, repo, wt)
	waitFor(t, 15*time.Second, "status to report the killed command as failed", func() bool {
		return dbState(t, repo, nil, wt, "central").Status == "failed"
	})

	text := repo.Berth(t, wt, "status").Stdout + repo.Berth(t, wt, "status", "--json").Stdout
	if logNamed(t, text, "early-marker") == "" {
		t.Errorf("status names no file holding what the killed script printed:\n%s", text)
	}
}

func reportedBytes(t *testing.T, repo *Repo, wt, key string) int64 {
	t.Helper()
	res := repo.Berth(t, wt, "status", "--json")
	var s struct {
		State []struct {
			Kind  string `json:"kind"`
			Name  string `json:"name"`
			Bytes int64  `json:"bytes"`
		} `json:"state"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &s); err != nil {
		t.Fatalf("berth status --json: %v\n%s", err, res.Stdout)
	}
	for _, e := range s.State {
		if e.Kind == "database" && e.Name == key {
			return e.Bytes
		}
	}
	t.Fatalf("status lists no database %s:\n%s", key, res.Stdout)
	return 0
}

const (
	bigRows = 30
	bigRow  = 300_000
	bigSize = bigRows * bigRow
)

func bigInserts(table string) string {
	var b strings.Builder
	for i := 0; i < bigRows; i++ {
		fmt.Fprintf(&b, "INSERT INTO %s VALUES (%d, REPEAT('x', %d));\n", table, i, bigRow)
	}
	return b.String()
}

func TestSeedReportsTheSizeItLoaded(t *testing.T) {
	repo, _ := dumpServer(t)
	writeFile(t, filepath.Join(repo.Dir, "database/seeds/big.sql"), "CREATE TABLE big (id INT PRIMARY KEY, pad LONGBLOB);\n"+bigInserts("big"))
	writeFile(t, repo.privateConfig(), seedConfig(seedEntry("1", "database/seeds/big.sql")))
	wt := repo.AddWorktree(t, "feat-size-seed")

	mustBerth(t, repo, wt, "add", "--wait")

	if got := reportedBytes(t, repo, wt, "1"); got < bigSize {
		t.Errorf("status reports %d bytes for a seed that loaded %d", got, bigSize)
	}
}

func TestCommandReportsTheSizeItsScriptLoaded(t *testing.T) {
	repo, m := dumpServer(t)
	sql := "CREATE TABLE $BERTH_TARGET_DB.big (id INT PRIMARY KEY, pad LONGBLOB); " + strings.ReplaceAll(bigInserts("$BERTH_TARGET_DB.big"), "\n", " ")
	c := writeCommand(t, m, fmt.Sprintf(`docker exec -e MYSQL_PWD=%s %s mysql -h127.0.0.1 -uroot -e "%s" || exit 1`, hostMySQLRootPassword, m.Container, sql))
	writeFile(t, repo.privateConfig(), seedConfig(commandEntry("central", c.path)))
	wt := repo.AddWorktree(t, "feat-size-cmd")

	mustBerth(t, repo, wt, "add", "--wait")

	if got := reportedBytes(t, repo, wt, "central"); got < bigSize {
		t.Errorf("status reports %d bytes for a script that loaded %d", got, bigSize)
	}
}

func TestDumpReportsTheSizeItCopied(t *testing.T) {
	repo, m := dumpServer(t)
	m.sql(t, "CREATE TABLE app_1.big (id INT PRIMARY KEY, pad LONGBLOB); "+bigInserts("app_1.big"))
	writeFile(t, repo.privateConfig(), seedConfig(`"1" = "dump"`))
	wt := repo.AddWorktree(t, "feat-size-dump")

	mustBerth(t, repo, wt, "add", "--wait")

	if got := reportedBytes(t, repo, wt, "1"); got < bigSize {
		t.Errorf("status reports %d bytes for a copy of %d", got, bigSize)
	}
}
