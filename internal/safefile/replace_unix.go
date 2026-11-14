//go:build !windows

package safefile

import "os"

// Replace renames tmp over path in one step.
func Replace(tmp, path string) error { return os.Rename(tmp, path) }
