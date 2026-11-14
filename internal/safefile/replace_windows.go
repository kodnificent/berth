package safefile

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// replaceWait bounds how long Replace waits for readers of path to close it.
const replaceWait = 2 * time.Second

// Replace renames tmp over path in one step. Windows refuses to replace a file another
// process has open, as any berth command reading state has for a moment, so Replace
// tries again until the readers are done.
func Replace(tmp, path string) error {
	deadline := time.Now().Add(replaceWait)
	wait := time.Millisecond
	for {
		err := os.Rename(tmp, path)
		if err == nil || !(errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(wait)
		wait = min(2*wait, 50*time.Millisecond)
	}
}
