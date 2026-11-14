//go:build darwin || windows

package truststore

import (
	"errors"
	"os"
	"runtime"
	"testing"
)

func TestTheToolsCopyIsPrivateAndGoneAfter(t *testing.T) {
	var path string
	var mode os.FileMode

	err := withPrivateCopy([]byte("pem"), func(p string) error {
		path = p
		info, err := os.Stat(p)
		if err == nil {
			mode = info.Mode().Perm()
		}
		return err
	})

	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && mode != 0o600 {
		t.Errorf("the copy had mode %#o, want 0600", mode)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the copy %s is still there", path)
	}
}
