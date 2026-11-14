//go:build !(darwin || linux || freebsd || netbsd || openbsd || dragonfly)

package cli

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"

	"github.com/kodnificent/berth/internal/apperr"
)

// runCommand runs the command as a child where a process cannot be replaced. A console
// interrupt reaches every process attached to it, so berth only outlives it to wait.
func runCommand(ctx *Context, path string, argv, env []string) error {
	signal.Ignore(os.Interrupt)
	cmd := &exec.Cmd{Path: path, Args: argv, Env: env, Stdin: ctx.Stdin, Stdout: ctx.Stdout, Stderr: ctx.Stderr}
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exitStatus(exit.ExitCode())
	}
	if err != nil {
		return apperr.New(apperr.Generic, "cannot run %s: %v", argv[0], err)
	}
	return nil
}
