package sqlite

import (
	"fmt"
	"os"
	"time"

	"github.com/kodnificent/berth/internal/winsys"
)

// The byte ranges SQLite's win32 VFS locks, from os_win.c and wal.c.
const (
	sharedFirst  = pendingByte + 2
	sharedSize   = 510
	walLockBase  = 120
	walReadLock0 = walLockBase + 3
)

// lockShared takes SQLite's SHARED lock on the database file f as SQLite does on
// Windows, holding the PENDING byte exclusively while it takes the shared range, so a
// writer waiting to commit is never starved. It waits up to busyWait for writers to let
// it in.
func lockShared(f *os.File) error {
	return retry("a writer of main's held it", func() (bool, error) {
		if ok, err := winsys.Lock(f, pendingByte, 1, true, false); err != nil || !ok {
			return false, err
		}
		ok, err := winsys.Lock(f, sharedFirst, sharedSize, false, false)
		if unlock := winsys.Unlock(f, pendingByte, 1); err == nil {
			err = unlock
		}
		return ok, err
	})
}

// lockReader takes shared locks on the first two reader slots of the wal-index shm. A
// checkpoint must hold the first exclusively while it writes WAL frames into the
// database file, and a writer starting the WAL again from the top, or a checkpoint
// truncating it, must hold the second.
func lockReader(shm *os.File) error {
	return retry("a checkpoint of main's held it", func() (bool, error) {
		return winsys.Lock(shm, walReadLock0, 2, false, false)
	})
}

func retry(holder string, try func() (bool, error)) error {
	deadline := time.Now().Add(busyWait)
	wait := time.Millisecond
	for {
		ok, err := try()
		if err != nil || ok {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("could not lock it for reading within %s; %s", busyWait, holder)
		}
		time.Sleep(wait)
		wait = min(2*wait, 50*time.Millisecond)
	}
}
