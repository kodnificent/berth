package cli

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/docker"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/volume"
	"github.com/kodnificent/berth/internal/worktree"
)

func init() {
	commands = append(commands, Command{
		Name:    "reset",
		Summary: "discard a berth's cloned volumes, databases and SQLite files and make them again from main",
		Usage:   "Usage: berth reset [-n|--dry-run] [path]\n\nFlags:\n  -n, --dry-run  list the volumes, databases and SQLite files that would be discarded without touching them\n",
		Run:     runReset,
	})
}

type discarded struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Volume string `json:"volume,omitempty"`
	Target string `json:"target"`
}

type resetJSON struct {
	ID        string      `json:"id"`
	Worktree  string      `json:"worktree"`
	DryRun    bool        `json:"dry_run"`
	Discarded []discarded `json:"discarded"`
}

func cloneTarget(rec registry.Record, key string) string { return rec.ComposeProject + "_" + key }

func runReset(ctx *Context, args []string) error {
	dryRun, args := dryRunFlag(args)
	w, err := resolveBerthWorktree("reset", args)
	if err != nil {
		return err
	}
	cfg, err := config.Load(w)
	if err != nil {
		return err
	}
	lock, err := registry.LockWorktree(w)
	if err != nil {
		return err
	}
	defer lock.Close()
	rec, err := findRecord(w)
	if err != nil {
		return err
	}
	keys := cloneKeys(cfg)
	if rec.ComposeFile != "" || len(keys) > 0 {
		if err := refuseRunning(rec); err != nil {
			return err
		}
	}
	refreshDatabaseSources(&rec, cfg)
	dbs, err := planDatabaseReset(w, rec)
	if err != nil {
		return err
	}
	defer dbs.close()
	rs, err := planRedisReset(w, rec)
	if err != nil {
		return err
	}
	var plan *composePlan
	var gone []discarded
	if len(keys) > 0 {
		if plan, gone, err = planVolumeReset(w, cfg, rec, keys); err != nil {
			return err
		}
	}
	if dryRun {
		return printReset(ctx, rec, slices.Concat(gone, dbs.discarded(), rs.discarded(), sqliteDiscards(rec, w.Path)), true)
	}

	dumps := dumpKeys(rec)
	var cloneLock *os.File
	var st volume.State
	if len(keys) > 0 || len(dumps) > 0 {
		if cloneLock, st, err = lockClones(ctx.Stderr, rec); err != nil {
			return err
		}
		if cloneLock == nil {
			return apperr.New(apperr.Generic, "berth %s's volumes or databases are still being cloned, so reset discarded nothing; wait with berth status --wait and run berth reset again", rec.ID)
		}
		// Saved before any volume is emptied or database dropped, so a reset that dies from
		// here on is recovered as failed and no emptied copy is ever reported ready.
		for _, key := range keys {
			if e, ok := st.Entry(key); ok {
				e.Status, e.Error, e.Reset = volume.Cloning, "", true
			}
		}
		for _, db := range databases(rec) {
			if !clonedStrategy(db.Strategy) {
				continue
			}
			e, ok := st.Database(db.Key)
			if !ok {
				st.PutDatabase(volume.Entry{Key: db.Key, Source: db.Source, Target: db.Target, Strategy: db.Strategy, StartedAt: time.Now().UTC().Truncate(time.Millisecond)})
				e, _ = st.Database(db.Key)
			}
			e.Status, e.Error, e.Reset = volume.Cloning, "", true
		}
		if err := volume.Save(rec.ID, st); err != nil {
			cloneLock.Close()
			return err
		}
	}
	dbGone, err := dbs.execute(ctx.Stderr, &rec)
	if err == nil {
		var keysGone []discarded
		keysGone, err = rs.execute()
		dbGone = append(dbGone, keysGone...)
	}
	if saveErr := saveRecord(rec); err == nil {
		err = saveErr
	}
	if err != nil {
		if cloneLock != nil {
			cloneLock.Close()
		}
		return err
	}
	sqliteGone, err := resetSQLite(ctx.Stderr, w, cfg, &rec)
	dbGone = append(dbGone, sqliteGone...)
	if saveErr := saveRecord(rec); err == nil {
		err = saveErr
	}
	if err != nil {
		if cloneLock != nil {
			cloneLock.Close()
		}
		return err
	}
	if cloneLock != nil {
		started, err := cloneUnder(cloneLock, st, ctx.Stderr, plan, cfg, &rec, keys, dbs.hostConn(), true)
		if saveErr := saveRecord(rec); err == nil {
			err = saveErr
		}
		if err != nil {
			return err
		}
		gone = slices.DeleteFunc(gone, func(d discarded) bool { return d.Kind == kindVolume && !slices.Contains(started, d.Name) })
	}
	return printReset(ctx, rec, append(gone, dbGone...), false)
}

// planVolumeReset makes reset's checks on the berth's clone volumes and lists those it
// would empty, changing nothing.
func planVolumeReset(w *worktree.Worktree, cfg *config.Config, rec registry.Record, keys []string) (*composePlan, []discarded, error) {
	plan, err := planCompose(w, cfg)
	if err != nil {
		return nil, nil, err
	}
	if plan.main == nil || plan.own == nil {
		return nil, nil, apperr.New(apperr.Generic, "[compose.volumes] lists clone volumes, but main has no compose project to clone them from, so berth reset has nothing to clone")
	}
	existing, err := docker.Volumes()
	if err != nil {
		return nil, nil, err
	}
	var gone []discarded
	for _, key := range keys {
		v := cloneTarget(rec, key)
		if !existing[v] || !slices.Contains(rec.Volumes, v) {
			continue
		}
		if project, _, err := docker.VolumeProject(v); err != nil {
			return nil, nil, err
		} else if project != rec.ComposeProject {
			return nil, nil, apperr.New(apperr.Generic, "volume %s was created by berth for berth %s, but it is now labelled as compose project %q's, so reset discarded nothing", v, rec.ID, project)
		}
		if _, external, declared := plan.main.Volume(key); declared && !external && refusal(plan, key) != "" {
			continue
		}
		gone = append(gone, discarded{kindVolume, key, v, v})
	}
	return plan, gone, nil
}

// refuseRunning refuses a reset while a container of the berth's compose project runs,
// whatever is being reset: an app in one may hold a database or a SQLite file that is
// about to be dropped. It refuses too when Docker can't be asked: a missing CLI, another
// DOCKER_HOST or a timeout fails the same way a stopped daemon does.
func refuseRunning(rec registry.Record) error {
	if err := docker.Reachable(); err != nil {
		return apperr.New(apperr.Generic, "Docker is not reachable, so berth can't tell whether berth %s's containers are running, and reset discarded nothing; start Docker and run this again (%v)", rec.ID, err)
	}
	running, err := docker.ProjectRunning(rec.ComposeProject)
	if err != nil {
		return err
	}
	if len(running) > 0 {
		return apperr.New(apperr.Generic, "berth %s's containers %s are running, so reset discarded nothing; stop them with docker compose stop and run berth reset again", rec.ID, strings.Join(running, ", "))
	}
	return nil
}

func findRecord(w *worktree.Worktree) (registry.Record, error) {
	reg, err := registry.Open()
	if err != nil {
		return registry.Record{}, err
	}
	defer reg.Close()
	rec, ok, err := reg.Find(w)
	if err != nil {
		return registry.Record{}, err
	}
	if !ok {
		return registry.Record{}, apperr.New(apperr.NoBerth, "worktree %s has no berth to reset; run berth add", w.Path).
			WithNext("run berth add before starting the app or its tests here")
	}
	noteUse(rec.ID)
	return rec, nil
}

func printReset(ctx *Context, rec registry.Record, gone []discarded, dryRun bool) error {
	if ctx.JSON {
		if gone == nil {
			gone = []discarded{}
		}
		return writeJSON(ctx.Stdout, resetJSON{rec.ID, rec.Worktree, dryRun, gone})
	}
	var b strings.Builder
	verb := "Reset"
	if dryRun {
		verb = "Would reset"
	}
	fmt.Fprintf(&b, "%s berth %s (%s)", verb, rec.ID, rec.Worktree)
	if len(gone) == 0 {
		b.WriteString(", which has no cloned volume, database or SQLite file to discard\n")
	} else {
		b.WriteString(", discarding these and making them again:\n")
		for _, d := range gone {
			fmt.Fprintf(&b, "  %s %s\n", d.Kind, d.Target)
		}
	}
	_, err := io.WriteString(ctx.Stdout, b.String())
	return err
}
