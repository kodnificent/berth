package privilege

import "syscall"

// keepSavedRoot changes every id but the saved uid, which stays root, as a drop done with
// seteuid alone would.
func keepSavedRoot(uid, gid int) error {
	if err := syscall.Setgroups([]int{gid}); err != nil {
		return err
	}
	if err := syscall.Setresgid(gid, gid, gid); err != nil {
		return err
	}
	return syscall.Setresuid(uid, uid, 0)
}
