package sqlite

import (
	"os"

	"github.com/kodnificent/berth/internal/winsys"
)

func cow(f *os.File, dst string) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	err = winsys.BlockClone(out, f)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chtimes(dst, info.ModTime(), info.ModTime())
	}
	if err != nil {
		os.Remove(dst)
	}
	return err
}
