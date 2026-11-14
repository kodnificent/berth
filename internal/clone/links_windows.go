package clone

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/kodnificent/berth/internal/winsys"
	"golang.org/x/sys/windows"
)

func linkIsDir(path string) bool { return winsys.LinkIsDir(path) }

func realPath(path string) (string, error) { return winsys.FinalPath(path) }

func makeSymlink(target, link string, dir bool) error {
	err := winsys.Symlink(target, link, dir)
	if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
		return fmt.Errorf("%s: %w", link, ErrNoSymlinkPrivilege)
	}
	return err
}

// isJunction reports whether the reparse point at path is a junction. A volume mount
// point has the same tag, but its target names a volume, not a path a berth could hold.
func isJunction(path string, mode fs.FileMode) bool {
	if mode&fs.ModeIrregular == 0 {
		return false
	}
	target, err := os.Readlink(path)
	return err == nil && !strings.HasPrefix(target, `\\`)
}

func makeJunction(target, link string) error { return winsys.MakeJunction(target, link) }
