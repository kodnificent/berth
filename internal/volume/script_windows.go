package volume

import (
	"fmt"
	"strings"
	"time"

	"github.com/kodnificent/berth/internal/winsys"
)

// scriptKillWait is how long Windows gets to end a stopped script.
const scriptKillWait = 3 * time.Second

// stopScript ends the command script of a clone process that died, and waits for it to
// go. What the script started ran in the clone process's job object, which Windows ends
// with the clone process however it dies. A pid whose command line doesn't carry mark
// is another process's, and is left alone.
func stopScript(pid int, mark string) error {
	if mark == "" {
		return nil
	}
	p, ok, err := winsys.OpenProcess(pid)
	if err != nil || !ok {
		return nil
	}
	defer p.Close()
	if line, err := p.CommandLine(); err != nil || !strings.Contains(line, mark) {
		return nil
	}
	termErr := p.Terminate()
	if p.Wait(scriptKillWait) {
		return nil
	}
	if termErr != nil {
		return fmt.Errorf("stopping the command's script (process %d): %w", pid, termErr)
	}
	return fmt.Errorf("the command's script (process %d) is still running after it was ended", pid)
}
