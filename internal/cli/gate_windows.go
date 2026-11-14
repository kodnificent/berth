package cli

import (
	"os"
	"os/exec"

	"github.com/kodnificent/berth/internal/shell"
)

// commandGate runs the script once berth has recorded this shell, so no run exists that
// recovery can't find; if berth dies first the pipe closes and the script never starts.
// The shell stays alive around the script, as a final command would replace it, because
// its arguments carry the mark that identifies the run. Windows passes a child no
// descriptor past the standard three, so the gate is stdin, which the script doesn't get.
// The script comes through the environment for the reason shell.Command's does.
const commandGate = `read -r go || exit 125; exec </dev/null; __berth_command=$BERTH_COMMAND; unset BERTH_COMMAND; sh -c "$__berth_command"; exit $?`

// gatedScript runs script behind commandGate, reading the gate as its stdin. It stays in
// the clone process's job object, which ends it, and all it starts, with that process.
func gatedScript(env []string, gate *os.File, mark, script string) (*exec.Cmd, error) {
	cmd, err := shell.Command(append(env, "BERTH_COMMAND="+script), commandGate, mark)
	if err != nil {
		return nil, err
	}
	cmd.Stdin = gate
	return cmd, nil
}
