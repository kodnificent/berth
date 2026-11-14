package docker

import "time"

// RunningProjects lists every compose project with a running container, from one call
// bounded by limit.
func RunningProjects(limit time.Duration) (map[string]bool, error) {
	out, err := runFor(limit, "ps", "--filter", "status=running", "--format", `{{.Label "`+ProjectLabel+`"}}`)
	if err != nil {
		return nil, err
	}
	running := map[string]bool{}
	for _, p := range lines(out) {
		running[p] = true
	}
	return running, nil
}
