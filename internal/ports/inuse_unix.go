//go:build !windows

package ports

import (
	"errors"
	"syscall"
)

// inUse reports whether a failed bind means another socket holds the address.
func inUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, syscall.EACCES)
}
