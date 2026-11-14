package docker

import (
	"strings"
	"time"
)

// ProjectLabel is the label compose puts on every container, network, volume and image
// it creates for a project.
const ProjectLabel = "com.docker.compose.project"

// removeLimit bounds one removal; a removed container is killed, not stopped, so this
// is only Docker being slow.
const removeLimit = 2 * time.Minute

// Reachable reports whether the Docker daemon answers.
func Reachable() error {
	_, err := run("version", "--format", "{{.Server.Version}}")
	return err
}

func lines(out string) []string {
	var list []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			list = append(list, l)
		}
	}
	return list
}

// ProjectContainers lists the names of every container, running or not, that compose
// created for project.
func ProjectContainers(project string) ([]string, error) {
	out, err := run("ps", "-a", "--filter", "label="+ProjectLabel+"="+project, "--format", "{{.Names}}")
	return lines(out), err
}

// ProjectRunning lists the names of project's containers that are up, paused or
// restarting: every one that isn't stopped.
func ProjectRunning(project string) ([]string, error) {
	out, err := run("ps", "--filter", "label="+ProjectLabel+"="+project, "--format", "{{.Names}}")
	return lines(out), err
}

// ProjectNetworks lists the names of the networks compose created for project.
func ProjectNetworks(project string) ([]string, error) {
	out, err := run("network", "ls", "--filter", "label="+ProjectLabel+"="+project, "--format", "{{.Name}}")
	return lines(out), err
}

// ProjectImages lists the references of images compose built for project under the
// project's own names, <project>-<service>.
func ProjectImages(project string) ([]string, error) {
	out, err := run("image", "ls", "--filter", "label="+ProjectLabel+"="+project, "--format", "{{.Repository}}:{{.Tag}}")
	var refs []string
	for _, ref := range lines(out) {
		if strings.HasPrefix(ref, project+"-") && !strings.HasSuffix(ref, ":<none>") {
			refs = append(refs, ref)
		}
	}
	return refs, err
}

// VolumeProject is the compose project volume name is labelled with; exists is false
// when Docker has no such volume.
func VolumeProject(name string) (project string, exists bool, err error) {
	out, err := run("volume", "inspect", "--format", `{{index .Labels "`+ProjectLabel+`"}}`, name)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "no such volume") {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(out), true, nil
}

// ProjectsInUse lists every compose project that has a container, volume or network.
func ProjectsInUse() (map[string]bool, error) {
	in := map[string]bool{}
	for _, args := range [][]string{
		{"ps", "-a", "--format", `{{.Label "` + ProjectLabel + `"}}`},
		{"volume", "ls", "--format", `{{.Label "` + ProjectLabel + `"}}`},
		{"network", "ls", "--format", `{{.Label "` + ProjectLabel + `"}}`},
	} {
		out, err := run(args...)
		if err != nil {
			return nil, err
		}
		for _, p := range lines(out) {
			in[p] = true
		}
	}
	return in, nil
}

// RemoveContainer kills and removes a container with its anonymous volumes.
func RemoveContainer(name string) error {
	_, err := runFor(removeLimit, "rm", "-f", "-v", name)
	return err
}

func RemoveNetwork(name string) error {
	_, err := runFor(removeLimit, "network", "rm", name)
	return err
}

func RemoveVolume(name string) error {
	_, err := runFor(removeLimit, "volume", "rm", name)
	return err
}

// RemoveImage removes the reference ref. An image that has other names keeps them and
// only loses this one; one a container uses is refused, never forced.
func RemoveImage(ref string) error {
	_, err := runFor(removeLimit, "image", "rm", ref)
	return err
}
