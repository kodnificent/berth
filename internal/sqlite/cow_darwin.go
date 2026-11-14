package sqlite

import (
	"os"

	"golang.org/x/sys/unix"
)

func cow(f *os.File, dst string) error {
	return unix.Fclonefileat(int(f.Fd()), unix.AT_FDCWD, dst, 0)
}
