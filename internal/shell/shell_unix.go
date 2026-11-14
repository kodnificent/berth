//go:build !windows

package shell

import "os/exec"

// Path is the sh scripts run with: the one on PATH.
func Path() (string, error) { return "sh", nil }

func command(sh string, env []string, script string, args []string) *exec.Cmd {
	cmd := exec.Command(sh, append([]string{"-c", script}, args...)...)
	cmd.Env = env
	return cmd
}
