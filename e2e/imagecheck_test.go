//go:build e2e

package e2e

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// imageCheckDelay makes add's image check wait this long before it starts, standing in
// for a build context too large to compare quickly.
const imageCheckDelay = "BERTH_TEST_IMAGE_CHECK_DELAY"

type buildEntry struct {
	Service   string `json:"service"`
	Shared    bool   `json:"shared"`
	Reason    string `json:"reason"`
	ReadBytes *int64 `json:"read_bytes"`
}

func buildsOf(t *testing.T, repo *Repo, dir string) []buildEntry {
	t.Helper()
	res := mustBerth(t, repo, dir, "status", "--json")
	var s struct {
		Builds []buildEntry `json:"builds"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &s); err != nil {
		t.Fatalf("berth status --json: %v\n%s", err, res.Stdout)
	}
	return s.Builds
}

func buildFor(t *testing.T, repo *Repo, dir, service string) buildEntry {
	t.Helper()
	for _, b := range buildsOf(t, repo, dir) {
		if b.Service == service {
			return b
		}
	}
	t.Fatalf("berth status --json reports no build check for %s", service)
	return buildEntry{}
}

// wholeRepoBuild makes web build with the whole repository as its context and read all
// of it through a bind mount, as a real project's Dockerfile does through a build arg in a
// COPY source. dockerignore is written when not empty. big/ is gitignored and listed
// under [files] clone, so every berth gets a fresh copy of main's.
func wholeRepoBuild(t *testing.T, repo *Repo, dockerignore string) {
	t.Helper()
	compose := filepath.Join(repo.Dir, "compose.yml")
	writeFile(t, compose, strings.Replace(readString(t, compose), "    build: ./web\n", "    build:\n      context: .\n      dockerfile: web/Dockerfile\n", 1))
	writeFile(t, filepath.Join(repo.Dir, "web", "Dockerfile"), "FROM busybox:1.36.1\nCOPY web/index.html /www/index.html\nRUN --mount=type=bind,target=/ctx true\nEXPOSE 80\nCMD [\"httpd\", \"-f\", \"-p\", \"80\", \"-h\", \"/www\"]\n")
	if dockerignore != "" {
		writeFile(t, filepath.Join(repo.Dir, ".dockerignore"), dockerignore)
	}
	appendFile(t, filepath.Join(repo.Dir, ".gitignore"), "big/\n")
	writeFile(t, repo.privateConfig(), "[files]\nclone = [\"big\"]\n")
	repo.Git(t, "add", "-A")
	repo.Git(t, "commit", "-q", "-m", "build from the whole repository")
}

// bigFiles writes n files of size bytes of random data under dir/big, plus small files
// standing in for a dependency directory, and removes them when the test ends. Random
// data, so nothing on the way can make reading them cheaper than it would be.
func bigFiles(t *testing.T, dir string, n int, size int64, small int) {
	t.Helper()
	root := filepath.Join(dir, "big")
	t.Cleanup(func() { os.RemoveAll(root) })
	if err := os.MkdirAll(filepath.Join(root, "pkgs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		f, err := os.Create(filepath.Join(root, fmt.Sprintf("blob-%d.bin", i)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.CopyN(f, rand.Reader, size); err != nil {
			f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	chunk := make([]byte, 1024)
	for i := 0; i < small; i++ {
		rand.Read(chunk)
		if err := os.WriteFile(filepath.Join(root, "pkgs", fmt.Sprintf("p%05d.js", i)), chunk, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func timedBerth(t *testing.T, repo *Repo, dir string, args ...string) (Result, time.Duration) {
	t.Helper()
	start := time.Now()
	res := mustBerth(t, repo, dir, args...)
	return res, time.Since(start)
}

// A berth whose large build context is a fresh copy of main's gets main's image without
// berth reading the copies: tracked files are settled by git, cloned files by size and
// modification time.
func TestLargeContextSharesMainsImageWithoutReadingIt(t *testing.T) {
	repo := NewRepo(t)
	wholeRepoBuild(t, repo, ".git\n.env\ncompose.berth.yml\n")
	mainID := buildMain(t, repo, repo.Project+"-web")
	bigFiles(t, repo.Dir, 3, 100<<20, 10000)

	wt := repo.AddWorktree(t, "feat-large-context")
	_, took := timedBerth(t, repo, wt, "add")
	t.Logf("berth add with a 310 MB build context took %s", took)
	if took > 10*time.Second {
		t.Errorf("berth add took %s with a large build context, want a few seconds", took)
	}

	b := buildFor(t, repo, wt, "web")
	if !b.Shared {
		t.Fatalf("the berth doesn't use main's web image although its build context is a copy of main's: %+v", b)
	}
	if b.ReadBytes == nil || *b.ReadBytes > 1<<20 {
		t.Errorf("berth read %v bytes of the build context to compare it, want next to none: %+v", b.ReadBytes, b)
	}
	if out := composeOutput(t, repo, wt, "up", "-d", "--dry-run", "--no-deps", "web"); imageBuild.MatchString(out) {
		t.Errorf("docker compose up in a berth with main's build inputs would build:\n%s", out)
	}
	if id := imageID(t, portsStatusOf(t, repo, wt).ComposeProject+"-web"); id != mainID {
		t.Errorf("the berth's web image is %s, want main's %s", id, mainID)
	}
}

// When deciding would mean reading more of the build context than berth's bound, berth
// doesn't share main's image, says so in add and status, and up builds in the berth.
func TestImageCheckOverBudgetBuilds(t *testing.T) {
	repo := NewRepo(t)
	wholeRepoBuild(t, repo, ".git\n.env\ncompose.berth.yml\n")
	buildMain(t, repo, repo.Project+"-web")
	bigFiles(t, repo.Dir, 1, 100<<20, 0)

	wt := repo.AddWorktree(t, "feat-over-budget")
	// The same bytes with a new modification time, as a copy that didn't keep it gives:
	// only reading both copies could tell they are equal.
	repo.mustRun(t, wt, "cp", "-R", filepath.Join(repo.Dir, "big"), filepath.Join(wt, "big"))
	t.Cleanup(func() { os.RemoveAll(filepath.Join(wt, "big")) })

	res, took := timedBerth(t, repo, wt, "add")
	t.Logf("berth add over the budget took %s", took)
	if took > 10*time.Second {
		t.Errorf("berth add took %s over the budget, want a few seconds", took)
	}
	if !strings.Contains(res.Stderr, "web") || !strings.Contains(res.Stderr, "MB") {
		t.Errorf("berth add doesn't say it skipped comparing web's build inputs, or why:\n%s", res.Stderr)
	}
	b := buildFor(t, repo, wt, "web")
	if b.Shared {
		t.Fatalf("the berth shares main's image although berth couldn't afford to compare: %+v", b)
	}
	if !strings.Contains(b.Reason, "MB") || !strings.Contains(b.Reason, ".dockerignore") {
		t.Errorf("status gives the reason %q, want how much it would read and a .dockerignore suggestion", b.Reason)
	}
	if text := mustBerth(t, repo, wt, "status").Stdout; !strings.Contains(text, "web") || !strings.Contains(text, b.Reason) {
		t.Errorf("berth status doesn't name web and the reason %q:\n%s", b.Reason, text)
	}
	if out := composeOutput(t, repo, wt, "up", "-d", "--dry-run", "--no-deps", "web"); !imageBuild.MatchString(out) {
		t.Errorf("docker compose up in a berth that doesn't share main's image wouldn't build:\n%s", out)
	}
}

// A build whose context is the whole repository with no .dockerignore is named as such,
// in add, status and doctor in main, with the suggestion to add a .dockerignore.
func TestWholeRepoContextWithoutDockerignore(t *testing.T) {
	repo := NewRepo(t)
	wholeRepoBuild(t, repo, "")
	buildMain(t, repo, repo.Project+"-web")
	bigFiles(t, repo.Dir, 1, 100<<20, 2000)

	d := doctor(t, repo, nil, repo.Dir)
	if d.Exit != 0 {
		t.Errorf("doctor in main exited %d for a warning, want 0:\n%s", d.Exit, d.Raw)
	}
	found := false
	for _, f := range d.with("build_context") {
		if strings.Contains(f.Location, "web") && strings.Contains(f.Message, ".dockerignore") {
			found = true
		}
	}
	if !found {
		t.Errorf("doctor in main doesn't warn that web builds the whole repository with no .dockerignore:\n%s", d.Raw)
	}

	wt := repo.AddWorktree(t, "feat-no-dockerignore")
	res, took := timedBerth(t, repo, wt, "add")
	t.Logf("berth add with the whole repository as context took %s", took)
	if took > 10*time.Second {
		t.Errorf("berth add took %s, want a few seconds", took)
	}
	if !strings.Contains(res.Stderr, "web") || !strings.Contains(res.Stderr, "no .dockerignore") {
		t.Errorf("berth add doesn't say web's context is the whole repository with no .dockerignore:\n%s", res.Stderr)
	}
	b := buildFor(t, repo, wt, "web")
	if b.Shared {
		t.Fatalf("the berth shares main's image although its context holds the berth's own .env: %+v", b)
	}
	if !strings.Contains(b.Reason, "whole repository") || !strings.Contains(b.Reason, "no .dockerignore") {
		t.Errorf("status gives the reason %q, want it to name the whole repository and the missing .dockerignore", b.Reason)
	}
}

// add holds the machine-wide lock only for registry and port bookkeeping, so list and
// status return promptly while another add is busy comparing build inputs.
func TestListAndStatusDoNotWaitForABusyAdd(t *testing.T) {
	repo := NewRepo(t)
	buildMain(t, repo, repo.Project+"-web")
	first := repo.AddWorktree(t, "feat-first")
	mustBerth(t, repo, first, "add")
	second := repo.AddWorktree(t, "feat-busy")

	const delay = 8 * time.Second
	cmd := exec.Command(berthBin, "add")
	cmd.Dir = second
	cmd.Env = append(repo.Env(), imageCheckDelay+"="+delay.String())
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	finished := false
	t.Cleanup(func() {
		if !finished {
			cmd.Process.Kill()
			<-done
		}
	})
	// Long enough for the busy add to register its berth and reach the check.
	time.Sleep(2 * time.Second)

	for _, args := range [][]string{{"list"}, {"status"}, {"list", "--json"}} {
		dir := first
		if args[0] == "list" {
			dir = repo.Dir
		}
		_, took := timedBerth(t, repo, dir, args...)
		if took > 2*time.Second {
			t.Errorf("berth %s took %s while another add was busy, want it to return promptly", strings.Join(args, " "), took)
		}
	}
	select {
	case err := <-done:
		finished = true
		t.Fatalf("the busy add finished after %s, before list and status were run, so they proved nothing (%v):\n%s", time.Since(start), err, out.String())
	default:
	}
	err := <-done
	finished = true
	if err != nil {
		t.Fatalf("the busy add failed: %v\n%s", err, out.String())
	}
	if took := time.Since(start); took < delay {
		t.Errorf("the busy add finished after %s, sooner than its %s check", took, delay)
	}
	if b := buildFor(t, repo, second, "web"); !b.Shared {
		t.Errorf("the busy add didn't give its berth main's image: %+v", b)
	}
}
