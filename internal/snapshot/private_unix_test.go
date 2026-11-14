//go:build !windows

package snapshot

import (
	"os"
	"testing"
)

func expectPrivate(t *testing.T, path string, d os.DirEntry) error {
	t.Helper()
	info, err := d.Info()
	if err != nil {
		return err
	}
	want := os.FileMode(0o600)
	if d.IsDir() {
		want = 0o700
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s has mode %o, want %o", path, got, want)
	}
	return nil
}
