// Package docker asks the Docker CLI about containers and volumes. Every call is
// short-lived and bounded, and callers treat Docker being unreachable as normal.
package docker

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const timeout = 10 * time.Second

func run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// published matches one mapping in docker ps's Ports column, such as
// "0.0.0.0:20001->80/tcp" or "[::]:20001->80/tcp".
var published = regexp.MustCompile(`:(\d+)->(\d+)/(\w+)`)

// PublishedPorts maps "<service>:<container port>" to the host port Docker published it
// on, for the running containers of a compose project.
func PublishedPorts(project string) (map[string]int, error) {
	out, err := run("ps", "--filter", "label=com.docker.compose.project="+project, "--format", `{{.Label "com.docker.compose.service"}}	{{.Ports}}`)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// Sorted, so a service with several replicas reports the same one every time.
	sort.Strings(lines)
	ports := map[string]int{}
	for _, line := range lines {
		service, cols, _ := strings.Cut(line, "\t")
		for _, m := range published.FindAllStringSubmatch(cols, -1) {
			key := service + ":" + m[2]
			if _, seen := ports[key]; !seen {
				ports[key], _ = strconv.Atoi(m[1])
			}
		}
	}
	return ports, nil
}

// It needs the CLI and its compose plugin, not the daemon, so it answers while Docker is down.
func ComposeVersion() (string, error) {
	out, err := run("compose", "version", "--short")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// VolumeExists reports whether Docker has a volume named name.
func VolumeExists(name string) (bool, error) {
	_, err := run("volume", "inspect", "--format", "{{.Name}}", name)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "no such volume") {
		return false, nil
	}
	return err == nil, err
}

// ImageExists reports whether Docker has an image under ref.
func ImageExists(ref string) (bool, error) {
	_, err := run("image", "inspect", "--format", "{{.Id}}", ref)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "no such image") {
		return false, nil
	}
	return err == nil, err
}

// TagImage gives the image under src the name dst as well.
func TagImage(src, dst string) error {
	_, err := run("image", "tag", src, dst)
	return err
}

// CreateVolume creates a volume with the given driver, options and labels.
func CreateVolume(name, driver string, opts, labels map[string]string) error {
	args := []string{"volume", "create"}
	if driver != "" {
		args = append(args, "--driver", driver)
	}
	for _, k := range sortedKeys(opts) {
		args = append(args, "--opt", k+"="+opts[k])
	}
	for _, k := range sortedKeys(labels) {
		args = append(args, "--label", k+"="+labels[k])
	}
	_, err := run(append(args, name)...)
	return err
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
