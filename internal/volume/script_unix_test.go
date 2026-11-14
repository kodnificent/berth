//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package volume

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

const testMark = "berth-command-test0123"

func startGroup(t *testing.T) (*exec.Cmd, <-chan error) {
	t.Helper()
	cmd := exec.Command("sh", "-c", "sleep 300; exit $?", testMark)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return cmd, done
}

func TestStopScriptStopsTheRunItRecorded(t *testing.T) {
	cmd, done := startGroup(t)

	if err := stopScript(cmd.Process.Pid, testMark); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the script is still running")
	}
}

func TestStopScriptLeavesAProcessThatIsNotTheRecordedRun(t *testing.T) {
	cmd, done := startGroup(t)

	if err := stopScript(cmd.Process.Pid, "berth-command-another"); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
		t.Fatal("stopped a process that doesn't carry the recorded run's mark")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestStopScriptLeavesAProcessWithNoRecordedMark(t *testing.T) {
	cmd, done := startGroup(t)

	if err := stopScript(cmd.Process.Pid, ""); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
		t.Fatal("stopped a process with no recorded mark")
	case <-time.After(200 * time.Millisecond):
	}
}
