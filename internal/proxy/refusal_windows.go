package proxy

import (
	"errors"

	"golang.org/x/sys/windows"
)

// refusal sorts a failed bind: another socket holds the address, Windows refuses the
// port, or neither, empty, such as an address the system lacks. WSAEACCES is a port in a
// reserved range or one another socket holds with SO_EXCLUSIVEADDRUSE.
func refusal(err error) string {
	switch {
	case errors.Is(err, windows.WSAEADDRINUSE):
		return inUse
	case errors.Is(err, windows.WSAEACCES):
		return denied
	}
	return ""
}
