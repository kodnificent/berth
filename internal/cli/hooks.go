package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/hook"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/shell"
	"github.com/kodnificent/berth/internal/volume"
	"github.com/kodnificent/berth/internal/worktree"
)

// postAddCommand is the hidden command add starts in the background to run post-add.
const postAddCommand = "__post-add"

func init() {
	commands = append(commands,
		Command{Name: "hook", Summary: "install or uninstall the git hook that runs berth add on git worktree add", Usage: "Usage: berth hook install [-n|--dry-run] [path]\n       berth hook uninstall [-n|--dry-run] [path]\n       berth hook run <post-checkout arguments>\n\nberth hook run does what the installed hook does, for a post-checkout hook berth\ndidn't write: it runs berth add only in a worktree git worktree add just created.\n\nFlags:\n  -n, --dry-run  say what would change without changing it\n", Run: runHook},
		Command{Name: postAddCommand, Hidden: true, Run: runPostAdd},
	)
}

func runHook(ctx *Context, args []string) error {
	if len(args) > 0 && args[0] == "uninstall" {
		return runHookUninstall(ctx, args[1:])
	}
	if len(args) > 0 && args[0] == "run" {
		return runHookRun(ctx, args[1:])
	}
	if len(args) == 0 || args[0] != "install" {
		return apperr.New(apperr.Usage, "usage: berth hook install|uninstall [-n|--dry-run] [path], or berth hook run from a post-checkout hook")
	}
	var dryRun bool
	var rest []string
	for _, a := range args[1:] {
		if a == "-n" || a == "--dry-run" {
			dryRun = true
			continue
		}
		rest = append(rest, a)
	}
	path, err := pathArg("hook install", rest)
	if err != nil {
		return err
	}
	w, err := worktree.Resolve(path)
	if err != nil {
		return err
	}
	// Git starts the hook itself, and without an sh it can only say "cannot spawn" and the hook's path.
	if _, err := shell.Path(); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	hookPath, changed, err := hook.Install(w, self, dryRun)
	if err != nil {
		return err
	}
	if ctx.JSON {
		return writeJSON(ctx.Stdout, map[string]any{"hook": hookPath, "changed": changed, "dry_run": dryRun})
	}
	switch {
	case dryRun && changed:
		_, err = fmt.Fprintf(ctx.Stdout, "Would install the post-checkout hook at %s\n", hookPath)
	case dryRun || !changed:
		_, err = fmt.Fprintf(ctx.Stdout, "The post-checkout hook at %s is already installed\n", hookPath)
	default:
		_, err = fmt.Fprintf(ctx.Stdout, "Installed the post-checkout hook at %s; git worktree add now creates a berth\n", hookPath)
	}
	return err
}

func runHookUninstall(ctx *Context, args []string) error {
	dryRun, rest := dryRunFlag(args)
	path, err := pathArg("hook uninstall", rest)
	if err != nil {
		return err
	}
	w, err := worktree.Resolve(path)
	if err != nil {
		return err
	}
	hookPath, removed, err := hook.Uninstall(w, dryRun)
	if err != nil {
		return err
	}
	if ctx.JSON {
		return writeJSON(ctx.Stdout, map[string]any{"hook": hookPath, "changed": removed, "dry_run": dryRun})
	}
	switch {
	case !removed:
		_, err = fmt.Fprintf(ctx.Stdout, "There is no post-checkout hook at %s\n", hookPath)
	case dryRun:
		_, err = fmt.Fprintf(ctx.Stdout, "Would remove the post-checkout hook at %s\n", hookPath)
	default:
		_, err = fmt.Fprintf(ctx.Stdout, "Removed the post-checkout hook at %s; git worktree add no longer creates a berth\n", hookPath)
	}
	return err
}

// runHookRun is Script in Go, for a post-checkout hook berth didn't write, and like it
// never fails, because a hook's exit status becomes git's.
func runHookRun(ctx *Context, args []string) error {
	if len(args) == 0 {
		return apperr.New(apperr.Usage, `berth hook run takes post-checkout's arguments; call it as berth hook run "$@" from a post-checkout hook`)
	}
	if args[0] == "" || strings.Trim(args[0], "0") != "" {
		return nil
	}
	if info, err := os.Stat(".git"); err != nil || !info.Mode().IsRegular() {
		return nil
	}
	if err := runAdd(ctx, nil); err != nil {
		var done *apperr.Reported
		if !errors.As(err, &done) {
			fmt.Fprintf(ctx.Stderr, "berth: %v\n", err)
		}
		fmt.Fprintln(ctx.Stderr, "berth: berth add failed in the post-checkout hook; the worktree is fine, run berth add in it to retry")
	}
	return nil
}

// schedulePostAdd starts post-add in the background unless it already ran, or is
// waiting or running. It runs under the worktree lock, after startClones, so the clone
// process it waits for, if any, is already holding the clone lock.
func schedulePostAdd(stderr io.Writer, cfg *config.Config, w *worktree.Worktree, rec registry.Record) error {
	if cfg.Hooks.PostAdd == "" {
		return nil
	}
	lock, free, err := hook.TryLock(rec.ID)
	if err != nil {
		return err
	}
	if !free {
		return nil
	}
	p, ok, err := hook.Load(rec.ID)
	if err != nil {
		lock.Close()
		return err
	}
	if ok && p.Status == hook.OK {
		lock.Close()
		return nil
	}
	st, err := volume.Load(rec.ID)
	if err != nil {
		lock.Close()
		return err
	}
	// A clone volume berth couldn't even start cloning has no entry, and was warned about.
	var waits, dbWaits []string
	for _, key := range cloneKeys(cfg) {
		if _, ok := st.Entry(key); ok {
			waits = append(waits, key)
		}
	}
	for _, key := range dumpKeys(rec) {
		if _, ok := st.Database(key); ok {
			dbWaits = append(dbWaits, key)
		}
	}
	p = hook.PostAdd{
		Command:        cfg.Hooks.PostAdd,
		Dir:            w.Path,
		Source:         w.Repository,
		Changed:        hook.ChangedFiles(w),
		Waits:          waits,
		WaitsDatabases: dbWaits,
		Status:         hook.Pending,
	}
	if err := hook.Save(rec.ID, p); err != nil {
		lock.Close()
		return err
	}
	logPath, err := hook.LogPath(rec.ID)
	if err == nil {
		_, err = spawnDetached(logPath, lock, postAddCommand, rec.ID)
	} else {
		lock.Close()
	}
	if err != nil {
		p.Status, p.Error = hook.Failed, fmt.Sprintf("starting post-add: %v", err)
		fmt.Fprintf(stderr, "berth: %s\n", p.Error)
		return hook.Save(rec.ID, p)
	}
	when := "now"
	if len(p.Waits) > 0 || len(p.WaitsDatabases) > 0 {
		when = "once every clone is ready"
	}
	fmt.Fprintf(stderr, "berth: running post-add in the background %s; berth status shows how it went\n", when)
	return nil
}

// runPostAdd waits for the berth's clones, then runs post-add if every clone it waits
// for is ready. add passes it the post-add lock as fd 3.
func runPostAdd(ctx *Context, args []string) error {
	if len(args) != 1 {
		return apperr.New(apperr.Usage, "berth %s is run by berth add", postAddCommand)
	}
	if !holdInheritedLock() {
		return apperr.New(apperr.Usage, "berth %s is run by berth add, which passes it the post-add lock", postAddCommand)
	}
	id := args[0]
	p, ok, err := hook.Load(id)
	if err != nil || !ok || p.Status != hook.Pending {
		return err
	}
	if len(p.Waits) > 0 || len(p.WaitsDatabases) > 0 {
		if err := volume.WaitUnlocked(id); err != nil {
			return err
		}
		st, _, err := liveState(id, nil)
		if err != nil {
			return err
		}
		var notReady []string
		check := func(kind, key string, e *volume.Entry, ok bool) {
			switch {
			case !ok:
				notReady = append(notReady, kind+" "+key+" was never cloned")
			case e.Status != volume.Ready:
				notReady = append(notReady, fmt.Sprintf("%s %s is %s: %s", kind, key, e.Status, e.Error))
			}
		}
		for _, key := range p.Waits {
			e, ok := st.Entry(key)
			check(kindVolume, key, e, ok)
		}
		for _, key := range p.WaitsDatabases {
			e, ok := st.Database(key)
			check(kindDatabase, key, e, ok)
		}
		if len(notReady) > 0 {
			now := time.Now().UTC().Truncate(time.Millisecond)
			p.Status, p.FinishedAt = hook.Skipped, &now
			p.Error = "post-add didn't run because " + strings.Join(notReady, "; ") + "; berth add clones it again and then runs post-add"
			return hook.Save(id, p)
		}
	}
	return hook.Execute(id, &p)
}

type postAddJSON struct {
	Command    string     `json:"command"`
	Status     string     `json:"status"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	Output     string     `json:"output,omitempty"`
	Error      string     `json:"error,omitempty"`
	Log        string     `json:"log,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

func postAddEntry(id string) *postAddJSON {
	p, ok, err := hook.Live(id)
	if err != nil || !ok {
		return nil
	}
	j := &postAddJSON{Command: p.Command, Status: p.Status, ExitCode: p.ExitCode, Output: p.Output, Error: p.Error, StartedAt: p.StartedAt, FinishedAt: p.FinishedAt}
	if p.StartedAt != nil {
		j.Log, _ = hook.LogPath(id)
	}
	return j
}

func postAddRows(p *postAddJSON) []row {
	line := p.Status
	if p.ExitCode != nil && p.Status == hook.Failed {
		line += fmt.Sprintf(" (exit %d)", *p.ExitCode)
	}
	if p.Error != "" {
		line += ": " + p.Error
	}
	out := []row{{rowPostAdd, "post-add", line, postAddMark(p.Status)}}
	if p.Status == hook.Failed && p.Output != "" {
		for _, l := range strings.Split(p.Output, "\n") {
			out = append(out, row{label: "", value: "| " + l})
		}
		if p.Log != "" {
			out = append(out, row{label: "post-add log", value: p.Log})
		}
	}
	return out
}
