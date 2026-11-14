package registry

import (
	"os"

	"github.com/kodnificent/berth/internal/winsys"
)

func lockFile(f *os.File) error {
	_, err := winsys.LockFile(f, true)
	return err
}

func unlockFile(f *os.File) error { return winsys.UnlockFile(f) }

func tryLockFile(f *os.File) (bool, error) { return winsys.LockFile(f, false) }
