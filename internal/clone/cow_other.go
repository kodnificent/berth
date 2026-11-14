//go:build !darwin && !linux && !windows

package clone

import (
	"errors"
	"os"
)

func cowTree(src, dst string) error { return errors.ErrUnsupported }

func reflink(out, in *os.File) error { return errors.ErrUnsupported }

func filesystem(path string) string { return "" }
