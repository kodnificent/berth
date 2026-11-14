//go:build !darwin && !linux && !windows

package sqlite

import (
	"errors"
	"os"
)

func cow(*os.File, string) error { return errors.ErrUnsupported }
