// Package registry records every berth on the machine, across repositories, and ties
// each one to its worktree through a marker git keeps with the worktree.
package registry

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/kodnificent/berth/internal/lockfile"
	"github.com/kodnificent/berth/internal/ports"
	"github.com/kodnificent/berth/internal/safefile"
	"github.com/kodnificent/berth/internal/worktree"
)

// markerName is a file in the worktree's git administrative directory. Git moves that
// directory with the worktree and deletes it on git worktree remove, so the marker
// follows moves and never survives into a new worktree that reuses the name.
const markerName = "berth-id"

// worktreeLockName sits beside the marker, so it moves with the worktree too.
const worktreeLockName = "berth-lock"

type Record struct {
	ID         string    `json:"id"`
	Repository string    `json:"repository"`
	CommonDir  string    `json:"common_dir"`
	AdminDir   string    `json:"admin_dir"`
	Worktree   string    `json:"worktree"`
	CreatedAt  time.Time `json:"created_at"`

	// Name, Host and ComposeProject are unique among all berths on the machine.
	Name           string `json:"name,omitempty"`
	Host           string `json:"host,omitempty"`
	ComposeProject string `json:"compose_project,omitempty"`
	// Ports are reserved for this berth until its record is deleted.
	Ports []ports.Port `json:"ports,omitempty"`
	// ComposeFile is the COMPOSE_FILE the berth's .env sets, empty with no compose project.
	ComposeFile string `json:"compose_file,omitempty"`

	// Files are the paths, relative to the worktree, that berth created there. Nothing
	// else in the worktree is berth's to delete.
	Files []string `json:"files,omitempty"`
	// Sizes are the apparent sizes of the directories among Files, recorded when berth
	// cloned them.
	Sizes map[string]int64 `json:"sizes,omitempty"`
	// Volumes are the Docker volumes berth created for this berth.
	Volumes []string `json:"volumes,omitempty"`
	// Images are image tags berth gave this berth, each naming an image main built.
	Images []string `json:"images,omitempty"`
	// Builds are what add found, for each service the berth builds that main builds too,
	// about using main's image.
	Builds []Build `json:"builds,omitempty"`
	// Cloned are the [files] clone paths already cloned, which later adds leave alone so
	// a file deleted in the berth stays deleted.
	Cloned []string `json:"cloned,omitempty"`
	// EnvSeparator is true when berth put a newline before the managed block because the
	// .env it found didn't end with one; removing the block removes that newline too.
	EnvSeparator bool `json:"env_separator,omitempty"`
	// MySQL and Postgres are what berth made for this berth on a host database server,
	// saved before any database is created, under the key of its engine. They are all
	// remove, prune and reset act on, whatever the config says now; a berth made before
	// berth managed databases has neither, and no berth has both.
	MySQL    *HostDB `json:"mysql,omitempty"`
	Postgres *HostDB `json:"postgres,omitempty"`
	// SQLite is each [state.sqlite] file berth has handled for this berth, saved before
	// any copy is written.
	SQLite []SQLiteFile `json:"sqlite,omitempty"`
	// Redis is the berth's key prefix on a host Redis server, saved before the prefix is
	// written to the berth's .env.
	Redis *Redis `json:"redis,omitempty"`
}

// Redis is a berth's key prefix on a host Redis server. It never holds a login.
type Redis struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	// EnvPrefix is the prefix of the keys the server and login are read from, such as
	// REDIS_; remove reads the login from the berth's own .env under it.
	EnvPrefix string `json:"env_prefix"`
	Segment   string `json:"segment"`
	// Prefix is the berth's key prefix. Every key under it, on every logical database of
	// the server, is the berth's.
	Prefix    string `json:"prefix"`
	PrefixKey string `json:"prefix_key"`
}

// SQLiteFile is one of main's SQLite databases and the berth's copy of it.
type SQLiteFile struct {
	// Path is the database's path, relative to the worktree, in main and in the berth.
	Path string `json:"path"`
	// Own is set before berth writes the berth's copy at Path, and only then are that file
	// and SQLite's own files beside it berth's to delete.
	Own bool `json:"own,omitempty"`
	// Dirs are the directories berth created to hold the copy, outermost first. Removal
	// takes those that are empty once the copy is gone.
	Dirs     []string `json:"dirs,omitempty"`
	Strategy string   `json:"strategy,omitempty"`
	Status   string   `json:"status"`
	// SourceMissing is a file main didn't have, so the berth got none.
	SourceMissing bool   `json:"source_missing,omitempty"`
	Error         string `json:"error,omitempty"`
	// Bytes is the copy's size when made.
	Bytes   int64    `json:"bytes,omitempty"`
	Seconds *float64 `json:"seconds,omitempty"`
}

// DB is the berth's place on a host database server, nil when it has none.
func (r Record) DB() *HostDB {
	if r.Postgres != nil {
		return r.Postgres
	}
	return r.MySQL
}

// HostDB is a berth's place on a host database server. It never holds a login.
type HostDB struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	// EnvPrefix is the prefix of the keys the server and login are read from, such as
	// DB_; remove reads the login from the berth's own .env under it. URLKey is set
	// instead when one URL holds them, such as DATABASE_URL, and only its key is kept here:
	// the URL itself holds the login.
	EnvPrefix string `json:"env_prefix"`
	URLKey    string `json:"url_key,omitempty"`
	// Segment is the berth's part of every database name it has.
	Segment string `json:"segment"`
	// Prefix is the berth's database prefix, empty without one. Every database under it
	// is the berth's, including those the app creates.
	Prefix string `json:"prefix,omitempty"`
	// PrefixKey is the env key the berth's prefix is written to.
	PrefixKey string     `json:"prefix_key,omitempty"`
	Databases []Database `json:"databases,omitempty"`
}

// Database is one of main's databases and the berth's own for it.
type Database struct {
	Key      string `json:"key"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	Strategy string `json:"strategy"`
	// File is the seed file loaded into Target, relative to the main worktree, for the
	// file strategy; Command is the script for the command strategy.
	File    string `json:"file,omitempty"`
	Command string `json:"command,omitempty"`
	// Env are the keys in main's .env naming Source, which the berth's .env sets to
	// Target.
	Env []string `json:"env,omitempty"`
	// URL says main's URL names Source, so the berth's URL names Target.
	URL    bool   `json:"url,omitempty"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// Bytes is the database's size when berth made it.
	Bytes int64 `json:"bytes,omitempty"`
}

// Owns reports whether berth made the database name for this berth, so it is the
// berth's to drop: one it created from the record, or any under its prefix.
func (m *HostDB) Owns(name string) bool {
	if m == nil {
		return false
	}
	for _, d := range m.Databases {
		if d.Source == name {
			return false
		}
	}
	if m.Prefix != "" && strings.HasPrefix(name, m.Prefix) {
		return true
	}
	for _, d := range m.Databases {
		if d.Target == name && d.Strategy != "none" {
			return true
		}
	}
	return false
}

// Build is whether a berth uses main's image for one service, and why not when it
// doesn't.
type Build struct {
	Service string `json:"service"`
	Shared  bool   `json:"shared"`
	Reason  string `json:"reason,omitempty"`
	// ReadBytes is how much of the build context berth read to decide.
	ReadBytes int64 `json:"read_bytes"`
}

// Live is a record checked against git.
type Live struct {
	Record
	Orphaned bool
	Locked   bool
}

type Registry struct {
	dir  string
	lock *os.File
}

// StateDir is where the registry lives. BERTH_STATE_DIR overrides it so tests never
// touch the user's registry.
func StateDir() (string, error) {
	if d := os.Getenv("BERTH_STATE_DIR"); d != "" {
		return d, nil
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" && filepath.IsAbs(d) {
		return filepath.Join(d, "berth"), nil
	}
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "berth"), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding the berth state directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "berth"), nil
}

// Open opens the registry and holds its lock until Close, so two berth processes never
// interleave a read and a write.
func Open() (*Registry, error) {
	dir, err := StateDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "berths"), 0o755); err != nil {
		return nil, fmt.Errorf("creating the berth state directory: %w", err)
	}
	f, err := lockfile.Open(filepath.Join(dir, "lock"), true)
	if err != nil {
		return nil, fmt.Errorf("opening the registry lock: %w", err)
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking the registry: %w", err)
	}
	r := &Registry{dir: dir, lock: f}
	if err := r.reconcile(); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

// LockWorktree takes w's own lock, held until the file is closed. add fills in a berth
// outside the registry lock while holding it, so two adds of one worktree, or an add and
// a remove, never interleave. It is always taken before the registry lock and never
// while holding it, so the two can't deadlock.
func LockWorktree(w *worktree.Worktree) (*os.File, error) {
	f, err := lockfile.Open(filepath.Join(w.AdminDir, worktreeLockName), true)
	if err != nil {
		return nil, fmt.Errorf("opening the lock of worktree %s: %w", w.Path, err)
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking worktree %s: %w", w.Path, err)
	}
	return f, nil
}

// WorktreeBusy reports whether a berth command holds the lock of the worktree whose git
// administrative directory is adminDir.
func WorktreeBusy(adminDir string) bool {
	f, err := lockfile.Open(filepath.Join(adminDir, worktreeLockName), false)
	if err != nil {
		return false
	}
	defer f.Close()
	free, err := tryLockFile(f)
	return err == nil && !free
}

// WaitWorktree returns once no berth command holds the lock of the worktree whose git
// administrative directory is adminDir.
func WaitWorktree(adminDir string) error {
	f, err := lockfile.Open(filepath.Join(adminDir, worktreeLockName), false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	return lockFile(f)
}

// reconcile frees the ports and host of every orphaned berth, so the command that first
// sees a worktree gone hands them back to the pool. The name and compose project stay
// reserved: the orphan's volumes still carry them until it is pruned.
func (r *Registry) reconcile() error {
	recs, err := r.records("")
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if len(rec.Ports) == 0 && rec.Host == "" {
			continue
		}
		if check(rec).Orphaned {
			rec.Ports, rec.Host = nil, ""
			if err := r.save(rec); err != nil {
				return err
			}
		}
	}
	return nil
}

// IfRecorded runs fn holding the registry lock, and only while berth id is recorded, so
// what fn writes for the berth is never left behind by a remove that ran first: remove
// forgets a berth's state and record under the same lock. It never creates the lock or
// the state directory, and reports whether fn ran.
func IfRecorded(id string, fn func() error) (bool, error) {
	dir, err := StateDir()
	if err != nil {
		return false, err
	}
	f, err := lockfile.Open(filepath.Join(dir, "lock"), false)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return false, err
	}
	defer unlockFile(f)
	if _, err := os.Stat(filepath.Join(dir, "berths", id+".json")); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, fn()
}

// Delete forgets berth rec: its record, and its worktree's marker while that still
// names it.
func (r *Registry) Delete(rec Record) error {
	if readMarker(rec.AdminDir) == rec.ID {
		if err := os.Remove(filepath.Join(rec.AdminDir, markerName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := os.Remove(r.path(rec.ID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (r *Registry) Close() error {
	_ = unlockFile(r.lock)
	return r.lock.Close()
}

// Add returns the worktree's berth, creating it when the worktree has none. created
// reports whether this call made it. provision fills in what the berth is allocated,
// seeing every other berth's record; when it fails, nothing is written.
func (r *Registry) Add(w *worktree.Worktree, provision func(rec *Record, others []Record) error) (rec Record, created bool, err error) {
	rec, found, err := r.Find(w)
	if err != nil {
		return Record{}, false, err
	}
	before, _ := json.Marshal(rec)
	if !found {
		id := readMarker(w.AdminDir)
		if id == "" {
			if id, err = newID(); err != nil {
				return Record{}, false, err
			}
		}
		rec = Record{
			ID:         id,
			Repository: w.Repository,
			CommonDir:  w.CommonDir,
			AdminDir:   w.AdminDir,
			Worktree:   w.Path,
			CreatedAt:  time.Now().UTC().Truncate(time.Second),
		}
	}
	others, err := r.records(rec.ID)
	if err != nil {
		return Record{}, false, err
	}
	if err := provision(&rec, others); err != nil {
		return Record{}, false, err
	}
	if !found && readMarker(w.AdminDir) != rec.ID {
		// The marker goes first: a crash before the record is written leaves a marker that
		// the next add adopts, never a record no worktree points at.
		if err := writeAtomic(filepath.Join(w.AdminDir, markerName), []byte(rec.ID+"\n")); err != nil {
			return Record{}, false, fmt.Errorf("marking worktree %s: %w", w.Path, err)
		}
	}
	if after, _ := json.Marshal(rec); found && string(after) == string(before) {
		return rec, false, nil
	}
	return rec, !found, r.save(rec)
}

// Update saves rec when it differs from the stored record, so an unchanged berth's
// record is never rewritten.
func (r *Registry) Update(rec Record) error {
	if old, err := r.load(rec.ID); err == nil {
		before, _ := json.Marshal(old)
		after, _ := json.Marshal(rec)
		if string(before) == string(after) {
			return nil
		}
	}
	return r.save(rec)
}

// records loads every berth's record except the one with id except.
func (r *Registry) records(except string) ([]Record, error) {
	entries, err := os.ReadDir(filepath.Join(r.dir, "berths"))
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || id == except {
			continue
		}
		rec, err := r.load(id)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// Find returns the worktree's berth, if it has one, with its recorded path brought up
// to date after a move.
func (r *Registry) Find(w *worktree.Worktree) (Record, bool, error) {
	id := readMarker(w.AdminDir)
	if id == "" {
		return Record{}, false, nil
	}
	rec, err := r.load(id)
	if errors.Is(err, fs.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	if rec.Worktree != w.Path || rec.AdminDir != w.AdminDir || rec.Repository != w.Repository || rec.CommonDir != w.CommonDir {
		rec.Worktree, rec.AdminDir, rec.Repository, rec.CommonDir = w.Path, w.AdminDir, w.Repository, w.CommonDir
		if err := r.save(rec); err != nil {
			return Record{}, false, err
		}
	}
	return rec, true, nil
}

// All returns every berth checked against git, sorted by repository and worktree.
// Paths of moved worktrees are updated as a side effect.
func (r *Registry) All() ([]Live, error) {
	recs, err := r.records("")
	if err != nil {
		return nil, err
	}
	var out []Live
	for _, rec := range recs {
		live := check(rec)
		if live.Worktree != rec.Worktree {
			if err := r.save(live.Record); err != nil {
				return nil, err
			}
		}
		out = append(out, live)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Repository != b.Repository {
			return a.Repository < b.Repository
		}
		if a.Worktree != b.Worktree {
			return a.Worktree < b.Worktree
		}
		return a.CreatedAt.Before(b.CreatedAt)
	})
	return out, nil
}

// Peek returns every berth checked against git, without taking the registry lock or
// writing anything, for a reader that runs alongside other berth commands. Records are
// replaced whole, so each one read is complete. One deleted meanwhile is left out; one
// whose file is there but can't be read or parsed is in unreadable, by id, since the
// berth still exists.
func Peek() (live []Live, unreadable map[string]error, err error) {
	dir, err := StateDir()
	if err != nil {
		return nil, nil, err
	}
	entries, err := os.ReadDir(filepath.Join(dir, "berths"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	r := &Registry{dir: dir}
	unreadable = map[string]error{}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		rec, err := r.load(id)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			unreadable[id] = err
		default:
			live = append(live, check(rec))
		}
	}
	return live, unreadable, nil
}

// check decides whether a berth is orphaned: its worktree is orphaned once git has
// forgotten it, or would forget it on the next prune. A locked worktree is never
// pruned, so its berth is kept even while its directory is missing.
func check(rec Record) Live {
	live := Live{Record: rec}
	if readMarker(rec.AdminDir) != rec.ID {
		live.Orphaned = true
		return live
	}
	l := worktree.Inspect(rec.AdminDir)
	if !l.Exists {
		live.Orphaned = true
		return live
	}
	live.Locked = l.Locked
	live.Orphaned = l.Prunable
	if !l.Prunable {
		live.Worktree = l.Path
	}
	return live
}

// Get reads berth id's record. ok is false when there is none.
func (r *Registry) Get(id string) (rec Record, ok bool, err error) {
	rec, err = r.load(id)
	if errors.Is(err, fs.ErrNotExist) {
		return Record{}, false, nil
	}
	return rec, err == nil, err
}

func (r *Registry) path(id string) string {
	return filepath.Join(r.dir, "berths", id+".json")
}

func (r *Registry) load(id string) (Record, error) {
	data, err := os.ReadFile(r.path(id))
	if err != nil {
		return Record{}, err
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, fmt.Errorf("reading berth record %s: %w", r.path(id), err)
	}
	return rec, nil
}

func (r *Registry) save(rec Record) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(r.path(rec.ID), append(data, '\n'))
}

func readMarker(adminDir string) string {
	data, err := os.ReadFile(filepath.Join(adminDir, markerName))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return safefile.Replace(tmp.Name(), path)
}
