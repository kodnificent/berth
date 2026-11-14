//go:build !windows

package cli

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// listeners asks lsof which processes listen on TCP port, and where each runs.
func listeners(port int) ([]listener, error) {
	out, err := exec.Command("lsof", "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-Fpc").Output()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("lsof: %w", err)
	}
	var list []listener
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "p"):
			pid, _ := strconv.Atoi(line[1:])
			list = append(list, listener{pid: pid})
		case strings.HasPrefix(line, "c") && len(list) > 0:
			list[len(list)-1].command = line[1:]
		}
	}
	for i := range list {
		cwd, _ := exec.Command("lsof", "-a", "-p", strconv.Itoa(list[i].pid), "-d", "cwd", "-Fn").Output()
		for _, line := range strings.Split(string(cwd), "\n") {
			if strings.HasPrefix(line, "n") {
				list[i].cwd = line[1:]
			}
		}
	}
	return list, nil
}
