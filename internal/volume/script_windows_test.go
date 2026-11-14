package volume

import (
	"os/exec"
	"testing"
	"time"
)

const testMark = "berth-command-test0123"

func startScript(t *testing.T) (pid int, done <-chan error) {
	t.Helper()
	cmd := exec.Command("cmd.exe", "/c", "ping -n 300 127.0.0.1 >nul & rem "+testMark)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	ch := make(chan error, 1)
	go func() { ch <- cmd.Wait() }()
	return cmd.Process.Pid, ch
}

func TestStopScriptStopsTheRunItRecorded(t *testing.T) {
	pid, done := startScript(t)

	if err := stopScript(pid, testMark); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the script is still running")
	}
}

func TestStopScriptLeavesAProcessThatIsNotTheRecordedRun(t *testing.T) {
	pid, done := startScript(t)

	if err := stopScript(pid, "berth-command-another"); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
		t.Fatal("stopped a process that doesn't carry the recorded run's mark")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestStopScriptLeavesAProcessWithNoRecordedMark(t *testing.T) {
	pid, done := startScript(t)

	if err := stopScript(pid, ""); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
		t.Fatal("stopped a process with no recorded mark")
	case <-time.After(200 * time.Millisecond):
	}
}
