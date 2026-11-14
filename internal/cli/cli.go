// Package cli parses berth's global flags, dispatches commands and reports their
// results. Only exec touches stdin, and only to hand it to the command it runs, so no
// command can ever prompt.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"strings"

	"github.com/kodnificent/berth/internal/apperr"
)

type Context struct {
	JSON   bool
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

type Command struct {
	Name    string
	Summary string
	// Usage is the command's synopsis and flags, printed by -h or --help after it.
	Usage string
	// Hidden commands are run by berth itself and left out of help.
	Hidden bool
	Run    func(ctx *Context, args []string) error
}

var commands []Command

func lookup(name string) (Command, bool) {
	for _, c := range commands {
		if c.Name == name {
			return c, true
		}
	}
	return Command{}, false
}

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, version string) int {
	ctx := &Context{Stdin: stdin, Stdout: stdout, Stderr: stderr}
	args, ctx.JSON = extractJSON(args)

	var showVersion, showHelp bool
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "--version":
			showVersion = true
		case "-h", "--help":
			showHelp = true
		default:
			return report(apperr.New(apperr.UnknownFlag, "unknown flag %q; run berth --help for usage", args[0]), ctx.JSON, stdout, stderr)
		}
		args = args[1:]
	}

	switch {
	case showVersion:
		return finish(printVersion(ctx, version), ctx)
	case showHelp:
		return finish(printHelp(ctx, stdout), ctx)
	case len(args) == 0:
		if !ctx.JSON {
			_ = printHelp(ctx, stderr)
		}
		return report(apperr.New(apperr.Usage, "no command given; run berth --help for usage"), ctx.JSON, stdout, stderr)
	}

	cmd, ok := lookup(args[0])
	if !ok {
		return report(apperr.New(apperr.UnknownCommand, "unknown command %q; run berth --help for the list of commands", args[0]), ctx.JSON, stdout, stderr)
	}
	if !cmd.Hidden && wantsHelp(args[1:]) {
		return finish(printCommandHelp(ctx, cmd), ctx)
	}
	return finish(cmd.Run(ctx, args[1:]), ctx)
}

// wantsHelp reports whether -h or --help comes before any "--" separator, wherever it
// sits among the command's arguments.
func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

func printCommandHelp(ctx *Context, cmd Command) error {
	if ctx.JSON {
		return writeJSON(ctx.Stdout, map[string]string{"command": cmd.Name, "summary": cmd.Summary, "usage": cmd.Usage})
	}
	_, err := fmt.Fprintf(ctx.Stdout, "berth %s: %s\n\n%s\n  --json     machine-readable output on stdout\n  -h, --help print this help\n", cmd.Name, cmd.Summary, strings.TrimRight(cmd.Usage, "\n"))
	return err
}

// extractJSON removes --json from anywhere before a "--" separator, so agents can
// append it to any command line.
func extractJSON(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	found := false
	for i, a := range args {
		if a == "--" {
			out = append(out, args[i:]...)
			break
		}
		if a == "--json" {
			found = true
			continue
		}
		out = append(out, a)
	}
	return out, found
}

// exitStatus is the exit code of a command berth ran, given back unchanged.
type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func finish(err error, ctx *Context) int {
	if err == nil {
		return 0
	}
	var status exitStatus
	if errors.As(err, &status) {
		return int(status)
	}
	var done *apperr.Reported
	if errors.As(err, &done) {
		return done.Code.Exit()
	}
	return report(err, ctx.JSON, ctx.Stdout, ctx.Stderr)
}

func report(err error, asJSON bool, stdout, stderr io.Writer) int {
	code, next := apperr.CodeOf(err), apperr.NextOf(err)
	if asJSON {
		_ = writeJSON(stdout, struct {
			Error errorJSON `json:"error"`
		}{errorJSON{code, err.Error(), next}})
	} else {
		fmt.Fprintf(stderr, "berth: %s\n", err)
		for _, h := range next {
			fmt.Fprintf(stderr, "next: %s\n", h)
		}
	}
	return code.Exit()
}

func jsonError(err error) *errorJSON {
	if err == nil {
		return nil
	}
	return &errorJSON{apperr.CodeOf(err), err.Error(), apperr.NextOf(err)}
}

type errorJSON struct {
	Code    apperr.Code `json:"code"`
	Message string      `json:"message"`
	Next    []string    `json:"next,omitempty"`
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// Version returns the version stamped at build time, falling back to the module
// version recorded by go install, so every binary reports something meaningful.
func Version(stamped string) string {
	if stamped != "" {
		return stamped
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return strings.TrimPrefix(info.Main.Version, "v")
	}
	return "dev"
}

func printVersion(ctx *Context, version string) error {
	if ctx.JSON {
		return writeJSON(ctx.Stdout, map[string]string{"version": version})
	}
	_, err := fmt.Fprintf(ctx.Stdout, "berth %s\n", version)
	return err
}

func printHelp(ctx *Context, w io.Writer) error {
	if ctx.JSON {
		type entry struct {
			Name    string `json:"name"`
			Summary string `json:"summary"`
		}
		list := make([]entry, 0, len(commands))
		for _, c := range commands {
			if c.Hidden {
				continue
			}
			list = append(list, entry{c.Name, c.Summary})
		}
		return writeJSON(w, map[string]any{"commands": list})
	}
	var b strings.Builder
	b.WriteString("berth gives every git worktree its own copy of the environment.\n\n")
	b.WriteString("Usage: berth [--json] <command> [args]\n\nCommands:\n")
	if len(commands) == 0 {
		b.WriteString("  (none yet)\n")
	}
	for _, c := range commands {
		if c.Hidden {
			continue
		}
		fmt.Fprintf(&b, "  %-10s %s\n", c.Name, c.Summary)
	}
	b.WriteString("\nFlags:\n  --json     machine-readable output on stdout\n  --version  print the version\n  -h, --help print this help\n")
	_, err := io.WriteString(w, b.String())
	return err
}
