//go:build !windows

package proxy

import (
	"errors"
	"syscall"
)

// refusal sorts a failed bind: another socket holds the address, the system keeps the
// port for administrators, or neither, empty, such as an address the system lacks.
func refusal(err error) string {
	switch {
	case errors.Is(err, syscall.EADDRINUSE):
		return inUse
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return denied
	}
	return ""
}
