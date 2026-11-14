//go:build !(darwin || linux || freebsd || netbsd || openbsd || dragonfly || windows)

package hook

import (
	"errors"
	"os"
)

var errNoLock = errors.New("berth cannot track post-add processes on this platform yet")

func tryLock(*os.File) (bool, error) { return false, errNoLock }
func waitLock(*os.File) error        { return errNoLock }
