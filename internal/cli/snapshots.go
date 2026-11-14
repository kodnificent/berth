package cli

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/bytesize"
	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/mysql"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/snapshot"
	"github.com/kodnificent/berth/internal/volume"
	"github.com/kodnificent/berth/internal/worktree"
)

func init() {
	commands = append(commands, Command{
		Name:    "snapshot",
		Summary: "take a snapshot of main's databases configured to clone from one",
		Usage:   "Usage: berth snapshot [-n|--dry-run]\n\nRun in the main worktree. Takes a consistent copy of every database configured as snapshot\nand keeps it until the next berth snapshot; add and reset then clone from it.\n\nFlags:\n  -n, --dry-run  list the databases that would be snapshotted without taking them\n",
		Run:     runSnapshot,
	})
}

type snapshotDatabaseJSON struct {
	Key           string   `json:"key"`
	Source        string   `json:"source"`
	Bytes         int64    `json:"bytes"`
	FileBytes     int64    `json:"file_bytes,omitempty"`
	Seconds       *float64 `json:"seconds,omitempty"`
	Paused        []string `json:"paused,omitempty"`
	PauseSeconds  *float64 `json:"pause_seconds,omitempty"`
	SourceMissing bool     `json:"source_missing,omitempty"`
	Notes         []string `json:"notes,omitempty"`
}

type snapshotJSON struct {
	DryRun    bool                   `json:"dry_run"`
	Databases []snapshotDatabaseJSON `json:"databases"`
}

type snapshotItem struct {
	db      registry.Database
	tables  []mysql.Table
	held    []string
	bytes   int64
	missing bool
}

// snapshotPlan lists main's databases configured as snapshot, measuring each and naming
// the tables whose writes taking it would hold. Listing sources takes every berth's
// record, so a pattern never matches a berth's databases on the same server.
func snapshotPlan(d *dbSetup) ([]snapshotItem, error) {
	if d == nil {
		return nil, nil
	}
	reg, err := registry.Open()
	if err != nil {
		return nil, err
	}
	live, err := reg.All()
	reg.Close()
	if err != nil {
		return nil, err
	}
	others := make([]registry.Record, 0, len(live))
	for _, l := range live {
		others = append(others, l.Record)
	}
	all, err := d.conn.Databases()
	if err != nil {
		return nil, apperr.New(apperr.Generic, "%v", err)
	}
	var items []snapshotItem
	for _, db := range d.sources(all, others, nil) {
		if db.Strategy != config.Snapshot {
			continue
		}
		item := snapshotItem{db: db}
		tables, exists, err := d.connection().Tables(db.Source)
		if err != nil {
			return nil, apperr.New(apperr.Generic, "%v", err)
		}
		item.missing = !exists
		item.tables, item.held = tables, mysql.Held(tables)
		for _, t := range tables {
			item.bytes += t.Bytes
		}
		items = append(items, item)
	}
	slices.SortFunc(items, func(a, b snapshotItem) int { return strings.Compare(a.db.Key, b.db.Key) })
	return items, nil
}

func runSnapshot(ctx *Context, args []string) error {
	dryRun, args := dryRunFlag(args)
	if len(args) > 0 {
		return apperr.New(apperr.Usage, "berth snapshot takes no arguments; run berth snapshot --help for usage")
	}
	w, err := worktree.Resolve("")
	if err != nil {
		return err
	}
	if !w.IsMain() {
		return apperr.New(apperr.Usage, "%s is a linked worktree, and berth snapshot copies main's databases; run it in the main worktree, %s", w.Path, w.Repository)
	}
	cfg, err := config.Load(w)
	if err != nil {
		return err
	}
	d, err := prepareDatabases(w, cfg)
	if err != nil {
		return err
	}
	defer d.close()
	var lock *os.File
	if !dryRun {
		if lock, err = registry.LockWorktree(w); err != nil {
			return err
		}
		defer lock.Close()
	}
	items, err := snapshotPlan(d)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return printSnapshot(ctx, snapshotJSON{DryRun: dryRun}, "No database is configured as snapshot in [state.mysql], so there is nothing to take\n")
	}
	previous, _, _ := snapshot.Load(w.Repository)
	for _, it := range items {
		if it.missing {
			if err := refuseLostDatabase(previous, it.db); err != nil {
				return err
			}
		}
	}
	if dryRun {
		return printDrySnapshot(ctx, items)
	}
	announceSnapshotPauses(ctx.Stderr, cfg, items)
	delay, err := testCloneDelay()
	if err != nil {
		return err
	}
	return takeSnapshot(ctx, w, d, items, previous, delay)
}

// refuseLostDatabase keeps a database main no longer has from replacing data the current
// snapshot holds with an empty copy; that is usually an accident on main, and the
// snapshot is what berths would be rebuilt from.
func refuseLostDatabase(previous snapshot.Manifest, db registry.Database) error {
	if held, ok := previous.Database(db.Key); !ok || held.SourceMissing {
		return nil
	}
	return apperr.New(apperr.StateCloneFailed, "main no longer has database %s, which the current snapshot holds data for, so the previous snapshot is kept; create it again, or remove %s from [state.mysql.databases] if it is meant to go", db.Source, db.Key)
}

func testCloneDelay() (time.Duration, error) {
	d := os.Getenv(testDelay)
	if d == "" {
		return 0, nil
	}
	delay, err := time.ParseDuration(d)
	if err != nil {
		return 0, apperr.New(apperr.Usage, "%s=%q is not a duration", testDelay, d)
	}
	return delay, nil
}

// announceSnapshotPauses says which of main's tables will have their writes held while
// the snapshot reads them, before any database is read, and warns about disk.
func announceSnapshotPauses(stderr io.Writer, cfg *config.Config, items []snapshotItem) {
	var total int64
	for _, it := range items {
		total += it.bytes
		if len(it.held) > 0 {
			fmt.Fprintf(stderr, "berth: writes to main's %s in database %s will pause while the snapshot reads those tables, since only InnoDB tables can be read consistently while they take writes, and resume as soon as they are read; main's InnoDB tables keep taking writes throughout\n",
				describeHeld(it.tables, it.held), it.db.Source)
		}
	}
	state, err := registry.StateDir()
	if err != nil {
		return
	}
	if err := os.MkdirAll(state, 0o755); err != nil {
		return
	}
	free, ok := freeSpace(state)
	if !ok {
		return
	}
	if min := cfg.MinFree(); free-total < min {
		fmt.Fprintf(stderr, "berth: this snapshot's %s of data would leave %s of free disk in %s, less than the %s that [clone] min_free asks for\n",
			bytesize.Format(total), bytesize.Format(max(free-total, 0)), state, bytesize.Format(min))
	}
}

func takeSnapshot(ctx *Context, w *worktree.Worktree, d *dbSetup, items []snapshotItem, previous snapshot.Manifest, delay time.Duration) error {
	gen, err := snapshot.Begin(w.Repository)
	if err != nil {
		return err
	}
	defer gen.Abort()
	login, _ := d.location().Login(d.mainEnv)
	manifest := snapshot.Manifest{Host: d.server.Host, Port: d.server.Port}
	var result []snapshotDatabaseJSON
	var human strings.Builder
	for _, it := range items {
		taken, entry, err := takeOne(gen, d.server, login, it, delay)
		if err != nil {
			return apperr.New(apperr.StateCloneFailed, "the snapshot of database %s failed, so the previous snapshot is kept: %v", it.db.Source, err)
		}
		if taken.SourceMissing {
			if err := refuseLostDatabase(previous, it.db); err != nil {
				return err
			}
		}
		manifest.Databases = append(manifest.Databases, taken)
		result = append(result, entry)
		describeSnapshotDatabase(&human, entry)
	}
	if err := gen.Commit(manifest); err != nil {
		return apperr.New(apperr.Generic, "the snapshot was not saved, so the previous snapshot is kept: %v", err)
	}
	if err := gen.RemoveReplaced(); err != nil {
		fmt.Fprintf(ctx.Stderr, "berth: the snapshot is saved and current, but the one it replaced couldn't be removed; the next berth snapshot tries again: %v\n", err)
	}
	return printSnapshot(ctx, snapshotJSON{Databases: result}, human.String())
}

func takeOne(gen *snapshot.Generation, server mysql.Server, login mysql.Login, it snapshotItem, delay time.Duration) (snapshot.Database, snapshotDatabaseJSON, error) {
	file, name, err := gen.Create(it.db.Key)
	if err != nil {
		return snapshot.Database{}, snapshotDatabaseJSON{}, err
	}
	started := time.Now().UTC().Truncate(time.Millisecond)
	out, err := mysql.Snapshot(server, login, it.db.Source, file, mysql.CopyOptions{
		Holding: func(tables []string) error {
			return refuseUnannounced(it.db.Source, it.held, tables, "berth snapshot")
		},
	})
	size, closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return snapshot.Database{}, snapshotDatabaseJSON{}, err
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	seconds := time.Since(started).Seconds()
	entry := snapshotDatabaseJSON{Key: it.db.Key, Source: it.db.Source, Bytes: out.Bytes, FileBytes: size, Seconds: &seconds,
		Paused: out.Held, SourceMissing: out.SourceMissing, Notes: out.Notes}
	if len(out.Held) > 0 && out.HeldFor > 0 {
		s := out.HeldFor.Seconds()
		entry.PauseSeconds = &s
	}
	return snapshot.Database{Key: it.db.Key, Source: it.db.Source, File: name, Charset: out.Charset, Collation: out.Collation,
		TakenAt: started, Bytes: out.Bytes, FileBytes: size, SourceMissing: out.SourceMissing, Notes: out.Notes}, entry, nil
}

func describeSnapshotDatabase(b *strings.Builder, e snapshotDatabaseJSON) {
	if e.SourceMissing {
		fmt.Fprintf(b, "Main has no database %s yet; the snapshot holds it empty\n", e.Source)
		return
	}
	fmt.Fprintf(b, "Took a snapshot of database %s (%s of data, %s on disk)", e.Source, bytesize.Format(e.Bytes), bytesize.Format(e.FileBytes))
	if e.PauseSeconds != nil {
		fmt.Fprintf(b, "; writes to main's %s paused %.2fs", strings.Join(e.Paused, ", "), *e.PauseSeconds)
	}
	b.WriteString("\n")
	for _, n := range e.Notes {
		fmt.Fprintf(b, "  note: %s\n", n)
	}
}

func printDrySnapshot(ctx *Context, items []snapshotItem) error {
	var human strings.Builder
	list := make([]snapshotDatabaseJSON, 0, len(items))
	for _, it := range items {
		list = append(list, snapshotDatabaseJSON{Key: it.db.Key, Source: it.db.Source, Bytes: it.bytes, Paused: it.held, SourceMissing: it.missing})
		if it.missing {
			fmt.Fprintf(&human, "Would take database %s, which main doesn't have yet, so it would be empty\n", it.db.Source)
			continue
		}
		fmt.Fprintf(&human, "Would take database %s (%s)\n", it.db.Source, bytesize.Format(it.bytes))
		if len(it.held) > 0 {
			fmt.Fprintf(&human, "  writes to main's %s would pause while the snapshot reads those tables\n", describeHeld(it.tables, it.held))
		}
	}
	return printSnapshot(ctx, snapshotJSON{DryRun: true, Databases: list}, human.String())
}

func printSnapshot(ctx *Context, out snapshotJSON, human string) error {
	if ctx.JSON {
		if out.Databases == nil {
			out.Databases = []snapshotDatabaseJSON{}
		}
		return writeJSON(ctx.Stdout, out)
	}
	_, err := io.WriteString(ctx.Stdout, human)
	return err
}

func snapshotEntry(repository, key string) (snapshot.Database, bool) {
	m, ok, err := snapshot.Load(repository)
	if err != nil || !ok {
		return snapshot.Database{}, false
	}
	return m.Database(key)
}

// snapshotRefusal says why database key can't be restored from set, "" when it can. set
// is nil when the repository has no snapshot.
func snapshotRefusal(set *snapshot.Set, key, source string, server mysql.Server) string {
	if set == nil {
		return fmt.Sprintf("database %s has no snapshot yet", source)
	}
	db, ok := set.Database(key)
	if !ok {
		return fmt.Sprintf("database %s has no snapshot yet", source)
	}
	snapped := mysql.Server{Host: set.Host, Port: set.Port}
	if db.Source != source || !snapped.Same(server) {
		return fmt.Sprintf("the snapshot of %s is of database %s on the MySQL server at %s, not %s on %s", key, db.Source, snapped, source, server)
	}
	return ""
}

// refuseUnrestorable refuses a reset that couldn't make one of the berth's databases
// again, before reset drops anything: a snapshot database needs its snapshot, a seed
// database its file.
func refuseUnrestorable(rec registry.Record, server mysql.Server) error {
	var keys []string
	for _, db := range databases(rec) {
		switch db.Strategy {
		case config.Snapshot:
			keys = append(keys, db.Key)
		case config.File:
			f, err := os.Open(seedPath(rec, db.File))
			if err != nil {
				return apperr.New(apperr.Generic, "seed file %s can't be read in the main worktree, so reset discarded nothing; restore it, or point database %s at another file, and run berth reset again", db.File, db.Key)
			}
			f.Close()
		}
	}
	if len(keys) == 0 {
		return nil
	}
	set, _, err := snapshot.Open(rec.Repository, keys)
	if err != nil {
		return apperr.New(apperr.Generic, "%v, so reset discarded nothing; run berth snapshot in the main worktree, then berth reset", err)
	}
	defer set.Close()
	for _, db := range databases(rec) {
		if db.Strategy != config.Snapshot {
			continue
		}
		if why := snapshotRefusal(set, db.Key, db.Source, server); why != "" {
			return apperr.New(apperr.Generic, "%s, so reset discarded nothing; run berth snapshot in the main worktree, then berth reset", why)
		}
	}
	return nil
}

// openRunSnapshots opens the snapshot once for a clone run, so every database the run
// restores comes from the same generation.
func openRunSnapshots(rec registry.Record, st volume.State) (*snapshot.Set, error) {
	var keys []string
	for _, e := range st.Databases {
		if e.Status == volume.Cloning && e.Strategy == config.Snapshot {
			keys = append(keys, e.Key)
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	set, _, err := snapshot.Open(rec.Repository, keys)
	return set, err
}

// restoreSnapshot replaces e's target with its data in set, the snapshot this clone run
// opened.
func restoreSnapshot(rec registry.Record, set *snapshot.Set, setErr error, server mysql.Server, login mysql.Login, e *volume.Entry, delay time.Duration, save func()) error {
	again := "berth add"
	if e.Reset {
		again = "berth reset"
	}
	if setErr != nil {
		return fmt.Errorf("%v; run berth snapshot in the main worktree, then %s", setErr, again)
	}
	if why := snapshotRefusal(set, e.Key, e.Source, server); why != "" {
		return fmt.Errorf("%s; run berth snapshot in the main worktree, then %s", why, again)
	}
	reader, err := set.Reader(e.Key)
	if err != nil {
		return fmt.Errorf("%v; run berth snapshot in the main worktree, then %s", err, again)
	}
	db, _ := set.Database(e.Key)
	taken := db.TakenAt
	e.SnapshotTakenAt, e.Total, e.Copied, e.Notes, e.SourceMissing = &taken, db.FileBytes, 0, nil, db.SourceMissing
	save()
	var lastSave time.Time
	out, err := mysql.Restore(server, login, e.Target, reader, db.Charset, db.Collation, mysql.RestoreOptions{
		Progress: func() {
			e.Copied = reader.Copied()
			if time.Since(lastSave) >= progressEvery {
				save()
				lastSave = time.Now()
			}
		},
	})
	e.Notes = append(slices.Clone(db.Notes), out.Notes...)
	if err != nil {
		return err
	}
	if delay > 0 {
		save()
		time.Sleep(delay)
	}
	e.Bytes, e.Copied = out.Bytes, 0
	return nil
}
