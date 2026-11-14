package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kodnificent/berth/internal/bytesize"
	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/hostdb"
	"github.com/kodnificent/berth/internal/mysql"
	"github.com/kodnificent/berth/internal/postgres"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/snapshot"
	"github.com/kodnificent/berth/internal/volume"
)

// progressEvery is how often the clone process records how far a copy is, for status.
const progressEvery = 2 * time.Second

// databases are the databases rec records, none without [state.mysql].
func databases(rec registry.Record) []registry.Database {
	if rec.DB() == nil {
		return nil
	}
	return rec.DB().Databases
}

// clonedStrategy reports whether the clone process makes a database of this strategy, in
// the background, rather than add or reset.
func clonedStrategy(s string) bool {
	return s == config.Dump || s == config.File || s == config.Command || s == config.Snapshot || s == config.Template
}

// dumpKeys are the keys of rec's databases the clone process makes: dumps, seed files,
// commands, snapshots and template copies.
func dumpKeys(rec registry.Record) []string {
	var out []string
	for _, db := range databases(rec) {
		if clonedStrategy(db.Strategy) {
			out = append(out, db.Key)
		}
	}
	return out
}

// prepareDumps puts in st an entry for each of rec's dump databases that isn't copied
// yet, or whose copy failed, measuring each source through src for the progress figure
// and the disk warning, and announcing any table whose writes the copy will hold, and
// for a template copy, the pause on main's new connections and any sessions it ends. It
// returns the keys the clone process will copy. A database add finds already made under
// another strategy holds the berth's own data, so only reset copies main's over it.
func prepareDumps(st *volume.State, stderr io.Writer, cfg *config.Config, rec *registry.Record, src hostdb.Conn, reset bool) []string {
	if rec.DB() == nil {
		return nil
	}
	var todo []volume.Entry
	for _, db := range rec.DB().Databases {
		if !clonedStrategy(db.Strategy) {
			continue
		}
		old, ok := st.Database(db.Key)
		if ok && old.Status == volume.Ready && old.Target == db.Target {
			continue
		}
		if !ok && !reset && db.Status == volume.Ready {
			fmt.Fprintf(stderr, "berth: database %s is %s now, but the berth already has its own %s, which may hold its own data; berth reset replaces it\n", db.Source, db.Strategy, db.Target)
			continue
		}
		e := volume.Entry{
			Key:       db.Key,
			Source:    db.Source,
			Target:    db.Target,
			Strategy:  db.Strategy,
			Status:    volume.Cloning,
			Reset:     reset,
			StartedAt: time.Now().UTC().Truncate(time.Millisecond),
		}
		if ok {
			// An earlier copy's session the server may still be running goes with the new
			// entry, so the clone process ends it before making the target again.
			e.CopySession = old.CopySession
		}
		todo = append(todo, e)
	}
	if len(todo) == 0 {
		return nil
	}
	my, _ := src.(*mysql.Conn)
	pg, _ := src.(*postgres.Conn)
	var measured []*volume.Entry
	for i := range todo {
		e := &todo[i]
		if e.Strategy == config.Template && pg != nil {
			if size, ok := prepareTemplate(stderr, cfg, pg, e); ok {
				sized := *e
				sized.Total = size
				measured = append(measured, &sized)
			}
			continue
		}
		if my == nil {
			continue
		}
		if e.Strategy == config.Snapshot {
			if db, ok := snapshotEntry(rec.Repository, e.Key); ok && db.Source == e.Source {
				e.Total = db.Bytes
				measured = append(measured, e)
			}
			continue
		}
		if e.Strategy != config.Dump {
			continue
		}
		tables, exists, err := my.Tables(e.Source)
		switch {
		case err != nil:
			fmt.Fprintf(stderr, "berth: could not measure database %s before copying it: %v\n", e.Source, err)
			continue
		case !exists:
			fmt.Fprintf(stderr, "berth: main has no database %s yet, so the berth's %s starts empty\n", e.Source, e.Target)
			continue
		}
		for _, t := range tables {
			e.Total += t.Bytes
		}
		measured = append(measured, e)
		e.Announced = mysql.Held(tables)
		if held := e.Announced; len(held) > 0 {
			fmt.Fprintf(stderr, "berth: writes to main's %s in database %s will pause while the copy reads those tables, since only InnoDB tables can be copied consistently while they take writes, and resume as soon as they are read; main's InnoDB tables keep taking writes throughout\n",
				describeHeld(tables, held), e.Source)
		}
	}
	if len(measured) > 0 {
		warnDatabaseDisk(stderr, cfg, engineFor(*rec).Name, recordedServer(rec.DB()), src, measured)
	}
	var keys []string
	for _, e := range todo {
		st.PutDatabase(e)
		keys = append(keys, e.Key)
	}
	return keys
}

func describeHeld(tables []mysql.Table, held []string) string {
	var out []string
	for _, t := range tables {
		if slices.Contains(held, t.Name) {
			out = append(out, fmt.Sprintf("%s (%s)", t.Name, t.Engine))
		}
	}
	return strings.Join(out, ", ")
}

// warnDatabaseDisk warns about each copy that would leave less free disk than [clone]
// min_free where the server keeps its data, counting the copies before it.
func warnDatabaseDisk(stderr io.Writer, cfg *config.Config, engine string, server hostdb.Server, src hostdb.Conn, copies []*volume.Entry) {
	free, ok := serverFree(server, src)
	if !ok {
		fmt.Fprintf(stderr, "berth: the %s server at %s is on another machine, so berth could not check its free disk before copying\n", engine, server)
		return
	}
	min := cfg.MinFree()
	var total int64
	for _, e := range copies {
		total += e.Total
		if left := free - total; left < min {
			fmt.Fprintf(stderr, "berth: copying database %s into %s (%s) would leave %s of free disk where the %s server keeps its data, less than the %s that [clone] min_free asks for\n",
				e.Source, e.Target, bytesize.Format(e.Total), bytesize.Format(max(left, 0)), engine, bytesize.Format(min))
		}
	}
}

// serverFree is the free space on the disk the server keeps its data on. A server on this
// machine whose data directory berth can see is measured there. One whose directory it
// can't see runs in a container or virtual machine here, whose disk is a file on this
// machine's own, so that is measured instead. ok is false for a server elsewhere.
func serverFree(server hostdb.Server, src hostdb.Conn) (int64, bool) {
	if !server.Loopback() {
		return 0, false
	}
	if d, ok := src.(interface{ DataDir() (string, error) }); ok {
		if dir, err := d.DataDir(); err == nil && filepath.IsAbs(dir) {
			if n, ok := freeSpace(dir); ok {
				return n, true
			}
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return 0, false
	}
	free, ok := freeSpace(home)
	if vm, vmOK := hostFree(); vmOK && (!ok || vm < free) {
		free, ok = vm, true
	}
	return free, ok
}

// recordDumps copies how each dump database's copy stands into rec, for status to fall
// back on should the clone state be lost.
func recordDumps(rec *registry.Record, st volume.State) {
	if rec.DB() == nil {
		return
	}
	for i := range rec.DB().Databases {
		db := &rec.DB().Databases[i]
		if e, ok := st.Database(db.Key); ok && clonedStrategy(db.Strategy) && e.Target == db.Target {
			db.Status, db.Error = e.Status, e.Error
		}
	}
}

// cloneVerb is what the clone process's log says it is doing for a strategy.
func cloneVerb(strategy string) string {
	switch strategy {
	case config.File:
		return "loading a seed file for"
	case config.Command:
		return "running the command for"
	case config.Snapshot:
		return "restoring the snapshot of"
	}
	return "copying"
}

// runDumps makes every database of berth id still waiting for its clone process. The caller
// holds the berth's clone lock. The login is read from main's .env now, never from the
// clone state, so it is never written anywhere.
func runDumps(id string, delay time.Duration, log io.Writer) error {
	st, err := volume.Load(id)
	if err != nil {
		return err
	}
	var pending bool
	for _, e := range st.Databases {
		pending = pending || e.Status == volume.Cloning
	}
	if !pending {
		return nil
	}
	rec, server, login, setupErr := dumpLogin(id)
	snaps, snapsErr := openRunSnapshots(rec, st)
	defer snaps.Close()
	for i := range st.Databases {
		e := &st.Databases[i]
		if e.Status != volume.Cloning {
			continue
		}
		save := func() {
			if err := volume.Save(id, st); err != nil {
				fmt.Fprintf(log, "saving clone state: %v\n", err)
			}
		}
		start := time.Now()
		fmt.Fprintf(log, "%s %s database %s into %s\n", start.Format(time.RFC3339), cloneVerb(e.Strategy), e.Source, e.Target)
		err := setupErr
		if err == nil {
			err = makeDatabase(rec, snaps, snapsErr, server, login, e, delay, save)
		}
		for _, n := range e.Notes {
			fmt.Fprintf(log, "database %s: %s\n", e.Key, n)
		}
		end := time.Now().UTC().Truncate(time.Millisecond)
		e.FinishedAt = &end
		if err != nil {
			e.Status, e.Error = volume.Failed, err.Error()
			fmt.Fprintf(log, "database %s failed: %v\n", e.Key, err)
		} else {
			e.Status = volume.Ready
			fmt.Fprintf(log, "database %s ready in %s\n", e.Key, time.Since(start).Round(time.Millisecond))
		}
		save()
	}
	return nil
}

// dumpLogin reads berth id's record and, from main's .env as it is now, the login for
// the server the record names.
func dumpLogin(id string) (registry.Record, mysql.Server, mysql.Login, error) {
	reg, err := registry.Open()
	if err != nil {
		return registry.Record{}, mysql.Server{}, mysql.Login{}, err
	}
	rec, ok, err := reg.Get(id)
	reg.Close()
	if err != nil {
		return rec, mysql.Server{}, mysql.Login{}, err
	}
	if !ok || rec.DB() == nil {
		return rec, mysql.Server{}, mysql.Login{}, fmt.Errorf("berth %s has no databases recorded, so there is nothing berth may copy into", id)
	}
	m := rec.DB()
	name := engineFor(rec).Name
	loc := locationOf(rec, m)
	recorded := recordedServer(m)
	path := filepath.Join(rec.Repository, ".env")
	env, err := readEnvFile(path)
	if err != nil {
		return rec, recorded, mysql.Login{}, fmt.Errorf("reading main's %s for the login to the %s server at %s: %v", path, name, recorded, err)
	}
	server, missing, err := loc.Server(env)
	switch {
	case len(missing) > 0:
		return rec, recorded, mysql.Login{}, fmt.Errorf("main's .env sets no %s, so berth can't tell it still points at the %s server at %s; set them and run berth add again", strings.Join(missing, ", "), name, recorded)
	case err != nil:
		return rec, recorded, mysql.Login{}, fmt.Errorf("in main's .env, %v", err)
	case !server.Same(recorded):
		return rec, recorded, mysql.Login{}, fmt.Errorf("main's .env now points at the %s server at %s, but the berth's databases are on %s, so nothing was copied", name, server, recorded)
	}
	login, _ := loc.Login(env)
	return rec, recorded, login, nil
}

// makeDatabase makes e's target by its strategy. The record must name it as the berth's
// own, since every strategy drops whatever the target holds first.
func makeDatabase(rec registry.Record, snaps *snapshot.Set, snapsErr error, server mysql.Server, login mysql.Login, e *volume.Entry, delay time.Duration, save func()) error {
	if !rec.DB().Owns(e.Target) {
		return fmt.Errorf("database %s is not recorded as berth %s's own, so berth won't overwrite it", e.Target, rec.ID)
	}
	switch e.Strategy {
	case config.File:
		return seedDatabase(rec, server, login, e, delay, save)
	case config.Command:
		return commandDatabase(rec, server, login, e, delay, save)
	case config.Snapshot:
		return restoreSnapshot(rec, snaps, snapsErr, server, login, e, delay, save)
	case config.Template:
		return templateDatabase(server, login, e, delay, save)
	}
	return copyDatabase(rec, server, login, e, delay, save)
}

// copyDatabase copies e's source into its target.
func copyDatabase(rec registry.Record, server mysql.Server, login mysql.Login, e *volume.Entry, delay time.Duration, save func()) error {
	e.Paused, e.PauseMS, e.Copied, e.SourceMissing = nil, nil, 0, false
	lastSave := time.Now()
	out, err := mysql.Copy(server, login, e.Source, e.Target, mysql.CopyOptions{
		Holding: func(tables []string) error {
			if err := refuseUnannounced(e.Source, e.Announced, tables, "berth reset"); err != nil {
				return err
			}
			e.Paused = tables
			save()
			return nil
		},
		Progress: func(copied int64) {
			e.Copied = copied
			if time.Since(lastSave) >= progressEvery {
				save()
				lastSave = time.Now()
			}
		},
		Done: func() {
			save()
			if delay > 0 {
				time.Sleep(delay)
			}
		},
	})
	e.Notes = out.Notes
	if len(out.Held) > 0 && out.HeldFor > 0 {
		ms := out.HeldFor.Milliseconds()
		e.PauseMS = &ms
	}
	if err != nil {
		return err
	}
	e.SourceMissing, e.Bytes, e.Copied = out.SourceMissing, out.Bytes, 0
	return nil
}

// refuseUnannounced refuses to hold writes to any of main's tables in database source
// that the command didn't name first, since berth says so before it pauses anything of
// main's. retry is the command to run again, which says so first.
func refuseUnannounced(source string, announced, held []string, retry string) error {
	var unannounced []string
	for _, t := range held {
		if !slices.Contains(announced, t) {
			unannounced = append(unannounced, t)
		}
	}
	if len(unannounced) == 0 {
		return nil
	}
	return fmt.Errorf("main's %s in database %s can no longer be copied without holding its writes, which berth didn't say before the copy started, so nothing was held or copied; run %s, which says so first", strings.Join(unannounced, ", "), source, retry)
}

// databaseEntries are rec's databases as status reports them: a copy as its clone entry
// says it stands, with pid the running clone process, and the rest as recorded.
func databaseEntries(rec registry.Record, st volume.State, pid int) []stateJSON {
	var out []stateJSON
	if rec.DB() == nil {
		return out
	}
	for _, db := range rec.DB().Databases {
		if e, ok := st.Database(db.Key); ok && clonedStrategy(db.Strategy) && e.Target == db.Target {
			j := entryJSON(*e, pid)
			j.Kind = kindDatabase
			out = append(out, j)
			continue
		}
		out = append(out, stateJSON{Name: db.Key, Kind: kindDatabase, Source: db.Source, Target: db.Target,
			Strategy: db.Strategy, Status: db.Status, Bytes: db.Bytes, Error: db.Error})
	}
	return out
}

// waitDumps reports how each of rec's database copies went, and lists those that failed.
func waitDumps(w io.Writer, rec registry.Record, st volume.State) []string {
	var failed []string
	for _, key := range dumpKeys(rec) {
		e, ok := st.Database(key)
		if !ok {
			continue
		}
		switch e.Status {
		case volume.Ready:
			if !e.SourceMissing {
				fmt.Fprintf(w, "berth: %s %s (%s, %s) in %s%s\n", cloneDone(e), e.Target, strategyNote(e), bytesize.Format(e.Bytes), seconds(e), heldNote(*e))
			}
			for _, n := range e.Notes {
				fmt.Fprintf(w, "berth: database %s: %s\n", e.Target, n)
			}
		default:
			failed = append(failed, fmt.Sprintf("database %s: %s", e.Target, e.Error))
		}
	}
	return failed
}

// cloneDone says what add --wait reports for a finished database.
func cloneDone(e *volume.Entry) string {
	switch e.Strategy {
	case config.File:
		return fmt.Sprintf("loaded a seed file for database %s into", e.Source)
	case config.Command:
		return fmt.Sprintf("ran the command for database %s into", e.Source)
	case config.Snapshot:
		return fmt.Sprintf("restored the snapshot of database %s into", e.Source)
	}
	return fmt.Sprintf("copied database %s into", e.Source)
}

// strategyNote is a copy's strategy, with how the server made its files when it says.
func strategyNote(e *volume.Entry) string {
	if e.Method == "" {
		return e.Strategy
	}
	return e.Strategy + ", " + e.Method
}

func heldNote(e volume.Entry) string {
	if e.PauseMS == nil || len(e.Paused) == 0 {
		return ""
	}
	return fmt.Sprintf("; writes to main's %s paused for %s", strings.Join(e.Paused, ", "), (time.Duration(*e.PauseMS) * time.Millisecond).Round(time.Millisecond))
}
