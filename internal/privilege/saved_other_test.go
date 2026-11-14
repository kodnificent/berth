//go:build !linux && !windows

package privilege

import "errors"

func keepSavedRoot(int, int) error { return errors.New("only Linux tests a kept saved uid") }
