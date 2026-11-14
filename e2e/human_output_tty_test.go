//go:build e2e && unix

package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

var sgr = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// startOnPty runs berth with a pseudo-terminal as its stdout only, stderr captured apart
// and stdin empty, and returns a function that waits for it to exit.
func startOnPty(t *testing.T, repo *Repo, env []string, dir string, args ...string) func() Result {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("opening a pty: %v", err)
	}
	cmd := exec.Command(berthBin, args...)
	cmd.Dir = dir
	for _, kv := range repo.Env() {
		if !strings.HasPrefix(kv, "NO_COLOR=") && !strings.HasPrefix(kv, "TERM=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, env...)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = slave, &stderr
	if err := cmd.Start(); err != nil {
		master.Close()
		slave.Close()
		t.Fatalf("berth %v: %v", args, err)
	}
	slave.Close()
	read := make(chan []byte, 1)
	go func() {
		// Linux ends a pty master's output with EIO once the child's side closes.
		out, _ := io.ReadAll(master)
		read <- out
	}()
	return func() Result {
		t.Helper()
		defer master.Close()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		var err error
		select {
		case err = <-done:
		case <-time.After(5 * time.Minute):
			_ = cmd.Process.Kill()
			t.Fatalf("berth %v on a pty did not exit in 5 minutes", args)
		}
		var out []byte
		select {
		case out = <-read:
		case <-time.After(30 * time.Second):
			t.Fatalf("berth %v exited but its pty output never ended", args)
		}
		res := Result{Stdout: strings.ReplaceAll(string(out), "\r\n", "\n"), Stderr: stderr.String()}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.Exit = exitErr.ExitCode()
		} else if err != nil {
			t.Fatalf("berth %v: %v", args, err)
		}
		return res
	}
}

func onPty(t *testing.T, repo *Repo, env []string, dir string, args ...string) Result {
	t.Helper()
	return startOnPty(t, repo, env, dir, args...)()
}

func mustOnPty(t *testing.T, repo *Repo, env []string, dir string, args ...string) Result {
	t.Helper()
	res := onPty(t, repo, env, dir, args...)
	if res.Exit != 0 {
		t.Fatalf("berth %v on a pty: exit %d\nstdout:\n%q\nstderr:\n%s", args, res.Exit, res.Stdout, res.Stderr)
	}
	return res
}

// cacheStyle is the escape sequences on the first row for the cache volume, in order.
func cacheStyle(t *testing.T, what, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(sgr.ReplaceAllString(line, "")); len(f) >= 2 && f[0] == "volume" && f[1] == "cache" {
			return strings.Join(sgr.FindAllString(line, -1), "")
		}
	}
	t.Fatalf("%s has no row for the cache volume:\n%q", what, out)
	return ""
}

var colour = []string{"TERM=xterm-256color"}

// On a terminal, ready, cloning and failed each carry a colour of their own,
// in add and in status.
func TestHumanOutputTerminalColoursEachState(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	out := map[string]map[string]string{"add": {}, "status": {}}

	ready := repo.AddWorktree(t, "feat-tty-ready")
	out["add"]["ready"] = mustOnPty(t, repo, colour, ready, "add", "--wait").Stdout
	out["status"]["ready"] = mustOnPty(t, repo, colour, ready, "status").Stdout

	cloning := repo.AddWorktree(t, "feat-tty-cloning")
	out["add"]["cloning"] = mustOnPty(t, repo, append([]string{cloneDelay + "=60s"}, colour...), cloning, "add").Stdout
	if e := stateOf(t, repo, cloning).volume(t, "cache"); e.Status != "cloning" {
		t.Fatalf("cache is %q right after add, want cloning", e.Status)
	}
	out["status"]["cloning"] = mustOnPty(t, repo, colour, cloning, "status").Stdout

	failed := repo.AddWorktree(t, "feat-tty-failed")
	wait := startOnPty(t, repo, append([]string{cloneDelay + "=120s"}, colour...), failed, "add", "--wait")
	var pid int
	waitFor(t, 30*time.Second, "the clone process to start", func() bool {
		res := repo.Berth(t, failed, "status", "--json")
		var s stateStatus
		if json.Unmarshal([]byte(res.Stdout), &s) != nil {
			return false
		}
		for _, e := range s.State {
			if e.Name == "cache" && e.PID > 0 && e.Status == "cloning" {
				pid = e.PID
			}
		}
		return pid > 0
	})
	if err := killNow(pid); err != nil {
		t.Fatalf("kill -9 %d: %v", pid, err)
	}
	res := wait()
	if res.Exit != 4 {
		t.Fatalf("berth add --wait exited %d after its clone was killed, want 4:\n%s", res.Exit, res.Stderr)
	}
	out["add"]["failed"] = res.Stdout
	waitFor(t, 10*time.Second, "status to report the killed clone as failed", func() bool {
		return stateOf(t, repo, failed).volume(t, "cache").Status == "failed"
	})
	out["status"]["failed"] = onPty(t, repo, colour, failed, "status").Stdout

	for _, cmd := range []string{"add", "status"} {
		style := map[string]string{}
		for _, state := range []string{"ready", "cloning", "failed"} {
			o := out[cmd][state]
			if !strings.Contains(o, "\x1b") {
				t.Errorf("berth %s on a terminal with a %s clone holds no escape byte:\n%q", cmd, state, o)
				continue
			}
			style[state] = cacheStyle(t, "berth "+cmd+" with a "+state+" clone", o)
		}
		if len(style) < 3 {
			continue
		}
		for _, pair := range [][2]string{{"ready", "cloning"}, {"ready", "failed"}, {"cloning", "failed"}} {
			if style[pair[0]] == style[pair[1]] {
				t.Errorf("berth %s styles a %s and a %s cache row alike: %q", cmd, pair[0], pair[1], style[pair[0]])
			}
		}
	}
}

// NO_COLOR or TERM=dumb leaves the summary and status unstyled on a terminal.
func TestHumanOutputNoStylingWhenAskedOnATerminal(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	for _, c := range []struct {
		name string
		env  []string
	}{
		{"no-color", []string{"TERM=xterm-256color", "NO_COLOR=1"}},
		{"dumb", []string{"TERM=dumb"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			wt := repo.AddWorktree(t, "feat-tty-"+c.name)
			add := mustOnPty(t, repo, c.env, wt, "add", "--wait")
			_, rows := summaryOf(t, add.Stdout)
			if len(rowsLabelled(rows, "volume cache")) != 1 {
				t.Errorf("add's summary on a terminal has no cache row:\n%q", add.Stdout)
			}
			assertNoEscape(t, "berth add on a terminal", add.Stdout)
			assertNoEscape(t, "berth status on a terminal", mustOnPty(t, repo, c.env, wt, "status").Stdout)
		})
	}
}

// --json output on a terminal is the same plain JSON.
func TestHumanOutputJSONPlainOnATerminal(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-tty-json")
	for _, args := range [][]string{{"add", "--json"}, {"status", "--json"}} {
		res := mustOnPty(t, repo, colour, wt, args...)
		assertNoEscape(t, "berth "+strings.Join(args, " ")+" on a terminal", res.Stdout)
		var m map[string]any
		if err := json.Unmarshal([]byte(res.Stdout), &m); err != nil || m["id"] == nil {
			t.Errorf("berth %v on a terminal is not its JSON: %v\n%q", args, err, res.Stdout)
		}
	}
}
