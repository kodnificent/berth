//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package volume

import (
	"os"
	"syscall"
)

func tryLock(f *os.File) (bool, error) {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch err {
		case nil:
			return true, nil
		case syscall.EWOULDBLOCK:
			return false, nil
		case syscall.EINTR:
			continue
		default:
			return false, err
		}
	}
}

func waitLock(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			return err
		}
	}
}
