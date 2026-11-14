//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package sqlite

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// The byte ranges SQLite's unix VFS locks, from os_unix.c and wal.c.
const (
	sharedFirst  = pendingByte + 2
	sharedSize   = 510
	walLockBase  = 120
	walReadLock0 = walLockBase + 3
)

// lockShared takes SQLite's SHARED lock on the database file f as SQLite does, through
// the PENDING byte, so a writer waiting to commit is never starved. It waits up to
// busyWait for writers to let it in.
func lockShared(f *os.File) error {
	return retry("a writer of main's held it", func() error {
		if err := setLock(f, unix.F_RDLCK, pendingByte, 1); err != nil {
			return err
		}
		err := setLock(f, unix.F_RDLCK, sharedFirst, sharedSize)
		if unlock := setLock(f, unix.F_UNLCK, pendingByte, 1); err == nil {
			err = unlock
		}
		return err
	})
}

// lockReader takes shared locks on the first two reader slots of the wal-index shm. A
// checkpoint must hold the first exclusively while it writes WAL frames into the
// database file, and a writer starting the WAL again from the top, or a checkpoint
// truncating it, must hold the second.
func lockReader(shm *os.File) error {
	return retry("a checkpoint of main's held it", func() error {
		return setLock(shm, unix.F_RDLCK, walReadLock0, 2)
	})
}

func setLock(f *os.File, typ int16, start, n int64) error {
	lk := unix.Flock_t{Type: typ, Whence: 0, Start: start, Len: n}
	for {
		err := unix.FcntlFlock(f.Fd(), unix.F_SETLK, &lk)
		if err != unix.EINTR {
			return err
		}
	}
}

func retry(holder string, try func() error) error {
	deadline := time.Now().Add(busyWait)
	wait := time.Millisecond
	for {
		err := try()
		if err == nil || !(errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EACCES)) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("could not lock it for reading within %s; %s", busyWait, holder)
		}
		time.Sleep(wait)
		wait = min(2*wait, 50*time.Millisecond)
	}
}
