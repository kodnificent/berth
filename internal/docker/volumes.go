package docker

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// HelperImage runs every short-lived container berth starts to read or copy volumes.
// It is pinned so a copy behaves the same on every machine.
const HelperImage = "busybox:1.36.1"

// HelperLabel marks berth's helper containers with the id of the berth they work for,
// so a helper left behind by a killed clone can be found and removed.
const HelperLabel = "dev.berth.helper"

// Container is a running container and the compose service it belongs to, if any.
type Container struct {
	ID      string
	Service string
	Name    string
	// Healthcheck is true when the container has a health check, which Docker reports
	// as unhealthy while the container is paused.
	Healthcheck bool
}

// Label is the container's compose service, or its name outside compose.
func (c Container) Label() string {
	if c.Service != "" {
		return c.Service
	}
	return c.Name
}

// RunningWithVolume lists the running containers that mount volume, berth's own helpers
// aside: they mount sources read-only and never write to them.
func RunningWithVolume(volume string) ([]Container, error) {
	out, err := run("ps", "--filter", "volume="+volume, "--filter", "status=running",
		"--format", `{{.ID}}	{{.Label "com.docker.compose.service"}}	{{.Names}}	{{.Label "`+HelperLabel+`"}}	{{.Status}}`)
	if err != nil {
		return nil, err
	}
	var list []Container
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 5 || f[3] != "" {
			continue
		}
		list = append(list, Container{ID: f[0], Service: f[1], Name: f[2], Healthcheck: strings.Contains(f[4], "health")})
	}
	return list, nil
}

func Pause(ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := run(append([]string{"pause"}, ids...)...)
	return err
}

// Unpause resumes the given containers, ignoring any that are no longer paused or gone,
// so it is safe to run again after a crash.
func Unpause(ids ...string) error {
	var errs []string
	for _, id := range ids {
		out, err := run("inspect", "--format", "{{.State.Paused}}", id)
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "no such") {
			continue
		}
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if strings.TrimSpace(out) != "true" {
			continue
		}
		if _, err := run("unpause", id); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// Volumes lists every volume Docker has, by name.
func Volumes() (map[string]bool, error) {
	out, err := run("volume", "ls", "--format", "{{.Name}}")
	if err != nil {
		return nil, err
	}
	all := map[string]bool{}
	for _, n := range strings.Fields(out) {
		all[n] = true
	}
	return all, nil
}

// StartHelper starts a helper container for berth id that mounts each of mounts
// ("<volume>:<path>[:ro]") and waits to be given commands with Exec. Any helper of the
// same name is removed first.
func StartHelper(name, id string, mounts ...string) error {
	_, _ = run("rm", "-f", name)
	args := []string{"run", "-d", "--name", name, "--label", HelperLabel + "=" + id, "--network", "none"}
	for _, m := range mounts {
		args = append(args, "-v", m)
	}
	// Bounded, so a helper whose clone process died never runs for good.
	_, err := runFor(5*time.Minute, append(args, HelperImage, "sleep", "21600")...)
	return err
}

// Exec runs a shell script in a running helper with no time limit, since copying a
// large volume takes as long as it takes.
func Exec(name string, env []string, script string) (string, error) {
	args := []string{"exec"}
	for _, kv := range env {
		args = append(args, "-e", kv)
	}
	return runFor(0, append(args, name, "sh", "-c", script)...)
}

// RemoveHelpers removes every helper container of berth id.
func RemoveHelpers(id string) error {
	out, err := run("ps", "-aq", "--filter", "label="+HelperLabel+"="+id)
	if err != nil {
		return err
	}
	if ids := strings.Fields(out); len(ids) > 0 {
		_, err = run(append([]string{"rm", "-f"}, ids...)...)
	}
	return err
}

// Measure runs one throwaway helper over volumes and reports each one's size and the
// space free where volumes are stored, both in bytes. Every volume must exist: docker
// run would create a missing one.
func Measure(id string, volumes []string, readOnly bool) (sizes []int64, free int64, err error) {
	args := []string{"run", "--rm", "--label", HelperLabel + "=" + id, "--network", "none"}
	var script strings.Builder
	for i, v := range volumes {
		m := fmt.Sprintf("%s:/v/%d", v, i)
		if readOnly {
			m += ":ro"
		}
		args = append(args, "-v", m)
		fmt.Fprintf(&script, "du -sk /v/%d | cut -f1; ", i)
	}
	script.WriteString("df -k /v 2>/dev/null | tail -1 | awk '{print $4}'")
	out, err := runFor(5*time.Minute, append(args, HelperImage, "sh", "-c", script.String())...)
	if err != nil {
		return nil, 0, err
	}
	nums := strings.Fields(out)
	if len(nums) != len(volumes)+1 {
		return nil, 0, fmt.Errorf("measuring volumes: unexpected output %q", out)
	}
	for _, n := range nums[:len(volumes)] {
		kb, err := strconv.ParseInt(n, 10, 64)
		if err != nil {
			return nil, 0, fmt.Errorf("measuring volumes: %q is not a size", n)
		}
		sizes = append(sizes, kb*1024)
	}
	kb, err := strconv.ParseInt(nums[len(volumes)], 10, 64)
	if err != nil {
		return nil, 0, fmt.Errorf("measuring free space: %q is not a size", nums[len(volumes)])
	}
	return sizes, kb * 1024, nil
}

// runFor is run with its own time limit; 0 means none.
func runFor(limit time.Duration, args ...string) (string, error) {
	ctx := context.Background()
	if limit > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, limit)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
