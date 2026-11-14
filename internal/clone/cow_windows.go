package clone

import (
	"errors"
	"os"

	"github.com/kodnificent/berth/internal/winsys"
)

func cowTree(src, dst string) error { return errors.ErrUnsupported }

func reflink(out, in *os.File) error { return winsys.BlockClone(out, in) }

func filesystem(path string) string { return winsys.Filesystem(path) }
