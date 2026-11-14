package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kodnificent/berth/internal/docker"
	"github.com/kodnificent/berth/internal/lastuse"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/worktree"
)

const (
	dockerLimit  = 3 * time.Second
	changesLimit = 1500 * time.Millisecond
	// More git processes at once than this only contend for the same filesystem, so
	// within changesLimit fewer worktrees would finish, not more (decision 0037).
	changesAtOnce = 4
)

// noteUse records that a berth command used berth id now. Call it holding the registry
// lock with the berth's record found. Inside a berth hook, with BERTH_PATH set, the
// command is berth's own work, and counting it would let pre-remove refresh the berth
// being removed.
func noteUse(id string) {
	if os.Getenv("BERTH_PATH") != "" {
		return
	}
	_ = lastuse.Record(id, time.Now())
}

type use struct {
	lastUsed time.Time
	// inUse is nil when Docker could not be asked.
	inUse *bool
	// unchecked is why the worktree was not checked for changes, nil when it was.
	unchecked error
}

// usage works out each berth's last use for list, warning on stderr about what it could
// not check, never failing.
func usage(all []registry.Live, stderr io.Writer) map[string]use {
	out, dockerErr := measureUse(all, changesLimit)
	if dockerErr != nil {
		fmt.Fprintf(stderr, "berth: Docker could not be reached, so whether each berth is in use is unknown (%v)\n", dockerErr)
	}
	var unchecked []string
	var firstErr error
	for _, b := range all {
		if err := out[b.ID].unchecked; err != nil {
			unchecked = append(unchecked, b.ID)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if len(unchecked) > 0 {
		fmt.Fprintf(stderr, "berth: could not check the worktree of berth %s for changes within %s, so it may have been used later than shown (%v)\n", strings.Join(unchecked, ", "), changesLimit, firstErr)
	}
	return out
}

// measureUse works out each berth's last use: the latest of its recorded use, a change in
// its worktree and, when a container of its compose project runs, now. A berth with
// nothing recorded counts from its creation. Docker is asked once for every berth,
// alongside the worktree checks, which limit bounds together when it is set. The error is
// Docker's.
func measureUse(all []registry.Live, limit time.Duration) (map[string]use, error) {
	now := time.Now()
	var running map[string]bool
	var dockerErr error
	changed := make([]time.Time, len(all))
	changeErrs := make([]error, len(all))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		running, dockerErr = docker.RunningProjects(dockerLimit)
	}()
	ctx := context.Background()
	if limit > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, limit)
		defer cancel()
	}
	slots := make(chan struct{}, changesAtOnce)
	for i, b := range all {
		if b.Orphaned {
			continue
		}
		if _, err := os.Stat(b.Worktree); err != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			changed[i], changeErrs[i] = worktree.LastChange(ctx, b.Worktree, b.AdminDir)
		}()
	}
	wg.Wait()

	out := map[string]use{}
	for i, b := range all {
		u := use{unchecked: changeErrs[i]}
		last := recordedUse(b.Record)
		if changeErrs[i] == nil && changed[i].After(last) {
			last = changed[i]
		}
		if dockerErr == nil {
			in := b.ComposeProject != "" && running[b.ComposeProject]
			u.inUse = &in
			if in {
				last = now
			}
		}
		if last.After(now) {
			last = now
		}
		u.lastUsed = last.UTC().Truncate(time.Second)
		out[b.ID] = u
	}
	return out, dockerErr
}

// recordedUse is the latest use berth itself recorded for rec, or its creation.
func recordedUse(rec registry.Record) time.Time {
	last := rec.CreatedAt
	if at, ok := lastuse.Load(rec.ID); ok && at.After(last) {
		last = at
	}
	return last
}
