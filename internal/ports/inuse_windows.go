package ports

import (
	"errors"

	"golang.org/x/sys/windows"
)

// inUse reports whether a failed bind means another socket holds the address. A socket
// bound with SO_EXCLUSIVEADDRUSE, as Windows services often are, refuses with
// WSAEACCES rather than WSAEADDRINUSE.
func inUse(err error) bool {
	return errors.Is(err, windows.WSAEADDRINUSE) || errors.Is(err, windows.WSAEACCES)
}
