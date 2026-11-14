//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package volume

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// scriptTermWait is how long a stopped script gets to end by itself before SIGKILL,
	// and scriptKillWait how long the kernel gets after it.
	scriptTermWait = 3 * time.Second
	scriptKillWait = 3 * time.Second
)

// scriptRunning reports whether process pid is still the run recorded with mark, which
// is in the arguments of that run's outer shell and of no later process given the pid.
func scriptRunning(pid int, mark string) bool {
	out, err := exec.Command("ps", "-ww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && mark != "" && strings.Contains(string(out), mark)
}

// stopScript stops the process group of a command script whose clone process died, and
// waits for it to go. A pid that can't be confirmed as that run's is left alone: its
// process has ended and the pid may be another's.
func stopScript(pid int, mark string) error {
	if !scriptRunning(pid, mark) {
		return nil
	}
	if pgid, err := syscall.Getpgid(pid); err != nil || pgid != pid {
		return nil
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("stopping the command's script (process group %d): %w", pid, err)
	}
	if groupGone(pid, scriptTermWait) {
		return nil
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("stopping the command's script (process group %d): %w", pid, err)
	}
	if groupGone(pid, scriptKillWait) {
		return nil
	}
	return fmt.Errorf("the command's script (process group %d) is still running after SIGKILL", pid)
}

func groupGone(pgid int, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}
