//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package cli

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// detach starts cmd in its own session, so it outlives add and the terminal or hook
// that ran it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// passLock passes lock to cmd as its fd 3, which holdInheritedLock takes up. The lock
// is the open file itself, so the child holds it from the moment it starts.
func passLock(cmd *exec.Cmd, lock *os.File) (lockPass, error) {
	cmd.ExtraFiles = []*os.File{lock}
	return lockPass{}, nil
}

type lockPass struct{}

func (lockPass) wait(*os.Process) error { return nil }
func (lockPass) close()                 {}

// holdInheritedLock checks that fd 3 is the clone lock add passed down and is held, and
// keeps it from leaking into docker commands, which could outlive this process and so
// keep a dead clone looking alive.
func holdInheritedLock() bool {
	if err := syscall.Flock(3, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return false
	}
	syscall.CloseOnExec(3)
	return true
}

func freeSpace(dir string) (int64, bool) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(dir, &s); err != nil {
		return 0, false
	}
	return int64(s.Bavail) * int64(s.Bsize), true
}

// confineChildren has nothing to do: the clone process leads its own session, and
// killClone ends the docker commands in it, while a command's script runs in a session
// of its own that recovery stops.
func confineChildren() error { return nil }

// killClone kills the process group berth id's clone process, pid, leads, so the
// docker commands it is waiting on go with it. The pid comes from a file that outlives
// the process, so a pid whose process isn't the clone of id is left alone: the kernel
// may have given it to a shell's job, which leads a group of its own.
func killClone(id string, pid int) error {
	out, err := exec.Command("ps", "-ww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil || !strings.HasSuffix(strings.TrimSpace(string(out)), " "+cloneCommand+" "+id) {
		return nil
	}
	return syscall.Kill(-pid, syscall.SIGKILL)
}
