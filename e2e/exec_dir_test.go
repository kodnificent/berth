//go:build e2e

package e2e

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestExecFindsARelativeCommandInTheWorktree(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-exec-relative")
	mustBerth(t, repo, wt, "add")
	tool := filepath.Join(wt, "sub", "tool")
	writeFile(t, tool, "#!/bin/sh\necho worktree tool\n")
	if err := makeExecutable(tool); err != nil {
		t.Fatal(err)
	}

	res := mustBerth(t, repo, repo.Dir, "exec", wt, "--", "./sub/tool")
	if res.Stdout != "worktree tool\n" {
		t.Errorf("berth exec %s -- ./sub/tool from main printed %q, want the worktree's tool", wt, res.Stdout)
	}
}

func TestExecSetsPWDToTheWorktree(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-exec-pwd")
	mustBerth(t, repo, wt, "add")

	res := mustBerth(t, repo, repo.Dir, "exec", wt, "--", "env")
	var pwd []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		if v, ok := strings.CutPrefix(line, "PWD="); ok {
			pwd = append(pwd, v)
		}
	}
	if len(pwd) != 1 || pwd[0] != wt {
		t.Errorf("berth exec %s -- env from main gave PWD %q, want only the worktree %q", wt, pwd, wt)
	}
}
