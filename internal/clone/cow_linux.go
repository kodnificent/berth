package clone

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func cowTree(src, dst string) error { return errors.ErrUnsupported }

// reflink makes out share in's extents with FICLONE, which btrfs and XFS support and
// every other filesystem refuses without writing anything.
func reflink(out, in *os.File) error {
	return unix.IoctlFileClone(int(out.Fd()), int(in.Fd()))
}

var filesystems = map[uint32]string{
	unix.BTRFS_SUPER_MAGIC:     "btrfs",
	unix.XFS_SUPER_MAGIC:       "xfs",
	unix.EXT4_SUPER_MAGIC:      "ext4",
	unix.BCACHEFS_SUPER_MAGIC:  "bcachefs",
	unix.F2FS_SUPER_MAGIC:      "f2fs",
	unix.TMPFS_MAGIC:           "tmpfs",
	unix.OVERLAYFS_SUPER_MAGIC: "overlayfs",
	unix.NFS_SUPER_MAGIC:       "nfs",
	0x2fc12fc1:                 "zfs",
}

func filesystem(path string) string {
	var st unix.Statfs_t
	if unix.Statfs(path, &st) != nil {
		return ""
	}
	return filesystems[uint32(st.Type)]
}
