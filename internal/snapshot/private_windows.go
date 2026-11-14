package snapshot

import (
	"os"

	"github.com/kodnificent/berth/internal/winsys"
)

// openShared opens a snapshot file so that a newer snapshot can still replace it.
func openShared(path string) (*os.File, error) { return winsys.OpenShared(path) }

// private gives the user alone access to the snapshot directory, as 0700 does
// elsewhere, since snapshots hold the data of main's databases.
func private(dir string) error { return winsys.Private(dir) }
