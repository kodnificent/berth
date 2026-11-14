package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/shell"
	"github.com/kodnificent/berth/internal/worktree"
)

const configUsage = "Usage: berth config path [--repo|--user] [path]\n       berth config edit [path]\n\n" +
	"path prints the personal config's path, the one in use or where edit would create it.\n" +
	"edit opens the personal config in $VISUAL or $EDITOR, creating it first, then validates the config.\n\n" +
	"Flags:\n  --repo     print the committed berth.toml's path instead\n  --user     print the user config's path instead\n"

const personalHeader = "# Your personal berth config for this repository. It is never committed.\n" +
	"# Keys here override berth.toml. Reference: https://useberth.dev/docs/reference/configuration/\n"

func init() {
	commands = append(commands, Command{Name: "config", Summary: "print the path of, or edit, the personal config", Usage: configUsage, Run: runConfig})
}

func runConfig(ctx *Context, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "path":
			return runConfigPath(ctx, args[1:])
		case "edit":
			return runConfigEdit(ctx, args[1:])
		}
	}
	return apperr.New(apperr.Usage, "usage: berth config path|edit [path]")
}

func runConfigPath(ctx *Context, args []string) error {
	layer := "personal"
	var rest []string
	for _, a := range args {
		switch a {
		case "--repo", "--user":
			if layer != "personal" {
				return apperr.New(apperr.Usage, "berth config path takes one of --repo and --user")
			}
			layer = a[2:]
		default:
			rest = append(rest, a)
		}
	}
	p, err := pathArg("config path", rest)
	if err != nil {
		return err
	}
	var path string
	var exists bool
	if layer == "user" {
		if path, err = config.UserPath(); err != nil {
			return err
		}
		_, err = os.Stat(path)
		exists = err == nil
	} else {
		w, err := worktree.Resolve(p)
		if err != nil {
			return err
		}
		if layer == "repo" {
			path = config.RepoPath(w)
			_, err = os.Stat(path)
			exists = err == nil
		} else if path, exists, err = config.Personal(w); err != nil {
			return err
		}
	}
	if ctx.JSON {
		return writeJSON(ctx.Stdout, map[string]any{"layer": layer, "path": path, "exists": exists})
	}
	_, err = fmt.Fprintln(ctx.Stdout, path)
	return err
}

func runConfigEdit(ctx *Context, args []string) error {
	p, err := pathArg("config edit", args)
	if err != nil {
		return err
	}
	w, err := worktree.Resolve(p)
	if err != nil {
		return err
	}
	path, exists, err := config.Personal(w)
	if err != nil {
		return err
	}
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		return apperr.New(apperr.Generic, "no editor set; set $VISUAL or $EDITOR, or edit %s directly", path)
	}
	if !exists {
		if err := createPersonal(w, path); err != nil {
			return err
		}
	}

	// Through the shell, as git runs its editor, so a command with flags such as
	// "code --wait" works while the path stays a separate argument.
	cmd, err := shell.Command(nil, editor+` "$@"`, editor, path)
	if err != nil {
		return err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, ctx.Stdout, ctx.Stderr
	if ctx.JSON {
		cmd.Stdout = ctx.Stderr
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor %q failed on %s: %w", editor, path, err)
	}
	if _, err := config.Load(w); err != nil {
		return err
	}
	if ctx.JSON {
		return writeJSON(ctx.Stdout, map[string]any{"path": path, "created": !exists})
	}
	_, err = fmt.Fprintf(ctx.Stdout, "%s is valid.\n", path)
	return err
}

// createPersonal writes the header and, for the visible file, the info/exclude entry
// that keeps it out of git status.
func createPersonal(w *worktree.Worktree, path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if err == nil {
		if _, err := f.WriteString(personalHeader); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	if filepath.Base(path) != config.LocalName {
		return nil
	}
	return exclude(&worktree.Worktree{Path: w.Repository, CommonDir: w.CommonDir}, []string{config.LocalName})
}
