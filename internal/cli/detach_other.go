//go:build !(darwin || linux || freebsd || netbsd || openbsd || dragonfly || windows)

package cli

import (
	"errors"
	"os"
	"os/exec"
)

func detach(*exec.Cmd) {}

func passLock(*exec.Cmd, *os.File) (lockPass, error) {
	return lockPass{}, errors.New("passing a lock to another process is not supported on this platform")
}

type lockPass struct{}

func (lockPass) wait(*os.Process) error { return nil }
func (lockPass) close()                 {}

func holdInheritedLock() bool { return false }

func confineChildren() error { return nil }

func freeSpace(string) (int64, bool) { return 0, false }

func killClone(string, int) error {
	return errors.New("stopping a clone is not supported on this platform")
}
