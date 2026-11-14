//go:build !(darwin || linux || freebsd || netbsd || openbsd || dragonfly || windows)

package registry

import (
	"errors"
	"os"
)

var errNoLock = errors.New("berth cannot lock its registry on this platform yet")

func lockFile(*os.File) error   { return errNoLock }
func unlockFile(*os.File) error { return nil }

func tryLockFile(*os.File) (bool, error) { return false, errNoLock }
