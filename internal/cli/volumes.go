package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/bytesize"
	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/docker"
	"github.com/kodnificent/berth/internal/hostdb"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/volume"
)

// cloneCommand is the hidden command add starts in the background to copy volumes.
const cloneCommand = "__clone"

// testDelay makes the clone process wait at the end of each copy, inside the pause, so
// tests can stand in for a large volume without filling the disk.
const testDelay = "BERTH_TEST_CLONE_DELAY"

func init() {
	commands = append(commands, Command{Name: cloneCommand, Hidden: true, Run: runClone})
}

func cloneKeys(cfg *config.Config) []string {
	var keys []string
	for k, role := range cfg.Compose.Volumes {
		if role == "clone" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// startClones starts copying the berth's clone volumes and dump databases that aren't
// copied yet, or whose copy failed, in a background process, and records the volumes
// it creates in rec. It runs under the worktree lock and returns once the process has
// started. A failure to clone is reported on stderr and in the clone state, not
// returned: the berth is usable without its data, and status says why it has none. src
// is a connection to the berth's database server, nil without one.
func startClones(stderr io.Writer, cfg *config.Config, plan *composePlan, rec *registry.Record, src hostdb.Conn) error {
	keys := cloneKeys(cfg)
	if len(keys) > 0 && (plan.main == nil || plan.own == nil) {
		fmt.Fprintf(stderr, "berth: [compose.volumes] lists clone volumes, but main has no compose project to clone them from\n")
		keys = nil
	}
	if len(keys) == 0 && len(dumpKeys(*rec)) == 0 {
		return nil
	}
	lock, st, err := lockClones(stderr, *rec)
	if err != nil || lock == nil {
		return err
	}
	_, err = cloneUnder(lock, st, stderr, plan, cfg, rec, keys, src, false)
	return err
}

// lockClones takes rec's clone lock and loads its clone state, recovering a clone whose
// process died. lock is nil, with no error, while a clone process holds it.
func lockClones(stderr io.Writer, rec registry.Record) (*os.File, volume.State, error) {
	id := rec.ID
	lock, free, err := volume.TryLock(id)
	if err != nil || !free {
		return nil, volume.State{}, err
	}
	st, err := volume.Load(id)
	if err != nil {
		lock.Close()
		return nil, volume.State{}, err
	}
	if _, err := volume.Recover(id, &st); err != nil {
		fmt.Fprintf(stderr, "berth: %v\n", err)
	}
	settleCopies(rec, &st)
	return lock, st, nil
}

// cloneUnder is startClones once the clone lock is held, which passes to the clone
// process or is closed here. reset marks the entries as reset's, for recovery text.
// started lists the volume keys whose existing volume the clone process will empty.
func cloneUnder(lock *os.File, st volume.State, stderr io.Writer, plan *composePlan, cfg *config.Config, rec *registry.Record, keys []string, src hostdb.Conn, reset bool) (started []string, err error) {
	defer func() {
		if lock != nil {
			lock.Close()
		}
	}()

	var copies, reads []string
	if len(keys) > 0 {
		if copies, reads, err = prepareVolumes(&st, stderr, plan, cfg, rec, keys, reset); err != nil {
			return nil, err
		}
	}
	dumps := prepareDumps(&st, stderr, cfg, rec, src, reset)
	defer func() { recordDumps(rec, st) }()
	if err := volume.Save(rec.ID, st); err != nil {
		return nil, err
	}
	if len(copies) == 0 && len(dumps) == 0 {
		return nil, nil
	}
	err = spawnClone(rec.ID, lock)
	lock = nil
	if err != nil {
		msg := fmt.Sprintf("starting the clone process: %v", err)
		for _, key := range copies {
			if e, ok := st.Entry(key); ok {
				e.Status, e.Error = volume.Failed, msg
			}
		}
		for _, key := range dumps {
			if e, ok := st.Database(key); ok {
				e.Status, e.Error = volume.Failed, msg
			}
		}
		return nil, volume.Save(rec.ID, st)
	}
	for _, key := range reads {
		fmt.Fprintf(stderr, "berth: cloning volume %s in the background; berth status shows how far it is\n", key)
	}
	for _, key := range dumps {
		if e, ok := st.Database(key); ok {
			fmt.Fprintf(stderr, "berth: %s database %s into %s in the background; berth status shows how it goes\n", cloneVerb(e.Strategy), e.Source, e.Target)
		}
	}
	return copies, nil
}

// prepareVolumes puts in st an entry for each of keys that isn't cloned yet, creating
// and recording the berth's volumes the copies go into, and announces what the copies
// will pause. copies are the keys the clone process will copy or empty, reads those it
// copies from main.
func prepareVolumes(st *volume.State, stderr io.Writer, plan *composePlan, cfg *config.Config, rec *registry.Record, keys []string, reset bool) (copies, reads []string, err error) {
	var todo []volume.Entry
	for _, key := range keys {
		if e, ok := st.Entry(key); ok && e.Status == volume.Ready {
			continue
		}
		e := volume.Entry{
			Key:       key,
			Target:    cloneTarget(*rec, key),
			Helper:    rec.ComposeProject + "-berth-clone-" + key,
			Strategy:  volume.Copy,
			Status:    volume.Cloning,
			Reset:     reset,
			StartedAt: time.Now().UTC().Truncate(time.Millisecond),
		}
		source, external, declared := plan.main.Volume(key)
		if !declared || external {
			if slices.Contains(rec.Volumes, e.Target) {
				e.SourceMissing = true
				todo = append(todo, e)
				continue
			}
			msg := fmt.Sprintf("[compose.volumes] %s is clone, but main's compose project has no volume %s of its own to clone", key, key)
			if old, ok := st.Entry(key); ok {
				old.Status, old.Error = volume.Failed, msg
				todo = append(todo, *old)
			} else {
				fmt.Fprintf(stderr, "berth: %s\n", msg)
			}
			continue
		}
		e.Source = source
		if msg := refusal(plan, key); msg != "" {
			e.Status, e.Error = volume.Failed, msg
		}
		todo = append(todo, e)
	}
	if len(todo) == 0 {
		return nil, nil, nil
	}

	existing, err := docker.Volumes()
	if err != nil {
		for i := range todo {
			if todo[i].Status == volume.Cloning {
				todo[i].Status, todo[i].Error = volume.Failed, fmt.Sprintf("Docker is not reachable: %v", err)
			}
		}
	}
	var create []*volume.Entry
	for i := range todo {
		e := &todo[i]
		if e.Status != volume.Cloning {
			continue
		}
		if !existing[e.Target] {
			create = append(create, e)
			continue
		}
		if !slices.Contains(rec.Volumes, e.Target) {
			e.Status = volume.Failed
			e.Error = fmt.Sprintf("the berth's volume %s already exists and berth didn't create it, so berth won't overwrite it; remove it with docker volume rm %s and run berth add again", e.Target, e.Target)
		} else if project, _, err := docker.VolumeProject(e.Target); err != nil {
			e.Status, e.Error = volume.Failed, err.Error()
		} else if project != rec.ComposeProject {
			e.Status = volume.Failed
			e.Error = fmt.Sprintf("the berth's volume %s is now labelled as compose project %q's, so berth won't overwrite it", e.Target, project)
		}
	}
	if len(create) > 0 {
		for _, e := range create {
			rec.Volumes = union(rec.Volumes, []string{e.Target})
		}
		// Saved before the volumes exist, so a kill from here on never leaves a volume
		// berth made but doesn't know is its own.
		if err := saveRecord(*rec); err != nil {
			return nil, nil, err
		}
	}
	var copying, reading []*volume.Entry
	for i := range todo {
		e := &todo[i]
		if e.Status != volume.Cloning {
			continue
		}
		if !existing[e.Target] {
			driver, opts, labels := plan.main.VolumeSpec(e.Key)
			all := map[string]string{"com.docker.compose.project": rec.ComposeProject, "com.docker.compose.volume": e.Key}
			for k, v := range labels {
				all[k] = v
			}
			if err := docker.CreateVolume(e.Target, driver, opts, all); err != nil {
				e.Status, e.Error = volume.Failed, err.Error()
				continue
			}
		}
		if e.SourceMissing || !existing[e.Source] {
			if e.SourceMissing {
				fmt.Fprintf(stderr, "berth: main's compose project no longer has volume %s, so the berth's is left empty\n", e.Key)
			} else {
				// docker run would create main's volume rather than fail, so a missing source
				// is handled here: the berth's volume is left empty, as main's would be.
				e.SourceMissing = true
				fmt.Fprintf(stderr, "berth: main has no volume %s yet, so the berth's %s starts empty\n", e.Source, e.Key)
			}
			if existing[e.Target] {
				copying = append(copying, e)
				continue
			}
			now := time.Now().UTC().Truncate(time.Millisecond)
			e.Status, e.FinishedAt = volume.Ready, &now
			continue
		}
		copying = append(copying, e)
		reading = append(reading, e)
	}
	for _, e := range todo {
		if e.Status == volume.Failed {
			fmt.Fprintf(stderr, "berth: cannot clone volume %s: %s\n", e.Key, e.Error)
		}
	}

	if len(reading) > 0 {
		warnDisk(stderr, cfg, rec.ID, reading)
		for _, e := range reading {
			running, err := docker.RunningWithVolume(e.Source)
			if err != nil || len(running) == 0 {
				continue
			}
			var who, checked []string
			for _, c := range running {
				who = append(who, c.Label())
				if c.Healthcheck {
					checked = append(checked, c.Label())
				}
			}
			note := ""
			if len(checked) > 0 {
				note = fmt.Sprintf("; Docker reports %s as unhealthy while paused and until the next health check after it", strings.Join(checked, ", "))
			}
			fmt.Fprintf(stderr, "berth: main's %s will pause while volume %s is copied, so the copy is consistent, and resume as soon as the copy ends%s\n", strings.Join(who, ", "), e.Key, note)
		}
	}
	for _, e := range todo {
		st.Put(e)
	}
	for _, e := range copying {
		copies = append(copies, e.Key)
	}
	for _, e := range reading {
		reads = append(reads, e.Key)
	}
	return copies, reads, nil
}

// refusal is why berth won't copy main's volume key, or "" when it will.
func refusal(plan *composePlan, key string) string {
	driver, opts, _ := plan.main.VolumeSpec(key)
	if driver != "" && driver != "local" {
		return fmt.Sprintf("volume %s uses the %s driver; berth only copies local volumes", key, driver)
	}
	if dev, ok := opts["device"]; ok {
		return fmt.Sprintf("volume %s mounts %s, so a copy would be the same storage as main's; berth only copies volumes Docker stores itself", key, dev)
	}
	return ""
}

// warnDisk warns about each copy that would leave less free disk than [clone] min_free,
// counting the copies before it.
func warnDisk(stderr io.Writer, cfg *config.Config, id string, copies []*volume.Entry) {
	var sources []string
	for _, e := range copies {
		sources = append(sources, e.Source)
	}
	sizes, free, err := docker.Measure(id, sources, true)
	if err != nil {
		fmt.Fprintf(stderr, "berth: could not measure the volumes to clone, so free disk was not checked: %v\n", err)
		return
	}
	if host, ok := hostFree(); ok && host < free {
		free = host
	}
	min := cfg.MinFree()
	var total int64
	for i, e := range copies {
		e.Total = sizes[i]
		total += sizes[i]
		if left := free - total; left < min {
			fmt.Fprintf(stderr, "berth: cloning volume %s (%s) would leave %s of free disk, less than the %s that [clone] min_free asks for\n",
				e.Key, bytesize.Format(sizes[i]), bytesize.Format(max(left, 0)), bytesize.Format(min))
		}
	}
}

// hostFree is the free space on the host disk that holds Docker's data, which bounds a
// VM disk image that grows as volumes fill. Unknown when Docker isn't Docker Desktop.
func hostFree() (int64, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0, false
	}
	dir := filepath.Join(home, "Library", "Containers", "com.docker.docker", "Data")
	if _, err := os.Stat(dir); err != nil {
		return 0, false
	}
	return freeSpace(dir)
}

func spawnClone(id string, lock *os.File) error {
	logPath, err := volume.LogPath(id)
	if err != nil {
		return err
	}
	pid, err := spawnDetached(logPath, lock, cloneCommand, id)
	if err != nil {
		return err
	}
	return volume.WritePID(id, pid)
}

// spawnDetached starts berth with args in its own session, output appended to logPath.
// The lock passes to it and is held for as long as it lives, with no moment between this
// process and that one when nobody holds it. lock is closed here.
func spawnDetached(logPath string, lock *os.File, args ...string) (int, error) {
	defer lock.Close()
	self, err := os.Executable()
	if err != nil {
		return 0, err
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	defer log.Close()
	cmd := exec.Command(self, args...)
	cmd.Stdout, cmd.Stderr = log, log
	// Not the worktree add ran in: Windows refuses to remove or move a directory that a
	// running process has as its current directory.
	cmd.Dir = filepath.Dir(logPath)
	pass, err := passLock(cmd, lock)
	if err != nil {
		return 0, err
	}
	defer pass.close()
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	err = pass.wait(cmd.Process)
	_ = cmd.Process.Release()
	if err != nil {
		return 0, err
	}
	return pid, nil
}

func runClone(ctx *Context, args []string) error {
	if len(args) != 1 {
		return apperr.New(apperr.Usage, "berth %s is run by berth add", cloneCommand)
	}
	if !holdInheritedLock() {
		return apperr.New(apperr.Usage, "berth %s is run by berth add, which passes it the clone lock", cloneCommand)
	}
	if err := confineChildren(); err != nil {
		return err
	}
	delay, err := testCloneDelay()
	if err != nil {
		return err
	}
	if err := volume.Run(args[0], delay, ctx.Stderr); err != nil {
		return err
	}
	return runDumps(args[0], delay, ctx.Stderr)
}

// liveState is berth id's clone state with a clone whose process has died recovered,
// so it reads as failed instead of cloning forever, and with rec, what its template
// copies left on the server settled. pid is the running clone's process.
func liveState(id string, rec *registry.Record) (st volume.State, pid int, err error) {
	lock, free, err := volume.TryLock(id)
	if err != nil {
		return volume.State{}, 0, err
	}
	st, err = volume.Load(id)
	if err != nil || !free {
		if lock != nil {
			lock.Close()
		}
		if !free {
			pid = volume.ReadPID(id)
		}
		return st, pid, err
	}
	defer lock.Close()
	_, err = volume.Recover(id, &st)
	if rec != nil {
		settleCopies(*rec, &st)
	}
	return st, 0, err
}

// waitClones waits for the berth's clone process to end, then reports how each clone
// went. Any clone volume or database copy that isn't ready makes it fail with exit 4.
func waitClones(w io.Writer, cfg *config.Config, rec registry.Record) error {
	if err := volume.WaitUnlocked(rec.ID); err != nil {
		return err
	}
	st, _, err := liveState(rec.ID, &rec)
	if err != nil {
		return err
	}
	var failed []string
	for _, key := range cloneKeys(cfg) {
		e, ok := st.Entry(key)
		if !ok {
			continue
		}
		switch e.Status {
		case volume.Ready:
			if !e.SourceMissing {
				fmt.Fprintf(w, "berth: cloned volume %s (%s, %s) in %s%s\n", key, e.Strategy, bytesize.Format(e.Bytes), seconds(e), pauseNote(*e))
			}
		default:
			failed = append(failed, fmt.Sprintf("volume %s: %s", key, e.Error))
		}
	}
	failed = append(failed, waitDumps(w, rec, st)...)
	if len(failed) > 0 {
		return apperr.New(apperr.StateCloneFailed, "cloning failed for %s", strings.Join(failed, "; "))
	}
	return nil
}

func seconds(e *volume.Entry) time.Duration {
	if e.FinishedAt == nil {
		return 0
	}
	return e.FinishedAt.Sub(e.StartedAt).Round(100 * time.Millisecond)
}

func pauseNote(e volume.Entry) string {
	if e.PauseMS == nil || len(e.Paused) == 0 {
		return ""
	}
	return fmt.Sprintf("; main's %s paused for %s", strings.Join(e.Paused, ", "), (time.Duration(*e.PauseMS) * time.Millisecond).Round(10*time.Millisecond))
}

type stateJSON struct {
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	Volume        string   `json:"volume,omitempty"`
	Source        string   `json:"source"`
	Target        string   `json:"target"`
	Strategy      string   `json:"strategy"`
	Status        string   `json:"status"`
	PID           int      `json:"pid,omitempty"`
	Bytes         int64    `json:"bytes,omitempty"`
	Progress      *float64 `json:"progress,omitempty"`
	CopiedBytes   int64    `json:"copied_bytes,omitempty"`
	TotalBytes    int64    `json:"total_bytes,omitempty"`
	Seconds       *float64 `json:"seconds,omitempty"`
	Paused        []string `json:"paused,omitempty"`
	PauseSeconds  *float64 `json:"pause_seconds,omitempty"`
	SourceMissing bool     `json:"source_missing,omitempty"`
	Notes         []string `json:"notes,omitempty"`
	Log           string   `json:"log,omitempty"`
	Error         string   `json:"error,omitempty"`
	// SnapshotTakenAt and SnapshotAgeSeconds are how old a snapshot database's data is.
	SnapshotTakenAt    *time.Time `json:"snapshot_taken_at,omitempty"`
	SnapshotAgeSeconds *float64   `json:"snapshot_age_seconds,omitempty"`
	Method             string     `json:"method,omitempty"`
}

// stateEntries are the berth's volumes and databases as status reports them.
func stateEntries(rec registry.Record) []stateJSON {
	out := []stateJSON{}
	st, pid, err := liveState(rec.ID, &rec)
	if err != nil {
		st = volume.State{}
	}
	for _, e := range st.Volumes {
		out = append(out, entryJSON(e, pid))
	}
	return append(out, databaseEntries(rec, st, pid)...)
}

// entryJSON is clone entry e as status reports it, as a volume; pid is the running clone
// process.
func entryJSON(e volume.Entry, pid int) stateJSON {
	j := stateJSON{Name: e.Key, Kind: kindVolume, Volume: e.Target, Source: e.Source, Target: e.Target, Strategy: e.Strategy, Status: e.Status,
		Bytes: e.Bytes, Paused: e.Paused, SourceMissing: e.SourceMissing, Notes: e.Notes, Log: e.Log, Error: e.Error, Method: e.Method}
	if clonedStrategy(e.Strategy) {
		j.Volume = ""
	}
	if e.Status == volume.Cloning {
		j.PID, j.CopiedBytes, j.TotalBytes = pid, e.Copied, e.Total
		if p, ok := e.Progress(); ok {
			j.Progress = &p
		}
	}
	if e.FinishedAt != nil {
		s := e.FinishedAt.Sub(e.StartedAt).Seconds()
		j.Seconds = &s
	}
	if e.PauseMS != nil {
		s := float64(*e.PauseMS) / 1000
		j.PauseSeconds = &s
	}
	if e.SnapshotTakenAt != nil {
		age := time.Since(*e.SnapshotTakenAt).Seconds()
		j.SnapshotTakenAt, j.SnapshotAgeSeconds = e.SnapshotTakenAt, &age
	}
	return j
}

// diskUse is the apparent size of everything berth created for each berth, from what it
// recorded: the files it wrote, as they are now, each directory it cloned at its size
// when cloned, each volume and database it copied at how much has been copied into it,
// and each other database it made and SQLite file it copied at its size when made.
// Nothing is walked, and neither Docker nor a database server is asked, so list returns
// promptly however large a berth's clones are. Copy-on-write clones share blocks with
// main, so this overstates the disk they take, and growth since a clone isn't counted.
func diskUse(berths []registry.Live) map[string]int64 {
	use := map[string]int64{}
	for _, b := range berths {
		for _, f := range b.Files {
			if n, ok := b.Sizes[f]; ok {
				use[b.ID] += n
			} else if info, err := os.Lstat(filepath.Join(b.Worktree, f)); err == nil && info.Mode().IsRegular() {
				use[b.ID] += info.Size()
			}
		}
		for _, e := range b.SQLite {
			if e.Own {
				use[b.ID] += e.Bytes
			}
		}
		dumps := dumpKeys(b.Record)
		var st volume.State
		if len(b.Volumes) > 0 || len(dumps) > 0 {
			st, _ = volume.Load(b.ID)
		}
		if b.DB() != nil {
			for _, db := range b.DB().Databases {
				if e, ok := st.Database(db.Key); ok && clonedStrategy(db.Strategy) && e.Target == db.Target {
					use[b.ID] += max(e.Bytes, e.Copied)
				} else if db.Strategy != config.None {
					use[b.ID] += db.Bytes
				}
			}
		}
		for _, e := range st.Volumes {
			if slices.Contains(b.Volumes, e.Target) {
				use[b.ID] += max(e.Bytes, e.Copied)
			}
		}
	}
	return use
}

// resumeDeadClones recovers every berth whose clone process died, so a container of
// main's that a killed copy left paused resumes on a command run anywhere, not only in
// that berth's worktree, where nobody may look while main is frozen.
func resumeDeadClones(all []registry.Live, stderr io.Writer) {
	for _, b := range all {
		if _, _, err := liveState(b.ID, nil); err != nil {
			fmt.Fprintf(stderr, "berth: berth %s (%s): %v\n", b.ID, b.Worktree, err)
		}
	}
}
