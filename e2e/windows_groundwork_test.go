//go:build e2e

package e2e

import (
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func onlyOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("Windows only")
	}
}

// needLinuxContainers skips a test on a Windows machine whose Docker can't run the
// Linux images the fixtures use. Elsewhere TestMain has already required Docker.
func needLinuxContainers(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		return
	}
	out, err := exec.Command("docker", "info", "--format", "{{.OSType}}").Output()
	if err != nil || strings.TrimSpace(string(out)) != "linux" {
		t.Skipf("needs Docker running Linux containers (%q, %v)", strings.TrimSpace(string(out)), err)
	}
}

// forceKill ends one process the way a crash does: no signal it can handle, and nothing
// it started goes with it.
func forceKill(pid int) error {
	if runtime.GOOS == "windows" {
		return exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run()
	}
	return exec.Command("kill", "-9", strconv.Itoa(pid)).Run()
}

// Git for Windows' sh reports its own pids in $$ and $!, not Windows', so the command
// line is what identifies a process on every platform.
func processesWith(t *testing.T, marker string) []string {
	t.Helper()
	cmd := exec.Command("ps", "-A", "-ww", "-o", "pid=", "-o", "command=")
	if runtime.GOOS == "windows" {
		cmd = exec.Command("powershell", "-NoProfile", "-Command", `Get-CimInstance Win32_Process | ForEach-Object { "$($_.ProcessId) $($_.CommandLine)" }`)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("listing processes: %v", err)
	}
	var found []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, marker) {
			found = append(found, strings.TrimSpace(line))
		}
	}
	return found
}

func killProcessesWith(t *testing.T, marker string) {
	t.Helper()
	for _, p := range processesWith(t, marker) {
		if pid, err := strconv.Atoi(strings.Fields(p)[0]); err == nil {
			_ = forceKill(pid)
		}
	}
}

func expectProcessesGone(t *testing.T, marker string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		left := processesWith(t, marker)
		if len(left) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("still running 10s later:\n%s", strings.Join(left, "\n"))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// The marker doubles as the sleep's duration and the script's argument, so it is in the
// command line of the script and of every process under it.
func startScriptWithChildren(t *testing.T, repo *Repo, m *hostMySQL, wt string) string {
	t.Helper()
	marker := fmt.Sprintf("8000.%d", rand.Uint32())
	t.Cleanup(func() { killProcessesWith(t, marker) })
	ready := filepath.Join(t.TempDir(), "ready")
	c := writeCommand(t, m, fmt.Sprintf(`sh -c 'sleep %[1]s; :' & sh -c 'sleep %[1]s; :' & touch '%[2]s'; wait`, marker, ready))
	writeFile(t, repo.privateConfig(), seedConfig(commandEntry("central", shellQuote(c.path)+" "+marker)))
	if res := repo.Berth(t, wt, "add"); res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	waitFor(t, 30*time.Second, "the script to start its children", func() bool { return fileExistsAt(ready) })
	if got := processesWith(t, marker); len(got) < 3 {
		t.Fatalf("found %d processes of the script, want the script and its children, so this proves nothing:\n%s", len(got), strings.Join(got, "\n"))
	}
	return marker
}

// gitWithoutSh is the environment of a machine whose git has no sh: PATH holds only that
// git and the system directory. BERTH_TEST_GIT_WITHOUT_SH is the root of such a git
// install, such as MinGit with any sh.exe and bash.exe taken out.
func gitWithoutSh(t *testing.T) []string {
	t.Helper()
	onlyOnWindows(t)
	root := os.Getenv("BERTH_TEST_GIT_WITHOUT_SH")
	if root == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("BERTH_TEST_GIT_WITHOUT_SH is not set; the Windows CI job must point it at a git install with no sh")
		}
		t.Skip("BERTH_TEST_GIT_WITHOUT_SH is not set")
	}
	err := filepath.WalkDir(root, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name := strings.ToLower(filepath.Base(path)); name == "sh.exe" || name == "bash.exe" {
			return fmt.Errorf("found %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("BERTH_TEST_GIT_WITHOUT_SH must be a git install with no sh: %v", err)
	}
	system := filepath.Join(os.Getenv("SystemRoot"), "System32")
	return []string{"PATH=" + filepath.Join(root, "cmd") + string(os.PathListSeparator) + system}
}

var namesSh = regexp.MustCompile(`\bsh(\.exe)?\b`)

func TestListAndStatusDoNotWaitForAnAddThatIsCopying(t *testing.T) {
	repo := NewRepo(t)
	ignoreSQLite(t, repo)
	rel := "database/app.sqlite"
	makeLedger(t, filepath.Join(repo.Dir, rel), "delete", 50, 100)
	writeFile(t, repo.privateConfig(), sqliteConfig(rel))
	other := repo.AddWorktree(t, "feat-other")
	mustBerth(t, repo, other, "add")
	busy := repo.AddWorktree(t, "feat-busy")

	// Outlasts all three commands at their limit, so each ran during the copy.
	wait := addHolding(t, repo, busy, rel, 20*time.Second)
	for _, c := range []struct {
		dir  string
		args []string
	}{
		{repo.Dir, []string{"list"}},
		{other, []string{"status"}},
		{busy, []string{"status"}},
	} {
		if _, took := timedBerth(t, repo, c.dir, c.args...); took > 5*time.Second {
			t.Errorf("berth %s in %s took %s while another add was copying, want it to return promptly", strings.Join(c.args, " "), c.dir, took)
		}
	}
	wait()
}

func TestCloneKilledMidCopyReadsFailedAndAddRecovers(t *testing.T) {
	needLinuxContainers(t)
	repo := NewRepo(t)
	src := fillCache(t, repo)
	want := checksum(t, src)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	wt := repo.AddWorktree(t, "feat-killed-copy")

	if res := berthWithDelay(t, repo, wt, "120s", "add"); res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s", res.Exit, res.Stderr)
	}
	e := stateOf(t, repo, wt).volume(t, "cache")
	if e.Status != "cloning" || e.PID <= 0 {
		t.Fatalf("right after add, cache is %q with pid %d, want cloning with the clone process's pid", e.Status, e.PID)
	}
	waitFor(t, 30*time.Second, "the copy to reach main's volume", func() bool {
		out, err := exec.Command("docker", "ps", "-q", "--filter", "volume="+src).Output()
		return err == nil && strings.TrimSpace(string(out)) != ""
	})

	if err := forceKill(e.PID); err != nil {
		t.Fatalf("killing the clone process %d: %v", e.PID, err)
	}
	waitFor(t, 15*time.Second, "status to report the killed clone as failed", func() bool {
		return stateOf(t, repo, wt).volume(t, "cache").Status == "failed"
	})

	mustBerth(t, repo, wt, "add", "--wait")
	e = stateOf(t, repo, wt).volume(t, "cache")
	if e.Status != "ready" {
		t.Fatalf("after add again, cache is %q, want ready: %+v", e.Status, e)
	}
	if got := checksum(t, e.Volume); got != want {
		t.Errorf("after recovery the berth's cache differs from main's:\nmain:\n%s\nberth:\n%s", want, got)
	}
}

func TestFailedBerthLeavesNoneOfItsScriptsProcessesRunning(t *testing.T) {
	needLinuxContainers(t)
	repo, m := dumpServer(t)
	wt := repo.AddWorktree(t, "feat-failed-tree")
	marker := startScriptWithChildren(t, repo, m, wt)

	pid := dbState(t, repo, nil, wt, "central").PID
	if pid <= 0 {
		t.Fatal("status names no clone process to kill")
	}
	if err := forceKill(pid); err != nil {
		t.Fatalf("killing the clone process %d: %v", pid, err)
	}
	waitFor(t, 15*time.Second, "status to report the killed command as failed", func() bool {
		return dbState(t, repo, nil, wt, "central").Status == "failed"
	})

	expectProcessesGone(t, marker)
}

func TestRemovedBerthLeavesNoneOfItsScriptsProcessesRunning(t *testing.T) {
	needLinuxContainers(t)
	repo, m := dumpServer(t)
	wt := repo.AddWorktree(t, "feat-removed-tree")
	marker := startScriptWithChildren(t, repo, m, wt)

	mustBerth(t, repo, wt, "remove")

	expectProcessesGone(t, marker)
}

// cmd.exe has no $( ) or [ ], so only a POSIX shell prints sh-ran.
const shOnly = `v=$(printf sh-ran); [ -n "$v" ] && printf '%s\n' "$v"`

func TestPostAddRunsThroughSh(t *testing.T) {
	repo := NewRepo(t)
	writeFile(t, repo.privateConfig(), "[hooks]\npost-add = "+tomlString(shOnly)+"\n")
	wt := repo.AddWorktree(t, "feat-sh-post-add")
	mustBerth(t, repo, wt, "add")

	pa := waitPostAdd(t, repo, wt)
	if pa.Status != "ok" || !strings.Contains(pa.Output, "sh-ran") {
		t.Errorf("post-add ended %q with output %q, want ok and sh-ran: %+v", pa.Status, pa.Output, pa)
	}
}

func TestPreRemoveRunsThroughSh(t *testing.T) {
	repo := NewRepo(t)
	writeFile(t, repo.privateConfig(), "[hooks]\npre-remove = "+tomlString(shOnly)+"\n")
	wt := repo.AddWorktree(t, "feat-sh-pre-remove")
	mustBerth(t, repo, wt, "add")

	res := mustBerth(t, repo, wt, "remove")

	if !strings.Contains(res.Stdout+res.Stderr, "sh-ran") {
		t.Errorf("remove doesn't show pre-remove's output sh-ran:\n%s%s", res.Stdout, res.Stderr)
	}
}

func TestCommandRunsThroughSh(t *testing.T) {
	needLinuxContainers(t)
	repo, _ := dumpServer(t)
	out := filepath.Join(t.TempDir(), "out")
	writeFile(t, repo.privateConfig(), seedConfig(commandEntry("central", shOnly+" > "+shellQuote(out))))
	wt := repo.AddWorktree(t, "feat-sh-command")

	mustBerth(t, repo, wt, "add", "--wait")

	if e := dbState(t, repo, nil, wt, "central"); e.Status != "ready" {
		t.Fatalf("central is %q after add --wait, want ready: %+v", e.Status, e)
	}
	if got := readString(t, out); !strings.Contains(got, "sh-ran") {
		t.Errorf("the command's script wrote %q, want sh-ran", got)
	}
}

func TestPostAddWithNoShFailsNamingIt(t *testing.T) {
	noSh := gitWithoutSh(t)
	repo := NewRepo(t)
	writeFile(t, repo.privateConfig(), "[hooks]\npost-add = \"true\"\n")
	wt := repo.AddWorktree(t, "feat-no-sh-post-add")

	if res := repo.BerthWith(t, noSh, wt, "add"); res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}

	pa := waitPostAdd(t, repo, wt)
	if pa.Status != "failed" || !namesSh.MatchString(pa.Error+pa.Output) {
		t.Errorf("post-add with no sh: %+v, want failed, naming sh", pa)
	}
}

func TestPreRemoveWithNoShFailsNamingItAndRemovesNothing(t *testing.T) {
	noSh := gitWithoutSh(t)
	repo := NewRepo(t)
	writeFile(t, repo.privateConfig(), "[hooks]\npre-remove = \"true\"\n")
	wt := repo.AddWorktree(t, "feat-no-sh-pre-remove")
	mustBerth(t, repo, wt, "add")

	res := repo.BerthWith(t, noSh, wt, "remove")

	if res.Exit == 0 || !namesSh.MatchString(res.Stderr) {
		t.Errorf("remove with no sh: exit %d, want non-zero, naming sh\n%s", res.Exit, res.Stderr)
	}
	if got := berthsAt(list(t, repo), wt); len(got) != 1 {
		t.Errorf("remove with no sh left %d berths at %s, want the berth kept", len(got), wt)
	}
}

func TestCommandWithNoShFailsNamingIt(t *testing.T) {
	noSh := gitWithoutSh(t)
	needLinuxContainers(t)
	repo, _ := dumpServer(t)
	writeFile(t, repo.privateConfig(), seedConfig(commandEntry("central", "true")))
	wt := repo.AddWorktree(t, "feat-no-sh-command")

	if res := repo.BerthWith(t, noSh, wt, "add"); res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}

	var e dbClone
	waitFor(t, 30*time.Second, "the command to fail", func() bool {
		e = dbState(t, repo, noSh, wt, "central")
		return e.Status == "failed"
	})
	if !namesSh.MatchString(e.Error) {
		t.Errorf("the failed command's error %q doesn't name sh", e.Error)
	}
}

func TestHookInstallWithNoShRefusesNamingItAndWritesNoHook(t *testing.T) {
	noSh := gitWithoutSh(t)
	repo := NewRepo(t)

	res := repo.BerthWith(t, noSh, repo.Dir, "hook", "install")

	if res.Exit == 0 || !namesSh.MatchString(res.Stdout+res.Stderr) {
		t.Errorf("hook install with no sh: exit %d, want non-zero, naming sh\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	hook := filepath.Join(repo.Dir, ".git", "hooks", "post-checkout")
	if _, err := os.Stat(hook); err == nil {
		t.Errorf("hook install with no sh wrote %s", hook)
	}
}

func TestHookInstallWithNoShRefusesNamingItWhenHooksPathIsInsideTheWorkTree(t *testing.T) {
	noSh := gitWithoutSh(t)
	repo := NewRepo(t)
	repo.Git(t, "config", "core.hooksPath", ".husky")
	existing := "echo husky ran\n"
	hook := filepath.Join(repo.Dir, ".husky", "post-checkout")
	// With no hook here, the instructions berth prints hold #!/bin/sh, which namesSh would match.
	writeFile(t, hook, existing)

	res := repo.BerthWith(t, noSh, repo.Dir, "hook", "install")

	if res.Exit == 0 || !namesSh.MatchString(res.Stdout+res.Stderr) {
		t.Errorf("hook install with no sh and core.hooksPath in the work tree: exit %d, want non-zero, naming sh\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	if got, err := os.ReadFile(hook); err != nil || string(got) != existing {
		t.Errorf("hook install with no sh changed %s: %q, %v", hook, got, err)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, ".git", "hooks", "post-checkout")); err == nil {
		t.Errorf("hook install with no sh wrote a hook in .git/hooks")
	}
}

func TestRegistryLivesUnderLocalAppData(t *testing.T) {
	onlyOnWindows(t)
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-local-app-data")
	appData := t.TempDir()
	env := []string{"BERTH_STATE_DIR=", "XDG_STATE_HOME=", "LOCALAPPDATA=" + appData}

	if res := repo.BerthWith(t, env, wt, "add"); res.Exit != 0 {
		t.Fatalf("add with LOCALAPPDATA=%s: exit %d\n%s", appData, res.Exit, res.Stderr)
	}

	dir := filepath.Join(appData, "berth")
	if entries, err := os.ReadDir(dir); err != nil || len(entries) == 0 {
		t.Errorf("add left nothing under %s (err %v)", dir, err)
	}
	if n := countBerths(t, repo, env); n != 1 {
		t.Errorf("list with the same LOCALAPPDATA shows %d berths, want the one add made", n)
	}
	if n := countBerths(t, repo, []string{"BERTH_STATE_DIR=", "XDG_STATE_HOME=", "LOCALAPPDATA=" + t.TempDir()}); n != 0 {
		t.Errorf("list with another LOCALAPPDATA shows %d berths, want none", n)
	}
}

func TestManagedBlockJoinsComposeFileWithSemicolon(t *testing.T) {
	onlyOnWindows(t)
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-compose-file")

	mustBerth(t, repo, wt, "add")

	got := strings.Split(blockValue(t, wt, "COMPOSE_FILE"), ";")
	if len(got) != 2 || filepath.Base(got[0]) != "compose.yml" || filepath.Base(got[1]) != "compose.berth.yml" {
		t.Errorf("COMPOSE_FILE split on ; is %q, want compose.yml then compose.berth.yml", got)
	}
}
