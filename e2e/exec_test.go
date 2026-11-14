//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Every variable berth env prints is set inside the command, with the
// same value.
func TestExecSetsEveryEnvVariable(t *testing.T) {
	repo := NewRepo(t)
	writeFile(t, repo.privateConfig(), `[berth]
url = "http://{host}:{port.web}"

[compose.ports]
"web:80" = "assign"

[ports]
vite = { env = "VITE_PORT" }

[env]
url = ["APP_URL"]

[env.set]
GREETING = "hello {name}"
TRICKY = 'say "hi" $HOME `+"`whoami`"+` end'
`)
	wt := repo.AddWorktree(t, "feat-exec-env")
	mustBerth(t, repo, wt, "add")

	var printed struct {
		Env map[string]string `json:"env"`
	}
	out := mustBerth(t, repo, wt, "env", "--json").Stdout
	if err := json.Unmarshal([]byte(out), &printed); err != nil {
		t.Fatalf("berth env --json: %v\n%s", err, out)
	}
	if len(printed.Env) == 0 {
		t.Fatalf("berth env --json prints no variables:\n%s", out)
	}

	script := fmt.Sprintf(`for k in %s; do printf '%%s=%%s\0' "$k" "$(printenv "$k")"; done`, strings.Join(sortedKeys(printed.Env), " "))
	res := mustBerth(t, repo, wt, "exec", "--", binSh, "-c", script)
	got := map[string]string{}
	for _, kv := range strings.Split(strings.TrimSuffix(res.Stdout, "\x00"), "\x00") {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	for k, v := range printed.Env {
		if got[k] != v {
			t.Errorf("inside berth exec, %s = %q, want berth env's %q", k, got[k], v)
		}
	}
}

// The working directory: given the worktree's path from elsewhere, the
// command runs in that worktree.
func TestExecRunsInTheWorktree(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-exec-dir")
	mustBerth(t, repo, wt, "add")

	res := mustBerth(t, repo, repo.Dir, "exec", wt, "--", binSh, "-c", shPwd)
	if got := strings.TrimSuffix(res.Stdout, "\n"); got != wt {
		t.Errorf("berth exec %s ran the command in %q, want the worktree %q", wt, got, wt)
	}
}

// A non-zero exit code other than berth's own 1 comes back unchanged.
func TestExecPassesExitCodeThrough(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-exec-exit")
	mustBerth(t, repo, wt, "add")

	res := repo.Berth(t, wt, "exec", "--", binSh, "-c", "exit 7")
	if res.Exit != 7 {
		t.Errorf("berth exec of a command exiting 7 exited %d\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
}

// Stdout and stderr stay separate and byte for byte what the command wrote,
// including NUL, CR and no trailing newline.
func TestExecPassesStdoutAndStderrThrough(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-exec-streams")
	mustBerth(t, repo, wt, "add")

	res := mustBerth(t, repo, wt, "exec", "--", binSh, "-c", `printf 'out\000one\r\ntwo'; printf 'err\000three\r\nfour' >&2`)
	if want := "out\x00one\r\ntwo"; res.Stdout != want {
		t.Errorf("berth exec stdout = %q, want the command's %q", res.Stdout, want)
	}
	if want := "err\x00three\r\nfour"; res.Stderr != want {
		t.Errorf("berth exec stderr = %q, want the command's %q", res.Stderr, want)
	}
}

// Stdin: what is piped to berth exec is what the command reads.
func TestExecGivesTheCommandItsStdin(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-exec-stdin")
	mustBerth(t, repo, wt, "add")

	input := "first line\n\x00binary\xff\r\nlast, no newline"
	received := filepath.Join(t.TempDir(), "received")
	cmd := exec.Command(berthBin, "exec", "--", binSh, "-c", `cat > "$0"`, received)
	cmd.Dir = wt
	cmd.Env = repo.Env()
	cmd.Stdin = strings.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("berth exec with piped stdin: %v\n%s", err, stderr.String())
		}
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("berth exec did not finish with its stdin closed\n%s", stderr.String())
	}
	data, err := os.ReadFile(received)
	if err != nil {
		t.Fatalf("the command wrote nothing: %v", err)
	}
	if string(data) != input {
		t.Errorf("the command read %q from stdin, want what berth exec was given, %q", data, input)
	}
}

// SIGINT and SIGTERM sent to the berth exec process itself, not its
// process group, reach the command.
func TestExecForwardsSignalsToTheCommand(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-exec-signal")
	mustBerth(t, repo, wt, "add")

	for _, c := range []struct {
		name string
		sig  syscall.Signal
	}{
		{"INT", syscall.SIGINT},
		{"TERM", syscall.SIGTERM},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			ready := filepath.Join(dir, "ready")
			caught := filepath.Join(dir, "caught")
			// The loop is bounded so a command berth fails to signal still exits on its own.
			script := fmt.Sprintf(`trap 'echo %[1]s > "$1"; exit 0' %[1]s; echo > "$0"; i=0; while [ $i -lt 300 ]; do sleep 0.1; i=$((i+1)); done`, c.name)
			cmd := exec.Command(berthBin, "exec", "--", binSh, "-c", script, ready, caught)
			cmd.Dir = wt
			cmd.Env = repo.Env()
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			defer func() {
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					cmd.Process.Kill()
					<-done
				}
			}()

			deadline := time.Now().Add(30 * time.Second)
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				select {
				case err := <-done:
					done <- err
					t.Fatalf("berth exec exited before the command started: %v\n%s", err, stderr.String())
				default:
				}
				if time.Now().After(deadline) {
					t.Fatalf("the command never started\n%s", stderr.String())
				}
				time.Sleep(50 * time.Millisecond)
			}

			if err := cmd.Process.Signal(c.sig); err != nil {
				if errors.Is(err, errors.ErrUnsupported) {
					t.Skipf("this platform has no SIG%s to send one process: %v", c.name, err)
				}
				t.Fatal(err)
			}
			deadline = time.Now().Add(10 * time.Second)
			for {
				if data, err := os.ReadFile(caught); err == nil && strings.TrimSpace(string(data)) == c.name {
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("SIG%s sent to berth exec never reached the command\n%s", c.name, stderr.String())
				}
				time.Sleep(50 * time.Millisecond)
			}
		})
	}
}

// A managed key exported in the shell is the berth's value inside the
// command.
func TestExecOverridesShadowedKeys(t *testing.T) {
	repo := NewRepo(t)
	writeFile(t, repo.privateConfig(), "[env.set]\nGREETING = \"hello\"\n")
	wt := repo.AddWorktree(t, "feat-exec-shadow")
	mustBerth(t, repo, wt, "add")
	project := stateOf(t, repo, wt).ComposeProject

	res := repo.BerthWith(t, []string{"COMPOSE_PROJECT_NAME=elsewhere", "GREETING=bye"}, wt,
		"exec", "--", binSh, "-c", `printf '%s\n%s' "$GREETING" "$COMPOSE_PROJECT_NAME"`)
	if res.Exit != 0 {
		t.Fatalf("berth exec with shadowed keys: exit %d\n%s", res.Exit, res.Stderr)
	}
	want := "hello\n" + project
	if res.Stdout != want {
		t.Errorf("inside berth exec with GREETING=bye and COMPOSE_PROJECT_NAME=elsewhere exported, got %q, want the berth's %q", res.Stdout, want)
	}
}

// In a worktree with no berth, exec runs nothing and exits 1.
func TestExecWithoutBerthRunsNothing(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-exec-none")
	marker := filepath.Join(t.TempDir(), "ran")

	res := repo.Berth(t, wt, "exec", "--", binSh, "-c", `echo ran; touch "$0"`, marker)
	if res.Exit != 1 {
		t.Errorf("berth exec without a berth exited %d, want 1\nstderr:\n%s", res.Exit, res.Stderr)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("berth exec without a berth ran the command (err %v)", err)
	}
	if res.Stdout != "" {
		t.Errorf("berth exec without a berth printed to stdout:\n%s", res.Stdout)
	}
	if !strings.HasPrefix(res.Stderr, "berth: ") || !strings.Contains(res.Stderr, "no berth") {
		t.Errorf("berth exec without a berth does not say the worktree has no berth on stderr:\n%s", res.Stderr)
	}
}

// With --json, a missing berth prints the no_berth envelope on stdout
// and nothing on stderr, and the command does not run.
func TestExecJSONPrintsNoBerthEnvelope(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-exec-json")
	marker := filepath.Join(t.TempDir(), "ran")

	res := repo.Berth(t, wt, "exec", "--json", "--", binSh, "-c", `touch "$0"`, marker)
	if res.Exit != 1 {
		t.Errorf("berth exec --json without a berth exited %d, want 1", res.Exit)
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &envelope); err != nil {
		t.Fatalf("berth exec --json stdout is not one JSON object: %v\n%s", err, res.Stdout)
	}
	if envelope.Error.Code != "no_berth" || envelope.Error.Message == "" {
		t.Errorf("berth exec --json error = %+v, want code no_berth with a message\n%s", envelope.Error, res.Stdout)
	}
	if res.Stderr != "" {
		t.Errorf("berth exec --json wrote to stderr on its own failure:\n%s", res.Stderr)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("berth exec --json without a berth ran the command (err %v)", err)
	}
}

// The other side: with --json, a command that fails on its own prints no
// envelope, so its exit 1 is told apart from berth's.
func TestExecJSONLeavesCommandFailureAlone(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-exec-json-cmd")
	mustBerth(t, repo, wt, "add")

	res := repo.Berth(t, wt, "exec", "--json", "--", binSh, "-c", "printf 'plain output'; exit 1")
	if res.Exit != 1 {
		t.Errorf("berth exec --json of a command exiting 1 exited %d", res.Exit)
	}
	if res.Stdout != "plain output" {
		t.Errorf("berth exec --json stdout = %q, want only the command's %q", res.Stdout, "plain output")
	}
}
