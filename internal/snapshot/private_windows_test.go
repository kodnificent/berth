package snapshot

import (
	"os"
	"testing"

	"github.com/kodnificent/berth/internal/winsys"
)

// Windows keeps no permission bits; who may open a file is its access control list.
func expectPrivate(t *testing.T, path string, _ os.DirEntry) error {
	t.Helper()
	only, err := winsys.OnlyUserMayAccess(path)
	if err != nil {
		return err
	}
	if !only {
		t.Errorf("%s grants access to someone other than the user", path)
	}
	return nil
}
