//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package cli

import (
	"syscall"

	"github.com/kodnificent/berth/internal/apperr"
)

// runCommand replaces berth with the command, so the command is the process the shell,
// the terminal and any signal sender already hold: a terminal's Ctrl-C reaches it once,
// and its exit status, death by signal included, is the one berth's caller sees.
func runCommand(_ *Context, path string, argv, env []string) error {
	if err := syscall.Exec(path, argv, env); err != nil {
		return apperr.New(apperr.Generic, "cannot run %s: %v", argv[0], err)
	}
	return nil
}
