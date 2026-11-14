package clone

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// cowTree clones a file or a whole directory tree in one call on APFS. CLONE_NOFOLLOW
// copies a top-level symlink as a link; links inside a directory are always copied as
// links.
func cowTree(src, dst string) error {
	return unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW)
}

func reflink(out, in *os.File) error { return errors.ErrUnsupported }

func filesystem(path string) string {
	var st unix.Statfs_t
	if unix.Statfs(path, &st) != nil {
		return ""
	}
	return unix.ByteSliceToString(st.Fstypename[:])
}
