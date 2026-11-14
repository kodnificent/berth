package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kodnificent/berth/internal/apperr"
)

func init() {
	commands = append(commands,
		Command{Name: "exec", Summary: "run a command with a berth's variables set", Usage: "Usage: berth exec [path] -- <command> [args]\n\nThe command runs in the worktree with every variable berth env prints set, overriding\nany exported in the shell. Its stdin, stdout, stderr and exit code are its own.\n\nFlags:\n", Run: runExec},
	)
}

func runExec(ctx *Context, args []string) error {
	sep := slices.Index(args, "--")
	if sep < 0 {
		return apperr.New(apperr.Usage, "berth exec needs -- before the command, as in berth exec -- make test")
	}
	argv := args[sep+1:]
	if len(argv) == 0 {
		return apperr.New(apperr.Usage, "berth exec needs a command after --")
	}
	cfg, rec, err := findBerth("exec", args[:sep])
	if err != nil {
		return err
	}
	dir := execDir(rec.Worktree)
	if err := os.Chdir(dir); err != nil {
		return err
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return apperr.New(apperr.Generic, "cannot run %s: %v", argv[0], err)
	}
	set := managedEnv(cfg, rec)
	set["PWD"] = dir
	return runCommand(ctx, path, argv, withEnv(os.Environ(), set))
}

// execDir keeps the current directory when it is inside the worktree, so a command run
// from a package's directory runs there, and is the worktree's top otherwise.
func execDir(worktree string) string {
	wd, err := os.Getwd()
	if err != nil {
		return worktree
	}
	rel, err := filepath.Rel(worktree, realpath(wd))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return worktree
	}
	return wd
}

func withEnv(base []string, set map[string]string) []string {
	out := slices.DeleteFunc(slices.Clone(base), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		_, managed := set[k]
		return managed
	})
	for _, k := range sortedKeys(set) {
		out = append(out, k+"="+set[k])
	}
	return out
}
