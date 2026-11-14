// Package worktree resolves the git worktree berth acts on and reads the git
// administrative files that tell whether a linked worktree still exists.
package worktree

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kodnificent/berth/internal/apperr"
)

type Worktree struct {
	Path string
	// AdminDir is git's administrative directory for this worktree, <common>/worktrees/<id>
	// for a linked worktree and the common directory itself for the main one.
	AdminDir  string
	CommonDir string
	// Repository is the main worktree's path.
	Repository string
	// Branch is the branch checked out, empty on a detached HEAD.
	Branch string
}

func (w *Worktree) IsMain() bool { return w.AdminDir == w.CommonDir }

// Resolve finds the worktree containing path, or the current directory when path is
// empty.
func Resolve(path string) (*Worktree, error) {
	if path == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		path = wd
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, apperr.New(apperr.WorktreeNotFound, "%s does not exist", abs)
	}
	dir := abs
	if !info.IsDir() {
		dir = filepath.Dir(abs)
	}

	out, err := git(dir, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-dir", "--git-common-dir")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if err != nil || len(lines) != 3 {
		return nil, apperr.New(apperr.WorktreeNotFound, "%s is not inside a git worktree", abs)
	}
	w := &Worktree{Path: realpath(lines[0]), AdminDir: realpath(lines[1]), CommonDir: realpath(lines[2])}

	list, err := git(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(strings.NewReader(list))
	for sc.Scan() {
		if p, ok := strings.CutPrefix(sc.Text(), "worktree "); ok {
			w.Repository = realpath(p)
			break
		}
	}
	if w.Repository == "" {
		w.Repository = w.CommonDir
	}
	if b, err := git(dir, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		w.Branch = strings.TrimSpace(b)
	}
	return w, nil
}

// Linked describes a linked worktree from its administrative directory alone, so it
// works when the worktree's own directory is gone.
type Linked struct {
	Exists bool
	Path   string
	Locked bool
	// Prunable is true when git would delete the administrative directory on the next
	// git worktree prune: the worktree is unlocked and its directory is missing.
	Prunable bool
}

// Inspect reads a linked worktree's administrative directory. The files it reads,
// gitdir and locked, are documented in gitrepository-layout(5).
func Inspect(adminDir string) Linked {
	data, err := os.ReadFile(filepath.Join(adminDir, "gitdir"))
	if err != nil {
		return Linked{}
	}
	dotGit := strings.TrimSpace(string(data))
	if !filepath.IsAbs(dotGit) {
		dotGit = filepath.Join(adminDir, dotGit)
	}
	l := Linked{Exists: true, Path: filepath.Dir(filepath.Clean(dotGit))}
	if _, err := os.Stat(filepath.Join(adminDir, "locked")); err == nil {
		l.Locked = true
	}
	if _, err := os.Stat(dotGit); err != nil {
		l.Prunable = !l.Locked
	} else {
		l.Path = realpath(l.Path)
	}
	return l
}

// Git runs git in the worktree and returns its stdout.
func (w *Worktree) Git(args ...string) (string, error) {
	return git(w.Path, args...)
}

// GitAt runs git in dir and returns its stdout.
func GitAt(dir string, args ...string) (string, error) {
	return git(dir, args...)
}

// git runs without the variables a git hook exports, which would otherwise make git
// describe the hook's repository instead of the directory berth was pointed at.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = CleanEnv(os.Environ())
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	return stdout.String(), err
}

// CleanEnv is env without the variables git exports to its hooks, so a process berth
// starts from a hook acts on the directory it runs in, not on the hook's repository.
func CleanEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		switch strings.SplitN(kv, "=", 2)[0] {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_PREFIX":
			continue
		}
		out = append(out, kv)
	}
	return out
}

func realpath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
