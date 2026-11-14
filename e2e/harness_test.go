//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const fixtureName = "berthfixture"

var berthBin string

// shimBin runs a script by its name where only programs can be, built only for Windows.
var shimBin string

func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	if out, err := exec.Command("docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e tests need a running Docker daemon: %v\n%s", err, out)
		return 1
	}
	dir, err := os.MkdirTemp("", "berth-e2e-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	berthBin = filepath.Join(dir, "berth"+exe)
	build := exec.Command("go", "build", "-o", berthBin, "../cmd/berth")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building berth: %v\n%s", err, out)
		return 1
	}
	if exe != "" {
		shimBin = filepath.Join(dir, "shim"+exe)
		if out, err := exec.Command("go", "build", "-ldflags=-X 'main.sh="+binSh+"'", "-o", shimBin, "./testdata/shim").CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "building the script shim: %v\n%s", err, out)
			return 1
		}
	}
	return m.Run()
}

// Repo is a git repository made from the fixture project, isolated from the user's git
// config, compose variables and berth registry.
type Repo struct {
	Dir      string
	Project  string
	StateDir string
	// ConfigHome stands in for ~/.config, so the developer's own berth config never
	// reaches a test. HOME stays real because Docker reads its contexts from it.
	ConfigHome string
	env        []string
}

type Result struct {
	Stdout, Stderr string
	Exit           int
}

func NewRepo(t *testing.T) *Repo {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &Repo{
		Dir:        filepath.Join(root, "app"),
		Project:    "berthe2e" + randomHex(t, 4),
		StateDir:   filepath.Join(root, "state"),
		ConfigHome: filepath.Join(root, "config"),
	}
	t.Cleanup(func() { cleanupDocker(t, r.Project) })
	t.Cleanup(func() { endProcessesIn(root) })

	if err := os.CopyFS(r.Dir, os.DirFS("testdata/fixture")); err != nil {
		t.Fatalf("copying fixture: %v", err)
	}
	pinFixtureName(t, filepath.Join(r.Dir, "compose.yml"), r.Project)
	writeFreePorts(t, filepath.Join(r.Dir, ".env"), "WEB_PORT", "SEARCH_PORT", "DB_PORT")

	gitconfig := filepath.Join(root, "gitconfig")
	conf := "[user]\n\tname = berth e2e\n\temail = e2e@berth.invalid\n[init]\n\tdefaultBranch = main\n[commit]\n\tgpgsign = false\n" + gitPlatformConfig
	if err := os.WriteFile(gitconfig, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	r.env = append(isolatedEnviron(),
		"GIT_CONFIG_GLOBAL="+gitconfig,
		"GIT_CONFIG_NOSYSTEM=1",
		"BERTH_STATE_DIR="+r.StateDir,
		"XDG_CONFIG_HOME="+r.ConfigHome,
	)

	r.Git(t, "init", "-q")
	r.Git(t, "add", "-A")
	r.Git(t, "commit", "-q", "-m", "fixture")
	return r
}

// ShareState points this repo's berth commands at other's state directory, so both
// repos see one registry, as they would on a real machine.
func (r *Repo) ShareState(other *Repo) {
	for i, kv := range r.env {
		if strings.HasPrefix(kv, "BERTH_STATE_DIR=") {
			r.env[i] = "BERTH_STATE_DIR=" + other.StateDir
		}
	}
	r.StateDir = other.StateDir
}

// Env is the environment every command in this repo should run with.
func (r *Repo) Env() []string { return append([]string{}, r.env...) }

func (r *Repo) Git(t *testing.T, args ...string) string {
	t.Helper()
	return r.mustRun(t, r.Dir, "git", args...)
}

// AddWorktree runs git worktree add on a new branch named name, beside the main
// worktree, and returns the worktree's path.
func (r *Repo) AddWorktree(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(filepath.Dir(r.Dir), "worktrees", worktreeDirName(name))
	r.Git(t, "worktree", "add", "-q", "-b", name, path)
	return path
}

func (r *Repo) Compose(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return r.mustRun(t, dir, "docker", append([]string{"compose"}, args...)...)
}

func (r *Repo) Berth(t *testing.T, dir string, args ...string) Result {
	t.Helper()
	res, err := r.run(dir, berthBin, args...)
	if err != nil {
		t.Fatalf("berth %v: %v", args, err)
	}
	return res
}

func (r *Repo) mustRun(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	res, err := r.run(dir, name, args...)
	if err != nil || res.Exit != 0 {
		t.Fatalf("%s %s (in %s): exit %d %v\nstdout:\n%s\nstderr:\n%s", name, strings.Join(args, " "), dir, res.Exit, err, res.Stdout, res.Stderr)
	}
	return res.Stdout
}

func (r *Repo) run(dir, name string, args ...string) (Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = r.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.Exit = exitErr.ExitCode()
		err = nil
	}
	if ctx.Err() != nil {
		err = fmt.Errorf("timed out: %w", ctx.Err())
	}
	return res, err
}

// worktreeDirName is the directory a worktree named name lives in: name, or its hash when
// name is longer than this platform allows.
func worktreeDirName(name string) string {
	if len(name) <= maxWorktreeDirName {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:6])
}

// isolatedEnviron drops variables that would make git, compose or berth act on
// something other than the test's own repo, such as a COMPOSE_PROJECT_NAME exported in
// the developer's shell or GIT_DIR set by a hook running the tests.
func isolatedEnviron() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") || strings.HasPrefix(kv, "COMPOSE_") || strings.HasPrefix(kv, "BERTH_") || strings.HasPrefix(kv, "XDG_CONFIG_HOME=") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

func randomHex(t *testing.T, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func pinFixtureName(t *testing.T, path, project string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	old := "name: " + fixtureName + "\n"
	if !bytes.Contains(data, []byte(old)) {
		t.Fatalf("%s does not pin %q", path, old)
	}
	data = bytes.Replace(data, []byte(old), []byte("name: "+project+"\n"), 1)
	if err := os.WriteFile(path, data, fs.FileMode(0o644)); err != nil {
		t.Fatal(err)
	}
}

func writeFreePorts(t *testing.T, path string, keys ...string) {
	t.Helper()
	var b strings.Builder
	var held []net.Listener
	for _, k := range keys {
		l, err := net.Listen("tcp", ":0")
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, l)
		fmt.Fprintf(&b, "%s=%d\n", k, l.Addr().(*net.TCPAddr).Port)
	}
	for _, l := range held {
		l.Close()
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func belongs(name, project string) bool {
	return name == project || strings.HasPrefix(name, project+"-") || strings.HasPrefix(name, project+"_")
}

type dockerResource struct {
	kind, id, name, project string
}

func listDocker(t *testing.T, project string) []dockerResource {
	t.Helper()
	queries := []struct {
		kind string
		args []string
	}{
		{"container", []string{"ps", "-a", "--format", `{{.ID}}	{{.Names}}	{{.Label "com.docker.compose.project"}}`}},
		{"volume", []string{"volume", "ls", "--format", `{{.Name}}	{{.Name}}	{{.Label "com.docker.compose.project"}}`}},
		{"network", []string{"network", "ls", "--format", `{{.ID}}	{{.Name}}	{{.Label "com.docker.compose.project"}}`}},
		// image ls can't format labels, so an image is matched by its repository name alone.
		{"image", []string{"image", "ls", "--format", `{{.ID}}	{{.Repository}}	`}},
	}
	var found []dockerResource
	for _, q := range queries {
		out, err := exec.Command("docker", q.args...).Output()
		if err != nil {
			t.Errorf("docker %v: %v", q.args, err)
			continue
		}
		for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			f := strings.Split(line, "\t")
			if len(f) != 3 {
				continue
			}
			if belongs(f[1], project) || belongs(f[2], project) {
				found = append(found, dockerResource{q.kind, f[0], f[1], f[2]})
			}
		}
	}
	return found
}

// dockerLeftovers lists every container, volume, network and image that belongs to
// project or to any compose project derived from it.
func dockerLeftovers(t *testing.T, project string) []string {
	t.Helper()
	var out []string
	for _, r := range listDocker(t, project) {
		out = append(out, r.kind+" "+r.name)
	}
	sort.Strings(out)
	return out
}

func cleanupDocker(t *testing.T, project string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	projects := map[string]bool{}
	for _, r := range listDocker(t, project) {
		if r.project != "" {
			projects[r.project] = true
		}
	}
	for p := range projects {
		_ = exec.CommandContext(ctx, "docker", "compose", "--project-name", p, "down", "--volumes", "--remove-orphans", "--timeout", "1").Run()
	}

	// down without the compose files cannot know which images it built, and a crashed
	// test may leave resources compose no longer tracks, so remove the rest directly.
	byKind := map[string][]string{}
	seen := map[string]bool{}
	for _, r := range listDocker(t, project) {
		if !seen[r.kind+r.id] {
			seen[r.kind+r.id] = true
			byKind[r.kind] = append(byKind[r.kind], r.id)
		}
	}
	for _, step := range []struct{ kind, cmd string }{
		{"container", "rm -f -v"},
		{"network", "network rm"},
		{"volume", "volume rm -f"},
		{"image", "image rm -f"},
	} {
		if ids := byKind[step.kind]; len(ids) > 0 {
			args := append(strings.Fields(step.cmd), ids...)
			if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
				t.Errorf("docker %s: %v\n%s", step.cmd, err, out)
			}
		}
	}

	if left := dockerLeftovers(t, project); len(left) > 0 {
		t.Errorf("Docker resources still present after cleanup:\n%s", strings.Join(left, "\n"))
	}
}
