package cli

import (
	"strings"

	"github.com/kodnificent/berth/internal/winsys"
)

// listeners asks Windows which processes listen on TCP port, and where each runs. A
// process berth may not read, such as another user's, is listed with what could be read.
func listeners(port int) ([]listener, error) {
	pids, err := winsys.Listeners(port)
	if err != nil {
		return nil, err
	}
	var list []listener
	for _, pid := range pids {
		l := listener{pid: pid}
		if image, err := winsys.Image(pid); err == nil {
			l.command = strings.TrimSuffix(image, ".exe")
		}
		l.cwd, _ = winsys.WorkingDir(pid)
		list = append(list, l)
	}
	return list, nil
}
