//go:build !windows

package lockfile

import "os"

// Open opens the lock file at path for locking, creating it when create is set.
func Open(path string, create bool) (*os.File, error) {
	flag := os.O_RDWR
	if create {
		flag |= os.O_CREATE
	}
	return os.OpenFile(path, flag, 0o644)
}
