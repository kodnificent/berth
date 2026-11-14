//go:build !windows

package cli

import (
	"os"
	"os/exec"

	"github.com/kodnificent/berth/internal/shell"
)

// commandGate runs the script once berth has recorded this shell, so no run exists that
// recovery can't find; if berth dies first the pipe closes and the script never starts.
// The shell stays alive around the script, as a final command would replace it, because
// its arguments carry the mark that identifies the run.
const commandGate = `read -r go <&3 || exit 125; exec 3<&-; sh -c "$1"; exit $?`

// gatedScript runs script behind commandGate, reading the gate as its fd 3, in a
// session of its own for recovery to stop as a group.
func gatedScript(env []string, gate *os.File, mark, script string) (*exec.Cmd, error) {
	cmd, err := shell.Command(env, commandGate, mark, script)
	if err != nil {
		return nil, err
	}
	cmd.ExtraFiles = []*os.File{gate}
	detach(cmd)
	return cmd, nil
}
