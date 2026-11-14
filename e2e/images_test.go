//go:build e2e

package e2e

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// imageBuild matches compose reporting that it builds or built an image, as in
// " Image berthe2e1a2b3c4d-web Building" or " Image web Built".
var imageBuild = regexp.MustCompile(`(?m)^\s*Image \S+ (Building|Built)\s*$`)

// composeOutput runs docker compose in dir and returns what it printed on both streams,
// since compose reports progress on stderr.
func composeOutput(t *testing.T, repo *Repo, dir string, args ...string) string {
	t.Helper()
	res, err := repo.run(dir, "docker", append([]string{"compose"}, args...)...)
	if err != nil || res.Exit != 0 {
		t.Fatalf("docker compose %s (in %s): exit %d %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), dir, res.Exit, err, res.Stdout, res.Stderr)
	}
	return res.Stdout + res.Stderr
}

// imageID is the ID of the image Docker has under ref, or "" when it has none.
func imageID(t *testing.T, ref string) string {
	t.Helper()
	out, err := exec.Command("docker", "image", "inspect", "--format", "{{.Id}}", ref).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// containerImage is the image ID a compose project's running service container uses.
func containerImage(t *testing.T, project, service string) string {
	t.Helper()
	out, err := exec.Command("docker", "ps", "-q", "--filter", "label=com.docker.compose.project="+project, "--filter", "label=com.docker.compose.service="+service).Output()
	if err != nil {
		t.Fatalf("docker ps: %v", err)
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		t.Fatalf("project %s has no running %s container", project, service)
	}
	img, err := exec.Command("docker", "inspect", "--format", "{{.Image}}", ids[0]).Output()
	if err != nil {
		t.Fatalf("docker inspect %s: %v", ids[0], err)
	}
	return strings.TrimSpace(string(img))
}

// buildMain builds main's web image as a developer would before making worktrees, and
// returns its ID.
func buildMain(t *testing.T, repo *Repo, image string) string {
	t.Helper()
	repo.Compose(t, repo.Dir, "build", "web")
	id := imageID(t, image)
	if id == "" {
		t.Fatalf("main built web but Docker has no image %s", image)
	}
	return id
}

func TestBerthWithMainsInputsDoesNotBuild(t *testing.T) {
	repo := NewRepo(t)
	mainID := buildMain(t, repo, repo.Project+"-web")
	wt := repo.AddWorktree(t, "feat-same-inputs")
	mustBerth(t, repo, wt, "add")
	s := portsStatusOf(t, repo, wt)

	// docker compose build always builds, so its dry run prints "Building" in main too.
	// What the guarantee needs is that the berth does no build work main wouldn't: the
	// same dry run as main's.
	mainDry := composeOutput(t, repo, repo.Dir, "build", "--dry-run", "web")
	berthDry := strings.ReplaceAll(composeOutput(t, repo, wt, "build", "--dry-run", "web"), s.ComposeProject, repo.Project)
	if got, want := imageBuild.FindAllString(berthDry, -1), imageBuild.FindAllString(mainDry, -1); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("docker compose build --dry-run in the berth reports build steps %q, main reports %q", got, want)
	}

	// --no-deps, because compose's dry run sometimes fails to simulate waiting on a healthy
	// dependency; the real up below still starts every service.
	if out := composeOutput(t, repo, wt, "up", "-d", "--dry-run", "--no-deps", "web"); imageBuild.MatchString(out) {
		t.Errorf("docker compose up -d --dry-run in a berth with main's build inputs would build:\n%s", out)
	}
	if out := composeOutput(t, repo, wt, "up", "-d"); imageBuild.MatchString(out) {
		t.Errorf("docker compose up -d in a berth with main's build inputs built an image:\n%s", out)
	}
	if got := containerImage(t, s.ComposeProject, "web"); got != mainID {
		t.Errorf("the berth's web runs image %s, want main's %s", got, mainID)
	}
}

func TestBerthWithChangedDockerfileBuilds(t *testing.T) {
	repo := NewRepo(t)
	mainID := buildMain(t, repo, repo.Project+"-web")
	wt := repo.AddWorktree(t, "feat-new-dockerfile")
	appendFile(t, filepath.Join(wt, "web", "Dockerfile"), "RUN echo berth > /changed\n")
	mustBerth(t, repo, wt, "add")
	s := portsStatusOf(t, repo, wt)

	out := composeOutput(t, repo, wt, "up", "-d")
	if !imageBuild.MatchString(out) {
		t.Errorf("docker compose up -d in a berth whose Dockerfile differs from main's did not build:\n%s", out)
	}
	got := containerImage(t, s.ComposeProject, "web")
	if got == mainID {
		t.Fatalf("the berth's web runs main's image %s although its Dockerfile changed", mainID)
	}
	changed, err := exec.Command("docker", "run", "--rm", "--entrypoint", "cat", got, "/changed").CombinedOutput()
	if err != nil || strings.TrimSpace(string(changed)) != "berth" {
		t.Errorf("the berth's image was not built from its own Dockerfile: cat /changed gave %q, %v", changed, err)
	}
	if id := imageID(t, repo.Project+"-web"); id != mainID {
		t.Errorf("main's image changed from %s to %s", mainID, id)
	}
}

func TestBerthBuildNeverChangesMainsImage(t *testing.T) {
	for _, tc := range []struct {
		name string
		// image is set as the web service's image: in compose.yml when not empty.
		image func(project string) string
	}{
		{"default image name", nil},
		{"image named in compose", func(project string) string { return project + "-pinned:dev" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := NewRepo(t)
			mainImage := repo.Project + "-web"
			if tc.image != nil {
				mainImage = tc.image(repo.Project)
				compose := filepath.Join(repo.Dir, "compose.yml")
				writeFile(t, compose, strings.Replace(readString(t, compose), "    build: ./web\n", "    build: ./web\n    image: "+mainImage+"\n", 1))
				repo.Git(t, "commit", "-q", "-am", "name the web image")
			}
			mainID := buildMain(t, repo, mainImage)
			wt := repo.AddWorktree(t, "feat-rebuild")
			mustBerth(t, repo, wt, "add")
			s := portsStatusOf(t, repo, wt)

			appendFile(t, filepath.Join(wt, "web", "Dockerfile"), "RUN echo berth > /changed\n")
			repo.Compose(t, wt, "build", "web")
			repo.Compose(t, wt, "up", "-d", "--no-deps", "web")

			if got := containerImage(t, s.ComposeProject, "web"); got == mainID {
				t.Fatalf("the berth's web still runs main's image %s after building a new Dockerfile", mainID)
			}
			if id := imageID(t, mainImage); id != mainID {
				t.Errorf("building in the berth changed main's image %s from %s to %s", mainImage, mainID, id)
			}
			if out := composeOutput(t, repo, repo.Dir, "up", "-d", "--dry-run", "--no-deps", "web"); imageBuild.MatchString(out) {
				t.Errorf("after a berth built, main's up would build:\n%s", out)
			}
		})
	}
}
