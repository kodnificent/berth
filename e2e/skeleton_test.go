//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The harness creates a git repository from the fixture project and adds
// worktrees to it.
func TestHarnessCreatesRepoAndWorktrees(t *testing.T) {
	repo := NewRepo(t)

	for _, f := range []string{"compose.yml", "web/Dockerfile", ".gitignore"} {
		if _, err := os.Stat(filepath.Join(repo.Dir, f)); err != nil {
			t.Errorf("fixture file %s missing from repo: %v", f, err)
		}
	}
	if out := repo.Git(t, "status", "--porcelain"); strings.TrimSpace(out) != "" {
		t.Errorf("fresh repo is not clean:\n%s", out)
	}
	if out := repo.Git(t, "rev-parse", "--verify", "HEAD"); strings.TrimSpace(out) == "" {
		t.Errorf("fresh repo has no commit")
	}

	wt := repo.AddWorktree(t, "feat-x")
	wt2 := repo.AddWorktree(t, "feat/nested-y")

	list := repo.Git(t, "worktree", "list", "--porcelain")
	for _, p := range []string{wt, wt2} {
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatalf("worktree path %s: %v", p, err)
		}
		// git prints a Windows path with forward slashes.
		if !strings.Contains(list, "worktree "+resolved) && !strings.Contains(list, "worktree "+filepath.ToSlash(resolved)) {
			t.Errorf("git worktree list does not include %s:\n%s", resolved, list)
		}
		if _, err := os.Stat(filepath.Join(p, "compose.yml")); err != nil {
			t.Errorf("worktree %s has no compose.yml: %v", p, err)
		}
	}
}

const childEnv = "BERTH_E2E_HARNESS_CHILD"

// After a test, whether it passed or failed, no Docker project, volume,
// network or image it created is left behind. Each mode runs a child test that brings
// the fixture up and then ends that way; the parent checks what remains.
func TestHarnessLeavesNoDockerResources(t *testing.T) {
	for _, mode := range []string{"pass", "fail", "panic"} {
		// Serial: each child holds two compose networks, and Docker Desktop's default
		// address pools run out after about thirty.
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHarnessChild$", "-test.v")
			cmd.Env = append(os.Environ(), childEnv+"="+mode)
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			err := cmd.Run()

			var exitErr *exec.ExitError
			switch {
			case mode == "pass" && err != nil:
				t.Fatalf("child test should pass, got %v\n%s", err, out.String())
			case mode != "pass" && !errors.As(err, &exitErr):
				t.Fatalf("child test should fail, got %v\n%s", err, out.String())
			}

			project, resources := "", 0
			sc := bufio.NewScanner(strings.NewReader(out.String()))
			for sc.Scan() {
				line := strings.TrimSpace(sc.Text())
				if v, ok := strings.CutPrefix(line, "PROJECT="); ok {
					project = v
				}
				if v, ok := strings.CutPrefix(line, "RESOURCES="); ok {
					resources, _ = strconv.Atoi(v)
				}
			}
			if project == "" {
				t.Fatalf("child did not report its project:\n%s", out.String())
			}
			if resources == 0 {
				t.Fatalf("child created no Docker resources, so the check proves nothing:\n%s", out.String())
			}
			if left := dockerLeftovers(t, project); len(left) > 0 {
				t.Fatalf("Docker resources left behind after a %s test:\n%s", mode, strings.Join(left, "\n"))
			}
		})
	}
}

func TestHarnessChild(t *testing.T) {
	mode := os.Getenv(childEnv)
	if mode == "" {
		t.Skip("run only as a child of TestHarnessLeavesNoDockerResources")
	}
	repo := NewRepo(t)
	fmt.Printf("\nPROJECT=%s\n", repo.Project)

	repo.Compose(t, repo.Dir, "up", "-d", "--build", "--wait")

	wt := repo.AddWorktree(t, "feat-x")
	repo.Compose(t, wt, "--project-name", repo.Project+"-feat-x", "create", "--build")

	fmt.Printf("\nRESOURCES=%d\n", len(dockerLeftovers(t, repo.Project)))

	switch mode {
	case "fail":
		t.Fatal("failing on purpose")
	case "panic":
		panic("panicking on purpose")
	}
}
