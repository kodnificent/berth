//go:build !windows

package snapshot

import "os"

func openShared(path string) (*os.File, error) { return os.Open(path) }

// private has nothing to add to the 0700 the directory is made with.
func private(string) error { return nil }
