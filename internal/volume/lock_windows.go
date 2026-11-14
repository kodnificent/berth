package volume

import (
	"os"

	"github.com/kodnificent/berth/internal/winsys"
)

// The lock is passed from the process that schedules the work to the one that does it.
func tryLock(f *os.File) (bool, error) { return winsys.TryPassable(f) }

func waitLock(f *os.File) error { return winsys.WaitPassable(f) }
