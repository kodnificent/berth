//go:build !windows

package privilege

import (
	"fmt"
	"os"
	"slices"
	"syscall"
)

// IsRoot reports whether berth runs with root's effective uid.
func IsRoot() bool { return os.Geteuid() == 0 }

// Drop makes a process running as root the user who ran sudo, for good: supplementary
// groups reduced to that user's primary group, then its gid, then its uid, which as root
// sets the real, effective and saved ids alike. It then checks root can't be had back.
// It does nothing when the process isn't root, and refuses root it was only lent.
func Drop() (dropped bool, err error) {
	if !IsRoot() {
		return false, nil
	}
	if err := rootByItself(os.Getuid()); err != nil {
		return false, err
	}
	uid, gid, err := sudoUser(os.Getenv)
	if err != nil {
		return false, err
	}
	if err := syscall.Setgroups([]int{gid}); err != nil {
		return false, dropFailed(fmt.Errorf("setgroups: %w", err))
	}
	if err := syscall.Setgid(gid); err != nil {
		return false, dropFailed(fmt.Errorf("setgid %d: %w", gid, err))
	}
	if err := syscall.Setuid(uid); err != nil {
		return false, dropFailed(fmt.Errorf("setuid %d: %w", uid, err))
	}
	if err := verifyDropped(uid, gid); err != nil {
		return true, dropFailed(err)
	}
	return true, nil
}

func verifyDropped(uid, gid int) error {
	if os.Getuid() != uid || os.Geteuid() != uid || os.Getgid() != gid || os.Getegid() != gid {
		return fmt.Errorf("ids are uid %d euid %d gid %d egid %d after the change, want uid %d and gid %d", os.Getuid(), os.Geteuid(), os.Getgid(), os.Getegid(), uid, gid)
	}
	if groups, err := os.Getgroups(); err != nil || slices.ContainsFunc(groups, func(g int) bool { return g != gid }) {
		return fmt.Errorf("supplementary groups are %v (%v), want only %d", groups, err, gid)
	}
	if syscall.Setuid(0) == nil || syscall.Seteuid(0) == nil || syscall.Setgid(0) == nil {
		return errRegained
	}
	return nil
}

// StateOwned refuses a state directory that exists and belongs to anyone but uid.
func StateOwned(dir string, uid int) error {
	return stateOwner(dir, uid, func(info os.FileInfo) (int, bool) {
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return 0, false
		}
		return int(st.Uid), true
	})
}
