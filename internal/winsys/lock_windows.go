package winsys

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// Lock takes a lock on n bytes of f from off, shared or exclusive. With wait it blocks
// until the lock is free; without, ok is false while another handle holds a conflicting
// one. Windows drops the lock when the handle closes or its process ends.
func Lock(f *os.File, off, n int64, exclusive, wait bool) (ok bool, err error) {
	var flags uint32
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	if !wait {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	ol := windows.Overlapped{Offset: uint32(off), OffsetHigh: uint32(off >> 32)}
	err = windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, uint32(n), uint32(n>>32), &ol)
	if !wait && errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}

// Unlock releases the lock this handle holds on exactly n bytes of f from off.
func Unlock(f *os.File, off, n int64) error {
	ol := windows.Overlapped{Offset: uint32(off), OffsetHigh: uint32(off >> 32)}
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, uint32(n), uint32(n>>32), &ol)
}

// lockBase is far past the end of any lock file. Windows locks are mandatory, so a lock
// on bytes someone reads, which even an empty file's first bytes are, fails that read.
const lockBase = 1 << 40

// LockFile takes the lock on the lock file f, exclusively, waiting for it with wait; ok
// is false without wait while another handle holds it.
func LockFile(f *os.File, wait bool) (ok bool, err error) { return Lock(f, lockBase, 1, true, wait) }

// UnlockFile releases the lock this handle holds on the lock file f.
func UnlockFile(f *os.File) error { return Unlock(f, lockBase, 1) }

// TryPassable takes the passable lock f without waiting. A lock on Windows belongs to
// the process that took it and can't pass to another, so a lock one process holds and
// the next takes over is two bytes: holding it means holding either, and taking it means
// taking both, the first byte first. HandOff lets go of the second while the first is
// still held, TakeOver takes the second, and only then does the first go, so at no
// moment is neither held. Only TakeOver takes the second byte without the first.
func TryPassable(f *os.File) (bool, error) {
	ok, err := Lock(f, lockBase, 1, true, false)
	if err != nil || !ok {
		return false, err
	}
	if ok, err = Lock(f, lockBase+1, 1, true, false); err != nil || !ok {
		_ = Unlock(f, lockBase, 1)
		return false, err
	}
	return true, nil
}

// WaitPassable waits for, then takes, both bytes of the passable lock f.
func WaitPassable(f *os.File) error {
	if _, err := Lock(f, lockBase, 1, true, true); err != nil {
		return err
	}
	if _, err := Lock(f, lockBase+1, 1, true, true); err != nil {
		_ = Unlock(f, lockBase, 1)
		return err
	}
	return nil
}

// HandOff lets go of the second byte of the passable lock f, which this handle holds in
// full, for another process to TakeOver.
func HandOff(f *os.File) error { return Unlock(f, lockBase+1, 1) }

// TakeOver waits for the second byte of the passable lock f, which only a HandOff frees
// while the first is held.
func TakeOver(f *os.File) error {
	_, err := Lock(f, lockBase+1, 1, true, true)
	return err
}
