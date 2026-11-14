//go:build !windows

package clone

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

func linkIsDir(string) bool { return false }

func makeSymlink(target, link string, _ bool) error { return os.Symlink(target, link) }

func isJunction(string, fs.FileMode) bool { return false }

func makeJunction(target, link string) error { return errors.ErrUnsupported }

func realPath(path string) (string, error) { return filepath.EvalSymlinks(path) }
