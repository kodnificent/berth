// Command shim runs the sh script beside it that has its name without .exe, so a test
// can put a script on PATH on Windows, where only programs are found there.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// sh is set when the shim is built: a shim standing in for git can't look sh up through
// the git on PATH, which is itself.
var sh string

func main() {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "shim:", err)
		os.Exit(127)
	}
	cmd := exec.Command(sh, append([]string{strings.TrimSuffix(self, ".exe")}, os.Args[1:]...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// The script hands its arguments on to the real program, and Git for Windows' sh
	// would expand any holding wildcards or braces, such as a docker --format template,
	// and rewrite any that look like POSIX paths, such as a container path, on the way.
	cmd.Env = append(os.Environ(), "MSYS=noglob", "MSYS_NO_PATHCONV=1", "MSYS2_ARG_CONV_EXCL=*")
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		os.Exit(exit.ExitCode())
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "shim:", err)
		os.Exit(127)
	}
}
