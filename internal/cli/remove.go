package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/cookie"
	"github.com/kodnificent/berth/internal/docker"
	"github.com/kodnificent/berth/internal/envfile"
	"github.com/kodnificent/berth/internal/hook"
	"github.com/kodnificent/berth/internal/hostdb"
	"github.com/kodnificent/berth/internal/lastuse"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/volume"
	"github.com/kodnificent/berth/internal/worktree"
)

func init() {
	commands = append(commands,
		Command{Name: "remove", Summary: "delete a worktree's berth and everything berth created for it", Usage: "Usage: berth remove [-n|--dry-run] [path]\n\nFlags:\n  -n, --dry-run  list what would be deleted without deleting it\n", Run: runRemove},
		Command{Name: "prune", Summary: "delete the berths of worktrees git no longer has, and with --idle those unused for a while", Usage: "Usage: berth prune [--idle <duration>] [-n|--dry-run]\n\nFlags:\n  --idle <duration>  also delete the berths of worktrees unused for that long, such as 7d, 12h or 30m\n  -n, --dry-run      list what would be deleted without deleting it\n", Run: runPrune},
	)
}

// Kinds of thing a teardown deletes, in the order it deletes them: containers first, so
// nothing holds the networks, volumes and images after them, and databases before
// files, since the login for their server is read from the berth's .env.
const (
	kindContainer = "container"
	kindNetwork   = "network"
	kindVolume    = "volume"
	kindImage     = "image"
	kindFile      = "file"
	kindEnvBlock  = "env_block"
)

type item struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// teardown is everything berth deletes for one berth, worked out before any of it is.
type teardown struct {
	rec registry.Record
	// dir is the worktree whose recorded files are berth's to delete, empty when none are.
	dir     string
	tracked map[string]bool
	items   []item
	db      *dbTeardown
	dbConn  hostdb.Conn
	redis   *redisLogin
}

type removeJSON struct {
	ID       string     `json:"id"`
	Worktree string     `json:"worktree"`
	DryRun   bool       `json:"dry_run"`
	Removed  []item     `json:"removed"`
	Error    *errorJSON `json:"error,omitempty"`
}

func dryRunFlag(args []string) (bool, []string) {
	dry := slices.ContainsFunc(args, func(a string) bool { return a == "-n" || a == "--dry-run" })
	return dry, slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "-n" || a == "--dry-run" })
}

// planTeardown lists what berth recorded as rec's own and still exists. A compose
// resource is the berth's when compose labels it with the berth's project; a volume only
// when the record lists it too, since volumes hold data.
func planTeardown(rec registry.Record, dir string, stderr io.Writer) (*teardown, error) {
	td := &teardown{rec: rec, dir: dir, tracked: map[string]bool{}, items: []item{}}
	if rec.ComposeFile != "" || len(rec.Volumes) > 0 || len(rec.Images) > 0 {
		if err := docker.Reachable(); err != nil {
			return nil, apperr.New(apperr.Generic, "Docker is not reachable, so berth can't delete berth %s's containers, volumes and images; start Docker and run this again (%v)", rec.ID, err)
		}
		if err := td.planDocker(stderr); err != nil {
			return nil, err
		}
	}
	if err := td.planDatabases(); err != nil {
		return nil, err
	}
	if err := td.planRedis(); err != nil {
		return nil, err
	}
	if dir == "" {
		return td, nil
	}
	td.tracked = trackedFiles(dir)
	for _, e := range rec.SQLite {
		if e.Own && !td.tracked[filepath.ToSlash(e.Path)] && sqliteExists(filepath.Join(dir, e.Path)) {
			td.items = append(td.items, item{kindSQLite, e.Path})
		}
	}
	for _, f := range rec.Files {
		if td.tracked[filepath.ToSlash(f)] {
			continue
		}
		if _, err := os.Lstat(filepath.Join(dir, f)); err != nil {
			continue
		}
		if err := realDirs(dir, filepath.Dir(f)); err != nil {
			fmt.Fprintf(stderr, "berth: leaving %s: %v; delete it yourself once that is out of the way\n", filepath.ToSlash(f), err)
			continue
		}
		td.items = append(td.items, item{kindFile, f})
	}
	if !slices.Contains(rec.Files, ".env") {
		if data, err := os.ReadFile(filepath.Join(dir, ".env")); err == nil && !bytes.Equal(envfile.Strip(data, rec.EnvSeparator), data) {
			td.items = append(td.items, item{kindEnvBlock, ".env"})
		}
	}
	return td, nil
}

func (td *teardown) planDocker(stderr io.Writer) error {
	rec := td.rec
	if rec.ComposeFile != "" {
		containers, err := docker.ProjectContainers(rec.ComposeProject)
		if err != nil {
			return err
		}
		networks, err := docker.ProjectNetworks(rec.ComposeProject)
		if err != nil {
			return err
		}
		for _, c := range containers {
			td.items = append(td.items, item{kindContainer, c})
		}
		for _, n := range networks {
			td.items = append(td.items, item{kindNetwork, n})
		}
	}
	for _, v := range rec.Volumes {
		project, exists, err := docker.VolumeProject(v)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if project != rec.ComposeProject {
			fmt.Fprintf(stderr, "berth: leaving volume %s: berth created it for this berth, but it is now labelled as compose project %q's\n", v, project)
			continue
		}
		td.items = append(td.items, item{kindVolume, v})
	}
	var images []string
	if rec.ComposeFile != "" {
		built, err := docker.ProjectImages(rec.ComposeProject)
		if err != nil {
			return err
		}
		images = append(images, built...)
	}
	for _, ref := range rec.Images {
		if have, err := docker.ImageExists(ref); err != nil {
			return err
		} else if have {
			images = append(images, ref)
		}
	}
	for _, ref := range union(nil, images) {
		td.items = append(td.items, item{kindImage, ref})
	}
	return nil
}

// execute deletes every item, then berth's own state for the berth and its record. When
// something can't be deleted, the record keeps what is left, so running again finishes.
func (td *teardown) execute(reg *registry.Registry) ([]item, error) {
	rec := td.rec
	done := []item{}
	var failed []string
	defer func() {
		if td.dbConn != nil {
			td.dbConn.Close()
		}
	}()
	td.endCopies()
	for _, it := range td.items {
		var err error
		switch it.Kind {
		case kindContainer:
			err = docker.RemoveContainer(it.Name)
		case kindNetwork:
			err = docker.RemoveNetwork(it.Name)
		case kindVolume:
			err = docker.RemoveVolume(it.Name)
		case kindImage:
			err = docker.RemoveImage(it.Name)
		case kindDatabase:
			err = td.dropDatabase(it.Name)
		case kindRedis:
			err = td.deleteRedisKeys(it.Name)
		case kindSQLite:
			err = removeSQLite(td.dir, it.Name, td.tracked)
		case kindFile:
			// The login a retry drops the rest with is read from the berth's .env.
			if it.Name == ".env" && slices.ContainsFunc(failed, func(f string) bool {
				return strings.HasPrefix(f, kindDatabase+" ") || strings.HasPrefix(f, kindRedis+" ")
			}) {
				continue
			}
			err = removeUntracked(td.dir, it.Name, td.tracked)
		case kindEnvBlock:
			err = stripEnv(filepath.Join(td.dir, ".env"), rec.EnvSeparator)
		}
		// A failed key deletion is never "already gone": a server that can't be found, such
		// as "no such host", would otherwise read as done and drop the record.
		if err != nil && (it.Kind == kindRedis || !gone(err)) {
			failed = append(failed, fmt.Sprintf("%s %s: %v", it.Kind, it.Name, err))
			continue
		}
		done = append(done, it)
		without := func(list []string) []string {
			return slices.DeleteFunc(slices.Clone(list), func(s string) bool { return s == it.Name })
		}
		switch it.Kind {
		case kindVolume:
			rec.Volumes = without(rec.Volumes)
		case kindImage:
			rec.Images = without(rec.Images)
		case kindFile:
			rec.Files = without(rec.Files)
		case kindDatabase:
			rec.DB().Databases = slices.DeleteFunc(slices.Clone(rec.DB().Databases), func(db registry.Database) bool { return db.Target == it.Name })
		case kindSQLite:
			rec.SQLite = slices.DeleteFunc(slices.Clone(rec.SQLite), func(e registry.SQLiteFile) bool { return e.Path == it.Name })
		}
	}
	if td.dir != "" {
		removeSQLiteDirs(td.dir, td.rec.SQLite)
	}
	if len(failed) > 0 {
		if err := reg.Update(rec); err != nil {
			failed = append(failed, err.Error())
		}
		return done, apperr.New(apperr.Generic, "could not remove everything berth created for berth %s, so it keeps the berth's record; fix what failed and run this again: %s", rec.ID, strings.Join(failed, "; "))
	}
	for _, forget := range []func(string) error{volume.Forget, hook.Forget, lastuse.Forget, cookie.Forget} {
		if err := forget(rec.ID); err != nil {
			return done, err
		}
	}
	return done, reg.Delete(rec)
}

func gone(err error) bool {
	msg := strings.ToLower(err.Error())
	return errors.Is(err, fs.ErrNotExist) || strings.Contains(msg, "no such") || strings.Contains(msg, "not found")
}

// removeUntracked deletes rel under root, keeping any file git tracks inside it, so a
// path berth cloned into is never taken from the worktree with a tracked file in it.
func removeUntracked(root, rel string, tracked map[string]bool) error {
	slash := filepath.ToSlash(rel)
	if tracked[slash] {
		return nil
	}
	var holds bool
	for f := range tracked {
		if strings.HasPrefix(f, slash+"/") {
			holds = true
			break
		}
	}
	path := filepath.Join(root, rel)
	if !holds {
		return os.RemoveAll(path)
	}
	if info, err := os.Lstat(path); err != nil {
		return err
	} else if info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		// A link or junction here may lead into main, so it goes on its own, never followed.
		return os.Remove(path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := removeUntracked(root, filepath.Join(rel, e.Name()), tracked); err != nil {
			return err
		}
	}
	return nil
}

// stripEnv takes the managed block out of a .env berth didn't create, leaving it as it
// was before add.
func stripEnv(path string, sep bool) error {
	content, mode, err := readEnv(path)
	if err != nil {
		return err
	}
	if out := envfile.Strip(content, sep); !bytes.Equal(out, content) {
		return writeFileAtomic(path, out, mode)
	}
	return nil
}

// quiesce gets berth id ready to tear down and holds it so until release: it refuses
// while post-add runs, stops a clone in progress, and resumes whatever of main's the
// clone paused.
func quiesce(id string) (release func(), err error) {
	var locks []*os.File
	closeAll := func() {
		for _, l := range locks {
			l.Close()
		}
	}
	defer func() {
		if err != nil {
			closeAll()
		}
	}()

	postLock, postFree, err := hook.TryLock(id)
	if err != nil {
		return nil, err
	}
	if postFree {
		locks = append(locks, postLock)
	} else if err := refusePostAdd(id); err != nil {
		return nil, err
	}

	cloneLock, free, err := volume.TryLock(id)
	if err != nil {
		return nil, err
	}
	if !free {
		if pid := volume.ReadPID(id); pid > 0 {
			_ = killClone(id, pid)
		}
		if cloneLock, err = waitLock(volume.TryLock, id, "the clone process"); err != nil {
			return nil, err
		}
	}
	locks = append(locks, cloneLock)
	st, err := volume.Load(id)
	if err != nil {
		return nil, err
	}
	if _, err := volume.Recover(id, &st); err != nil {
		return nil, err
	}

	if !postFree {
		// post-add was waiting for the clone just stopped, and now records that it was
		// skipped and exits.
		if postLock, err = waitLock(hook.TryLock, id, "post-add"); err != nil {
			return nil, err
		}
		locks = append(locks, postLock)
	}
	return closeAll, nil
}

// refusePostAdd refuses to tear down berth id while its post-add command runs: stopping
// it halfway could leave the berth's services half set up as they are deleted.
func refusePostAdd(id string) error {
	lock, free, err := hook.TryLock(id)
	if err != nil {
		return err
	}
	if free {
		return lock.Close()
	}
	if p, ok, _ := hook.Load(id); ok && p.Status == hook.Running {
		return apperr.New(apperr.Generic, "post-add is still running for berth %s; wait for it to finish (berth status shows it) and run this again", id)
	}
	return nil
}

func waitLock(try func(string) (*os.File, bool, error), id, what string) (*os.File, error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		f, ok, err := try(id)
		if err != nil || ok {
			return f, err
		}
		if time.Now().After(deadline) {
			return nil, apperr.New(apperr.Generic, "%s of berth %s did not stop within 30s; run this again", what, id)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func runRemove(ctx *Context, args []string) error {
	dryRun, args := dryRunFlag(args)
	w, err := resolveBerthWorktree("remove", args)
	if err != nil {
		return err
	}
	cfg, err := config.Load(w)
	if err != nil {
		return err
	}
	// A real remove plans again under its locks and reports what it leaves then.
	planOut := ctx.Stderr
	if !dryRun {
		planOut = io.Discard
	}
	rec, td, err := planRemove(w, planOut)
	if err != nil || dryRun {
		if err == nil {
			err = printRemoved(ctx, rec, td.items, true, nil)
		}
		return err
	}
	rec, done, err := removeSequence(ctx, w, rec.ID, cfg.Hooks.PreRemove, "berth remove", nil)
	if errors.Is(err, errGone) {
		return apperr.New(apperr.NoBerth, "worktree %s has no berth to remove", w.Path)
	}
	if done != nil {
		if printErr := printRemoved(ctx, rec, done, false, err); printErr != nil {
			return printErr
		}
		if err != nil && ctx.JSON {
			return &apperr.Reported{Code: apperr.CodeOf(err)}
		}
	}
	return err
}

// errGone is a berth removed by another command while this one waited to remove it.
var errGone = errors.New("the berth is gone")

// removeSequence removes w's berth, id when it was planned, as remove does: it refuses
// while post-add runs, runs preRemove, then under the worktree lock and the registry lock, held to the end, quiesces
// the berth and tears it down. keep, when set, is asked under both locks whether the
// berth must stay after all; its error keeps the berth. done is nil when nothing was
// deleted.
func removeSequence(ctx *Context, w *worktree.Worktree, id, preRemove, again string, keep func(registry.Record) error) (rec registry.Record, done []item, err error) {
	// Run with no lock held, so the command may run berth itself.
	if preRemove != "" {
		if err := refusePostAdd(id); err != nil {
			return rec, nil, err
		}
		if err := hook.PreRemove(preRemove, w.Path, w.Repository, hook.ChangedFiles(w), ctx.Stderr); err != nil {
			return rec, nil, apperr.New(apperr.Generic, "%v, so nothing was removed; fix [hooks] pre-remove or take it out, and run %s again", err, again)
		}
	}

	// Before the registry lock, as add takes them, so an add still filling in the berth
	// finishes first.
	lock, err := registry.LockWorktree(w)
	if err != nil {
		return rec, nil, err
	}
	defer lock.Close()
	reg, err := registry.Open()
	if err != nil {
		return rec, nil, err
	}
	defer reg.Close()
	rec, ok, err := reg.Find(w)
	if err != nil {
		return rec, nil, err
	}
	if !ok {
		return rec, nil, errGone
	}
	if keep != nil {
		if err := keep(rec); err != nil {
			return rec, nil, err
		}
	}
	release, err := quiesce(rec.ID)
	if err != nil {
		return rec, nil, err
	}
	defer release()
	// Planned again, since pre-remove may have stopped or started containers of its own.
	td, err := planTeardown(rec, w.Path, ctx.Stderr)
	if err != nil {
		return rec, nil, err
	}
	done, err = td.execute(reg)
	return rec, done, err
}

func planRemove(w *worktree.Worktree, stderr io.Writer) (registry.Record, *teardown, error) {
	reg, err := registry.Open()
	if err != nil {
		return registry.Record{}, nil, err
	}
	defer reg.Close()
	rec, ok, err := reg.Find(w)
	if err != nil {
		return registry.Record{}, nil, err
	}
	if !ok {
		return registry.Record{}, nil, apperr.New(apperr.NoBerth, "worktree %s has no berth to remove", w.Path)
	}
	td, err := planTeardown(rec, w.Path, stderr)
	return rec, td, err
}

// printRemoved prints what was removed. With --json a failure that left some of the berth
// behind goes in the same object, so stdout holds one object either way.
func printRemoved(ctx *Context, rec registry.Record, items []item, dryRun bool, failure error) error {
	if ctx.JSON {
		return writeJSON(ctx.Stdout, removeJSON{rec.ID, rec.Worktree, dryRun, items, jsonError(failure)})
	}
	_, err := io.WriteString(ctx.Stdout, describeRemoved(rec, items, dryRun))
	return err
}

func describeRemoved(rec registry.Record, items []item, dryRun bool) string {
	return describeItems(fmt.Sprintf("%s berth %s (%s)", removedVerb(dryRun), rec.ID, rec.Worktree), items)
}

func describeIdle(rec registry.Record, items []item, lastUsed time.Time, dryRun bool) string {
	return describeItems(fmt.Sprintf("%s idle berth %s (%s), last used %s", removedVerb(dryRun), rec.ID, rec.Worktree, ago(time.Since(lastUsed))), items)
}

func removedVerb(dryRun bool) string {
	if dryRun {
		return "Would remove"
	}
	return "Removed"
}

func describeItems(head string, items []item) string {
	var b strings.Builder
	b.WriteString(head)
	if len(items) == 0 {
		b.WriteString(", which had nothing else to delete\n")
		return b.String()
	}
	b.WriteString(":\n")
	for _, it := range items {
		switch it.Kind {
		case kindEnvBlock:
			fmt.Fprintf(&b, "  managed block of %s\n", it.Name)
			continue
		case kindRedis:
			fmt.Fprintf(&b, "  Redis keys under %s\n", it.Name)
			continue
		}
		fmt.Fprintf(&b, "  %s %s\n", it.Kind, it.Name)
	}
	return b.String()
}
