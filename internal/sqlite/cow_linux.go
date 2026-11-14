package sqlite

import (
	"os"

	"golang.org/x/sys/unix"
)

// cow reflinks f into dst on filesystems that share extents, such as btrfs and XFS.
func cow(f *os.File, dst string) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if err := unix.IoctlFileClone(int(out.Fd()), int(f.Fd())); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}
