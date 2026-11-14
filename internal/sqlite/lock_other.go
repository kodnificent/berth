//go:build !(darwin || linux || freebsd || netbsd || openbsd || dragonfly || windows)

package sqlite

import (
	"errors"
	"os"
)

var errNoLock = errors.New("berth cannot take SQLite's locks on this platform yet")

func lockShared(*os.File) error { return errNoLock }
func lockReader(*os.File) error { return errNoLock }
