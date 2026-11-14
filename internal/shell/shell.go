// Package shell runs the user's scripts with sh, the same sh on every platform.
package shell

import (
	"os"
	"os/exec"
	"path/filepath"
)

// Command runs script with sh, args becoming $0, $1 and on, in the environment env, or
// this process's when env is nil.
func Command(env []string, script string, args ...string) (*exec.Cmd, error) {
	sh, err := Path()
	if err != nil {
		return nil, err
	}
	return command(sh, env, script, args), nil
}

// gitSh is the sh.exe of the Git for Windows install that holds dir, looking in dir and
// up to levels-1 of its parents, in each of which Git for Windows, its portable build
// and MSYS2 keep sh in bin or usr\bin. Git for Windows' bin\sh.exe goes first: it puts
// git's own tools on PATH before starting the sh in usr\bin.
func gitSh(dir string, levels int) (string, bool) {
	for range levels {
		for _, sh := range []string{filepath.Join(dir, "bin", "sh.exe"), filepath.Join(dir, "usr", "bin", "sh.exe")} {
			if info, err := os.Stat(sh); err == nil && info.Mode().IsRegular() {
				return sh, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}
