package cli

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/docker"
	"github.com/kodnificent/berth/internal/lastuse"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/worktree"
)

const (
	reasonOrphan = "orphan"
	reasonIdle   = "idle"
)

type pruneEntry struct {
	ID       string    `json:"id"`
	Worktree string    `json:"worktree"`
	DryRun   bool      `json:"dry_run"`
	Removed  []item    `json:"removed"`
	Reason   string    `json:"reason"`
	LastUsed time.Time `json:"last_used"`
}

// kept is a berth prune --idle listed and then left, because it stopped being idle.
type kept struct{ why string }

func (k kept) Error() string { return k.why }

type pruner struct {
	ctx     *Context
	dryRun  bool
	results []pruneEntry
	failed  []string
}

func runPrune(ctx *Context, args []string) error {
	dryRun, args := dryRunFlag(args)
	idle, err := pruneArgs(args)
	if err != nil {
		return err
	}
	p := &pruner{ctx: ctx, dryRun: dryRun, results: []pruneEntry{}}
	live, err := p.pruneOrphans()
	if err != nil {
		return err
	}
	if idle > 0 {
		p.pruneIdle(live, idle)
	}
	return p.finish(idle > 0)
}

// pruneArgs is the --idle duration, zero when it isn't given.
func pruneArgs(args []string) (time.Duration, error) {
	var idle time.Duration
	for i := 0; i < len(args); i++ {
		a := args[i]
		value, isIdle := strings.CutPrefix(a, "--idle=")
		if a == "--idle" {
			if i+1 == len(args) {
				return 0, apperr.New(apperr.Usage, "--idle needs a duration, such as 7d, 12h or 30m")
			}
			i++
			value, isIdle = args[i], true
		}
		switch {
		case isIdle:
			d, err := parseIdle(value)
			if err != nil {
				return 0, err
			}
			idle = d
		case strings.HasPrefix(a, "-"):
			return 0, apperr.New(apperr.UnknownFlag, "unknown flag %q for berth prune", a)
		default:
			return 0, apperr.New(apperr.Usage, "berth prune takes no arguments, got %q", a)
		}
	}
	return idle, nil
}

var idlePattern = regexp.MustCompile(`^([0-9]+)([dhm])$`)

func parseIdle(s string) (time.Duration, error) {
	invalid := apperr.New(apperr.Usage, "invalid --idle duration %q: give a whole number of days, hours or minutes, such as 7d, 12h or 30m", s)
	m := idlePattern.FindStringSubmatch(s)
	if m == nil {
		return 0, invalid
	}
	unit := map[string]time.Duration{"d": 24 * time.Hour, "h": time.Hour, "m": time.Minute}[m[2]]
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n == 0 || n > math.MaxInt64/int64(unit) {
		return 0, invalid
	}
	return time.Duration(n) * unit, nil
}

// pruneOrphans prunes every orphan under one registry lock, as v0.1 did, and returns the
// berths that are not orphaned.
func (p *pruner) pruneOrphans() ([]registry.Live, error) {
	reg, err := registry.Open()
	if err != nil {
		return nil, err
	}
	defer reg.Close()
	all, err := reg.All()
	if err != nil {
		return nil, err
	}
	var live []registry.Live
	for _, b := range all {
		if !b.Orphaned {
			live = append(live, b)
			continue
		}
		last := recordedUse(b.Record).UTC().Truncate(time.Second)
		done, err := pruneOne(p.ctx, reg, b.Record, p.dryRun)
		if done != nil {
			p.results = append(p.results, pruneEntry{b.ID, b.Worktree, p.dryRun, done, reasonOrphan, last})
		}
		if err != nil {
			p.failed = append(p.failed, err.Error())
		}
	}
	return live, nil
}

// pruneIdle removes, or with a dry run lists, every berth in live unused for idle. Each
// is judged with every worktree checked however long git takes, since a check cut short
// could only make a berth look less used than it was.
func (p *pruner) pruneIdle(live []registry.Live, idle time.Duration) {
	var judged []registry.Live
	for _, b := range live {
		if !b.Locked {
			judged = append(judged, b)
		}
	}
	if len(judged) == 0 {
		return
	}
	since := time.Now().Add(-idle)
	uses, dockerErr := measureUse(judged, 0)
	for _, b := range judged {
		u := uses[b.ID]
		if idle, err := judgeIdle(u, since, dockerErr); err != nil {
			p.failed = append(p.failed, fmt.Sprintf("berth %s (%s) is kept: %v", b.ID, b.Worktree, err))
			continue
		} else if !idle {
			continue
		}
		if !p.dryRun {
			p.removeIdle(b, u.lastUsed, since)
			continue
		}
		td, err := planTeardown(b.Record, b.Worktree, p.ctx.Stderr)
		if err != nil {
			p.failed = append(p.failed, fmt.Sprintf("berth %s (%s): %v", b.ID, b.Worktree, err))
			continue
		}
		p.results = append(p.results, pruneEntry{b.ID, b.Worktree, true, td.items, reasonIdle, u.lastUsed})
	}
}

// judgeIdle is whether a berth with use u is unused since then. A berth used since is
// not idle whatever else is unknown; one that otherwise would be is an error when Docker
// or its worktree could not be asked.
func judgeIdle(u use, since time.Time, dockerErr error) (bool, error) {
	switch {
	case !u.lastUsed.Before(since):
		return false, nil
	case u.inUse == nil:
		return false, fmt.Errorf("whether it is idle is unknown, since Docker could not be reached to see if it runs (%v)", dockerErr)
	case u.unchecked != nil:
		return false, fmt.Errorf("whether it is idle is unknown, since its worktree could not be checked for changes (%v)", u.unchecked)
	}
	return true, nil
}

// removeIdle removes one idle berth through remove's own sequence. An earlier berth's
// pre-remove may have used, started or locked this one since the list, so it is judged
// again in full before its own pre-remove runs. Under the locks after a hook, only what
// the hook can't stand for is checked: the hook may rewrite the worktree, stash or commit,
// but a container running now or a berth command's use means someone else is using it.
// With no hook, use is measured again in full under the locks.
func (p *pruner) removeIdle(b registry.Live, listedUse, since time.Time) {
	fail := func(err error) {
		p.failed = append(p.failed, fmt.Sprintf("berth %s (%s): %v", b.ID, b.Worktree, err))
	}
	keep := func(why string) {
		fmt.Fprintf(p.ctx.Stderr, "berth: kept berth %s (%s): %s\n", b.ID, b.Worktree, why)
	}
	w, err := worktree.Resolve(b.Worktree)
	if err != nil {
		fail(err)
		return
	}
	// Taken by a berth remove during an earlier berth's hook: its pre-remove must not run,
	// nor anything recreate state for a berth that no longer exists.
	if gone, err := berthGone(w, b.ID); err != nil || gone {
		if err != nil {
			fail(err)
		}
		return
	}
	cfg, err := config.Load(w)
	if err != nil {
		fail(err)
		return
	}
	if worktree.Inspect(b.AdminDir).Locked {
		keep("its worktree was locked after prune listed it")
		return
	}
	recorded, _ := lastuse.Load(b.ID)
	uses, dockerErr := measureUse([]registry.Live{b}, 0)
	u := uses[b.ID]
	if idle, err := judgeIdle(u, since, dockerErr); err != nil {
		fail(fmt.Errorf("kept: %v", err))
		return
	} else if !idle {
		if u.inUse != nil && *u.inUse {
			keep("a container of it started after prune listed it")
		} else {
			keep(fmt.Sprintf("it was used after prune listed it, at %s", u.lastUsed.Format(time.RFC3339)))
		}
		return
	}

	afterHook := func(rec registry.Record) error {
		if rec.ID != b.ID {
			return kept{"its worktree has another berth now"}
		}
		if worktree.Inspect(rec.AdminDir).Locked {
			return kept{"its worktree was locked after prune judged it at its turn"}
		}
		if at, ok := lastuse.Load(rec.ID); ok && at.After(recorded) {
			return kept{fmt.Sprintf("a berth command used it at %s, after prune judged it at its turn", at.UTC().Format(time.RFC3339))}
		}
		// With no hook to account for them, worktree changes made while prune waited for
		// the locks are someone's use.
		if cfg.Hooks.PreRemove == "" {
			again, dockerErr := measureUse([]registry.Live{{Record: rec}}, 0)
			v := again[rec.ID]
			idle, err := judgeIdle(v, since, dockerErr)
			if err != nil {
				return fmt.Errorf("kept: %v", err)
			}
			if !idle || v.lastUsed.After(u.lastUsed) {
				return kept{fmt.Sprintf("it was used at %s, while prune waited for its locks", v.lastUsed.Format(time.RFC3339))}
			}
		}
		if rec.ComposeProject == "" {
			return nil
		}
		running, err := docker.ProjectRunning(rec.ComposeProject)
		if err != nil {
			return fmt.Errorf("kept: whether a container of it runs is unknown, since Docker could not be reached (%v)", err)
		}
		if len(running) > 0 {
			return kept{fmt.Sprintf("container %s of it is running after its pre-remove", running[0])}
		}
		return nil
	}
	_, done, err := removeSequence(p.ctx, w, b.ID, cfg.Hooks.PreRemove, "berth prune --idle", afterHook)
	if done != nil {
		p.results = append(p.results, pruneEntry{b.ID, b.Worktree, false, done, reasonIdle, listedUse})
	}
	var k kept
	switch {
	case errors.As(err, &k):
		keep(k.why)
	case errors.Is(err, errGone):
	case err != nil:
		fail(err)
	}
}

// berthGone is whether w no longer holds berth id.
func berthGone(w *worktree.Worktree, id string) (bool, error) {
	reg, err := registry.Open()
	if err != nil {
		return false, err
	}
	defer reg.Close()
	rec, ok, err := reg.Find(w)
	return !ok || rec.ID != id, err
}

func (p *pruner) finish(idle bool) error {
	ctx := p.ctx
	var failure error
	if len(p.failed) > 0 {
		failure = apperr.New(apperr.Generic, "%s", strings.Join(p.failed, "\n"))
	}
	if ctx.JSON {
		body := map[string]any{"dry_run": p.dryRun, "berths": p.results}
		if failure != nil {
			body["error"] = jsonError(failure)
		}
		if err := writeJSON(ctx.Stdout, body); err != nil {
			return err
		}
		if failure != nil {
			return &apperr.Reported{Code: apperr.CodeOf(failure)}
		}
		return nil
	} else if len(p.results) == 0 && len(p.failed) == 0 {
		if idle {
			fmt.Fprintln(ctx.Stdout, "No orphaned or idle berths.")
		} else {
			fmt.Fprintln(ctx.Stdout, "No orphaned berths.")
		}
	} else {
		for _, r := range p.results {
			rec := registry.Record{ID: r.ID, Worktree: r.Worktree}
			if r.Reason == reasonIdle {
				fmt.Fprint(ctx.Stdout, describeIdle(rec, r.Removed, r.LastUsed, p.dryRun))
				continue
			}
			fmt.Fprint(ctx.Stdout, describeRemoved(rec, r.Removed, p.dryRun))
		}
	}
	return failure
}

// pruneOne tears down one orphan. It runs no pre-remove: the worktree that command would
// run in is gone.
func pruneOne(ctx *Context, reg *registry.Registry, rec registry.Record, dryRun bool) ([]item, error) {
	dir := orphanDir(rec)
	// A real prune plans again under the lock and reports what it leaves then.
	planOut := ctx.Stderr
	if !dryRun {
		planOut = io.Discard
	}
	td, err := planTeardown(rec, dir, planOut)
	if err != nil {
		return nil, err
	}
	if dryRun {
		return td.items, nil
	}
	release, err := quiesce(rec.ID)
	if err != nil {
		return nil, err
	}
	defer release()
	if td, err = planTeardown(rec, dir, ctx.Stderr); err != nil {
		return nil, err
	}
	return td.execute(reg)
}

// orphanDir is an orphan's worktree directory when berth's files there are still its to
// delete: the directory exists and is no longer inside any git worktree. A directory git
// knows may be a new worktree at the same path, whose files are another berth's.
func orphanDir(rec registry.Record) string {
	if info, err := os.Stat(rec.Worktree); err != nil || !info.IsDir() {
		return ""
	}
	if _, err := worktree.Resolve(rec.Worktree); err == nil {
		return ""
	}
	return rec.Worktree
}
