//go:build e2e && !windows

package e2e

import (
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// binSh is the sh a test hands to berth exec by its path.
const binSh = "/bin/sh"

const exe = ""

func killNow(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }

// alive reports whether the process a script's $$ named still runs.
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// killScriptPid ends the process a script's $$ named.
func killScriptPid(pid int) { _ = syscall.Kill(pid, syscall.SIGKILL) }

// killScriptGroup ends the process group of the script whose $$ was pid.
func killScriptGroup(pid int) {
	if pgid, err := syscall.Getpgid(pid); err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
}

// lockLikeBerth takes the lock berth takes on its registry and worktree lock files.
func lockLikeBerth(f *os.File) error   { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }
func unlockLikeBerth(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

// ownGroup starts cmd as the leader of a process group killGroupOf ends whole.
func ownGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func killGroupOf(cmd *exec.Cmd) error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

// makeExecutable lets the sh script at path be run by its name.
func makeExecutable(path string) error { return os.Chmod(path, 0o755) }

// lchtimes sets path's access and modification times to at, of a symlink itself.
func lchtimes(path string, at time.Time) error {
	tv := []unix.Timeval{unix.NsecToTimeval(at.UnixNano()), unix.NsecToTimeval(at.UnixNano())}
	return unix.Lutimes(path, tv)
}

// scriptCommand runs the sh script at path as the program that started it would.
func scriptCommand(path string, args ...string) *exec.Cmd { return exec.Command(path, args...) }

func addrInUse(err error) bool { return strings.Contains(err.Error(), "address already in use") }

const gitPlatformConfig = ""

func endProcessesIn(string) {}

// shPwd prints the working directory.
const shPwd = "pwd -P"

// maxWorktreeDirName is how long a worktree's directory name may be.
const maxWorktreeDirName = 1 << 16

func expectPrivate(t *testing.T, path string, info fs.FileInfo) {
	t.Helper()
	want := os.FileMode(0o600)
	if info.IsDir() {
		want = 0o700
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s has mode %o, want %o", path, got, want)
	}
}

// blockRemovalOf keeps the directory from being removed for the rest of the test.
func blockRemovalOf(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}
