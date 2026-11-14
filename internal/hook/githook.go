package hook

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/safefile"
	"github.com/kodnificent/berth/internal/shell"
	"github.com/kodnificent/berth/internal/worktree"
)

// marker identifies a post-checkout hook berth wrote, so berth rewrites its own hook
// and never someone else's.
const marker = "# berth post-checkout hook"

// Script is the post-checkout hook that runs berth at path. git worktree add is the
// only checkout git runs post-checkout for with an all-zero previous HEAD, and a linked
// worktree is the only checkout whose .git is a file. The hook always exits 0, because
// its exit status becomes git's, and a berth that failed must not fail the worktree.
func Script(berth string) string {
	return `#!/bin/sh
` + marker + `, written by berth hook install.
# Delete this file to stop git worktree add from creating berths.
case "$1" in *[!0]*) exit 0 ;; esac
[ -f .git ] || exit 0
berth=` + shellQuote(berth) + `
[ -x "$berth" ] || berth=$(command -v berth) || {
	echo "berth: the post-checkout hook cannot find berth; run berth add in this worktree" >&2
	exit 0
}
"$berth" add </dev/null || echo "berth: berth add failed in the post-checkout hook; the worktree is fine, run berth add in it to retry" >&2
exit 0
`
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// RunLine is the line a user adds to a post-checkout hook in a folder berth won't write
// into. Such a folder is often committed and shared, so the line finds berth on PATH
// rather than naming one machine's binary, and it never fails a checkout: not where berth
// isn't installed, nor with an older berth that has no hook run.
const RunLine = `if command -v berth >/dev/null 2>&1; then berth hook run "$@" || true; fi`

// Hooks is the folder git runs w's hooks from.
type Hooks struct {
	// Dir is the folder with symlinks resolved.
	Dir string
	// Setting and Origin are core.hooksPath and the config file that set it, both empty
	// when it is unset.
	Setting, Origin string
	// InWorkTree is true when Dir is inside a work tree of the repository and outside its
	// git directory, so it holds the repository's own files.
	InWorkTree bool
}

func (h Hooks) Path() string { return filepath.Join(h.Dir, "post-checkout") }

// HooksDir finds the folder git runs w's hooks from. A relative core.hooksPath is
// resolved against the main worktree, because git resolves it against the worktree it
// runs in, and git worktree add run from main is the run that matters.
func HooksDir(w *worktree.Worktree) (Hooks, error) {
	common := realpath(w.CommonDir)
	out, err := w.Git("config", "--type=path", "--show-origin", "--get", "core.hooksPath")
	origin, value, _ := strings.Cut(strings.TrimSpace(out), "\t")
	if err != nil || value == "" {
		return Hooks{Dir: filepath.Join(common, "hooks")}, nil
	}
	h := Hooks{Setting: value, Origin: origin}
	dir := value
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(w.Repository, dir)
	}
	h.Dir = realpath(dir)
	if within(h.Dir, common) || within(h.Dir, realpath(w.AdminDir)) {
		return h, nil
	}
	trees, err := worktreePaths(w)
	if err != nil {
		return Hooks{}, err
	}
	for _, t := range trees {
		if within(h.Dir, t) {
			h.InWorkTree = true
		}
	}
	return h, nil
}

func worktreePaths(w *worktree.Worktree) ([]string, error) {
	list, err := w.Git("worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	paths := []string{realpath(w.Path), realpath(w.Repository)}
	for _, line := range strings.Split(list, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			paths = append(paths, realpath(p))
		}
	}
	return paths, nil
}

// realpath resolves symlinks in the longest part of p that exists, so a hooks folder
// that doesn't exist yet, or a link to one, is still placed where it would be created.
func realpath(p string) string { return resolve(filepath.Clean(p), 40) }

func resolve(p string, hops int) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	if target, err := os.Readlink(p); err == nil && hops > 0 {
		if !filepath.IsAbs(target) {
			target = filepath.Join(parent, target)
		}
		return resolve(filepath.Clean(target), hops-1)
	}
	return filepath.Join(resolve(parent, hops), filepath.Base(p))
}

func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (h Hooks) describe() string {
	return fmt.Sprintf("core.hooksPath is set to %s (%s), so git runs hooks from %s, a folder inside this repository's work tree. Berth never writes into the repository's own files, committed or not", h.Setting, h.Origin, h.Dir)
}

// Instructions says how to make the post-checkout hook in h run berth by hand.
func (h Hooks) Instructions() string {
	if _, err := os.Stat(h.Path()); err == nil {
		return fmt.Sprintf("add this line to %s:\n    %s", h.Path(), RunLine)
	}
	return fmt.Sprintf("create %s, executable, with these lines:\n    #!/bin/sh\n    %s", h.Path(), RunLine)
}

// Install writes the post-checkout hook for w's repository in the folder git runs hooks
// from, unless dryRun, and records that berth installed it. changed is false when the
// hook was already exactly this one.
func Install(w *worktree.Worktree, berth string, dryRun bool) (path string, changed bool, err error) {
	h, err := HooksDir(w)
	if err != nil {
		return "", false, err
	}
	if h.InWorkTree {
		return "", false, apperr.New(apperr.Generic, "%s, so berth hook install wrote nothing. To create a berth on every git worktree add, %s", h.describe(), h.Instructions())
	}
	path = h.Path()
	want := Script(berth)
	have, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return "", false, err
	case string(have) == want:
		if dryRun {
			return path, false, nil
		}
		return path, false, remember(w, path)
	case !bytes.Contains(have, []byte(marker)):
		return "", false, apperr.New(apperr.Generic,
			"%s already exists and berth didn't write it, so berth won't replace it; add this line to it to create berths on git worktree add:\n    %s",
			path, RunLine)
	}
	if dryRun {
		return path, true, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", false, err
	}
	tmp := path + ".berth-tmp"
	if err := os.WriteFile(tmp, []byte(want), 0o755); err != nil {
		return "", false, err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return "", false, err
	}
	if err := safefile.Replace(tmp, path); err != nil {
		return "", false, err
	}
	return path, true, remember(w, path)
}

// Uninstall deletes the post-checkout hook for w's repository, unless dryRun, but only
// when it is exactly a hook berth hook install wrote, and forgets that berth installed
// it. removed is false when there is no hook to remove.
func Uninstall(w *worktree.Worktree, dryRun bool) (path string, removed bool, err error) {
	h, err := HooksDir(w)
	if err != nil {
		return "", false, err
	}
	if h.InWorkTree {
		return "", false, apperr.New(apperr.Generic, "%s, so berth hook uninstall removed nothing. To stop git worktree add from creating berths, delete the berth hook run line from %s yourself", h.describe(), h.Path())
	}
	path = h.Path()
	have, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if dryRun {
			return path, false, nil
		}
		return path, false, forget(w)
	case err != nil:
		return "", false, err
	case !bytes.Contains(have, []byte(marker)):
		return "", false, apperr.New(apperr.Generic, "%s exists and berth didn't write it, so berth won't delete it", path)
	case !written(string(have)):
		return "", false, apperr.New(apperr.Generic, "%s was written by berth but has been edited since, so berth won't delete it; remove the berth lines from it yourself", path)
	}
	if dryRun {
		return path, true, nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", false, err
	}
	return path, true, forget(w)
}

// Problem says why the post-checkout hook berth installed for w's repository may no
// longer run berth, or is empty when it does, or when berth never installed one.
func Problem(w *worktree.Worktree) (where, why string, err error) {
	recorded, err := remembered(w)
	if err != nil {
		return "", "", err
	}
	h, err := HooksDir(w)
	if err != nil {
		return "", "", err
	}
	have, readErr := os.ReadFile(h.Path())
	if recorded == "" && !bytes.Contains(have, []byte(marker)) {
		return "", "", nil
	}
	switch {
	case h.InWorkTree && bytes.Contains(have, []byte("berth hook run")):
		return "", "", nil
	case h.InWorkTree:
		return h.Dir, fmt.Sprintf("berth hook install wrote %s, but core.hooksPath now points at %s inside the work tree, so git worktree add no longer creates a berth; %s", recorded, h.Dir, h.Instructions()), nil
	case errors.Is(readErr, fs.ErrNotExist) && recorded != h.Path():
		return h.Path(), fmt.Sprintf("berth hook install wrote %s, but git now runs hooks from %s, so git worktree add no longer creates a berth; run berth hook install", recorded, h.Dir), nil
	case errors.Is(readErr, fs.ErrNotExist):
		return h.Path(), "berth hook install wrote this hook, but it is gone, so git worktree add no longer creates a berth; run berth hook install", nil
	case readErr != nil:
		return "", "", readErr
	case !bytes.Contains(have, []byte(marker)):
		return h.Path(), "this hook is no longer the one berth hook install wrote, so git worktree add may not create a berth; add this line to it:\n    " + RunLine, nil
	case !written(string(have)):
		return h.Path(), "berth wrote this hook but it has been edited since, so it may not run berth as installed; berth hook install rewrites it, dropping the edits", nil
	}
	return "", "", nil
}

// installed is the record of a hook berth installed, kept in berth's state directory so
// berth knows which repositories expect one without writing to the repository.
type installed struct {
	CommonDir string `json:"common_dir"`
	Hook      string `json:"hook"`
}

func recordPath(w *worktree.Worktree) (string, error) {
	d, err := registry.StateDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(realpath(w.CommonDir)))
	return filepath.Join(d, "hooks", hex.EncodeToString(sum[:8])+".json"), nil
}

func remember(w *worktree.Worktree, hook string) error {
	p, err := recordPath(w)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(installed{CommonDir: realpath(w.CommonDir), Hook: hook})
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return safefile.Replace(tmp, p)
}

func remembered(w *worktree.Worktree) (string, error) {
	p, err := recordPath(w)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var rec installed
	if err := json.Unmarshal(data, &rec); err != nil {
		return "", fmt.Errorf("reading %s: %w", p, err)
	}
	return rec.Hook, nil
}

func forget(w *worktree.Worktree) error {
	p, err := recordPath(w)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// written reports whether hook is Script for whichever berth path it names, so a hook
// installed by an older or moved binary still counts as berth's own.
func written(hook string) bool {
	for _, line := range strings.Split(hook, "\n") {
		if quoted, ok := strings.CutPrefix(line, "berth='"); ok && strings.HasSuffix(quoted, "'") {
			return hook == Script(strings.ReplaceAll(strings.TrimSuffix(quoted, "'"), `'\''`, "'"))
		}
	}
	return false
}

// PreRemove runs command in the berth's worktree and waits for it, with its output on
// out. A non-zero exit is an error.
func PreRemove(command, dir, source string, changed []string, out io.Writer) error {
	cmd, err := shell.Command(append(worktree.CleanEnv(os.Environ()), Env(dir, source, changed)...), command)
	if err != nil {
		return fmt.Errorf("pre-remove failed (%v)", err)
	}
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pre-remove failed (%v)", err)
	}
	return nil
}
