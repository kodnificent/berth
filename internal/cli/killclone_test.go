package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestMain lets the test binary stand in for a clone process: run as "__clone <id>",
// it lives until its stdin closes, then exits 0.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == cloneCommand {
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	if len(os.Args) == 2 && os.Args[1] == proxyChildCommand {
		err := runProxy(&Context{Stdout: os.Stdout, Stderr: os.Stderr}, nil)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// startClone starts exe as the clone of id, detached as add starts one, and returns its
// pid, the pipe whose closing ends it, and its exit code once it has ended.
func startClone(t *testing.T, exe, id string) (int, io.Closer, <-chan int) {
	t.Helper()
	cmd := exec.Command(exe, cloneCommand, id)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	detach(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	exit := make(chan int, 1)
	go func() {
		_ = cmd.Wait()
		exit <- cmd.ProcessState.ExitCode()
	}()
	return cmd.Process.Pid, stdin, exit
}

func exitCode(t *testing.T, exit <-chan int) int {
	t.Helper()
	select {
	case code := <-exit:
		return code
	case <-time.After(10 * time.Second):
		t.Fatal("the process is still running 10s later")
		return 0
	}
}

func self(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestKillCloneEndsTheBerthsCloneProcess(t *testing.T) {
	pid, _, exit := startClone(t, self(t), "a1b2c3")

	if err := killClone("a1b2c3", pid); err != nil {
		t.Fatal(err)
	}

	if code := exitCode(t, exit); code == 0 {
		t.Error("the clone process ran to its end, want it killed")
	}
}

func TestKillCloneLeavesAProcessThatIsNotTheBerthsClone(t *testing.T) {
	pid, stdin, exit := startClone(t, self(t), "a1b2c3d4")

	if err := killClone("b2c3d4", pid); err != nil {
		t.Fatal(err)
	}

	stdin.Close()
	if code := exitCode(t, exit); code != 0 {
		t.Errorf("the process exited %d, want it left to run to its end", code)
	}
}
