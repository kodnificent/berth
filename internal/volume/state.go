// Package volume clones a berth's compose volumes from main's in a background process
// and keeps the record that tells everyone else how each clone stands.
package volume

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kodnificent/berth/internal/lockfile"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/safefile"
)

const (
	Cloning = "cloning"
	Ready   = "ready"
	Failed  = "failed"
)

// Copy is the only strategy for a Docker volume in v0.1: on Docker Desktop volumes live
// on ext4 inside the VM, which has no copy-on-write.
const Copy = "copy"

// Entry is one clone volume of a berth.
type Entry struct {
	Key      string `json:"key"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	Helper   string `json:"helper"`
	Strategy string `json:"strategy"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
	// SourceMissing is true when main had no such volume, so the berth's is left empty.
	SourceMissing bool `json:"source_missing,omitempty"`
	// Reset is true when berth reset started this clone, so a failed one says to run
	// reset again rather than add.
	Reset      bool       `json:"reset,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Bytes      int64      `json:"bytes,omitempty"`
	// Total is the source's size when the clone started, and Copied how much of it has
	// reached the berth's volume so far, both as du counts them; they give the progress.
	Total  int64 `json:"total,omitempty"`
	Copied int64 `json:"copied,omitempty"`
	// Paused names the services paused for the copy, PauseMS how long they were.
	Paused  []string `json:"paused,omitempty"`
	PauseMS *int64   `json:"pause_ms,omitempty"`
	// Announced names the tables add or reset said a database copy would hold writes to;
	// the copy holds no others.
	Announced []string `json:"announced,omitempty"`
	// Notes name what a database copy left out of the berth's, and why.
	Notes []string `json:"notes,omitempty"`
	// SnapshotTakenAt is when the snapshot this database was restored from was read from
	// main, set once the restore has found it.
	SnapshotTakenAt *time.Time `json:"snapshot_taken_at,omitempty"`
	// Log is the file a command database's script wrote its output to.
	Log string `json:"log,omitempty"`
	// ScriptPID is the process group of a running command script, ScriptMark what tells
	// it from a later process given the same pid. Whoever finds the clone process dead
	// stops that group first.
	ScriptPID  int    `json:"script_pid,omitempty"`
	ScriptMark string `json:"script_mark,omitempty"`
	// Disconnect is set when add or reset said a template copy would end main's sessions on
	// its source; the clone process ends them only then.
	Disconnect bool `json:"disconnect,omitempty"`
	// CopySession is the server session running a template copy, kept until the copy ends
	// and the server is known to have nothing left of it. Whoever finds the clone process
	// dead ends that session and drops what it made.
	CopySession *Session `json:"copy_session,omitempty"`
	// Method is how the server made a template copy's files.
	Method string `json:"method,omitempty"`
	// Pausing holds the containers paused right now. It is written before they pause,
	// so if the clone process dies mid-copy, whoever notices can resume them.
	Pausing []string `json:"pausing,omitempty"`
}

// Session is one session on a database server. Its start time tells it from a later
// session given the same pid.
type Session struct {
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
}

type State struct {
	Volumes []Entry `json:"volumes"`
	// Databases are the berth's copies of main's host databases, made by the same clone
	// process under the same lock. They never touch Docker.
	Databases []Entry `json:"databases,omitempty"`
}

// Entry returns the entry for key, if there is one.
func (s *State) Entry(key string) (*Entry, bool) {
	for i := range s.Volumes {
		if s.Volumes[i].Key == key {
			return &s.Volumes[i], true
		}
	}
	return nil, false
}

// Put replaces the entry with e's key, or adds e.
func (s *State) Put(e Entry) { s.Volumes = put(s.Volumes, e) }

// Database returns the database entry for key, if there is one.
func (s *State) Database(key string) (*Entry, bool) {
	for i := range s.Databases {
		if s.Databases[i].Key == key {
			return &s.Databases[i], true
		}
	}
	return nil, false
}

// PutDatabase replaces the database entry with e's key, or adds e.
func (s *State) PutDatabase(e Entry) { s.Databases = put(s.Databases, e) }

func put(list []Entry, e Entry) []Entry {
	for i := range list {
		if list[i].Key == e.Key {
			list[i] = e
			return list
		}
	}
	return append(list, e)
}

// Progress is how far a clone is, from 0 to 1, and false when it can't be told. A
// clone is never shown done before it is, since the source may grow while it runs.
func (e *Entry) Progress() (float64, bool) {
	if e.Status != Cloning || e.Total <= 0 {
		return 0, false
	}
	return min(float64(e.Copied)/float64(e.Total), 0.99), true
}

// Pending reports whether any entry is still cloning.
func (s *State) Pending() bool {
	for _, e := range append(s.Volumes, s.Databases...) {
		if e.Status == Cloning {
			return true
		}
	}
	return false
}

func dir() (string, error) {
	d, err := registry.StateDir()
	if err != nil {
		return "", err
	}
	d = filepath.Join(d, "clones")
	return d, os.MkdirAll(d, 0o755)
}

func path(id, ext string) (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, id+ext), nil
}

// Load reads berth id's clone state; a berth that never cloned has an empty one.
func Load(id string) (State, error) {
	p, err := path(id, ".json")
	if err != nil {
		return State{}, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, fmt.Errorf("reading clone state %s: %w", p, err)
	}
	return s, nil
}

// Save writes berth id's clone state. Only the holder of the berth's clone lock may.
func Save(id string, s State) error {
	p, err := path(id, ".json")
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return safefile.Replace(tmp, p)
}

// TryLock takes berth id's clone lock without waiting. The clone process holds it for
// as long as it lives, and the kernel releases it however the process ends, so a lock
// that can be taken means no clone is running. ok is false while one is.
func TryLock(id string) (f *os.File, ok bool, err error) {
	p, err := path(id, ".lock")
	if err != nil {
		return nil, false, err
	}
	f, err = lockfile.Open(p, true)
	if err != nil {
		return nil, false, err
	}
	ok, err = tryLock(f)
	if err != nil || !ok {
		f.Close()
		return nil, false, err
	}
	return f, true, nil
}

// WaitUnlocked returns once no clone process of berth id is running.
func WaitUnlocked(id string) error {
	p, err := path(id, ".lock")
	if err != nil {
		return err
	}
	f, err := lockfile.Open(p, true)
	if err != nil {
		return err
	}
	defer f.Close()
	return waitLock(f)
}

// WritePID records the clone process's pid for status to report.
func WritePID(id string, pid int) error {
	p, err := path(id, ".pid")
	if err != nil {
		return err
	}
	return os.WriteFile(p, []byte(strconv.Itoa(pid)+"\n"), 0o644)
}

func ReadPID(id string) int {
	p, err := path(id, ".pid")
	if err != nil {
		return 0
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return n
}

// LogPath is where the clone process of berth id writes what it did.
func LogPath(id string) (string, error) { return path(id, ".log") }

// CommandLogPath is where the script of berth id's command database key writes its
// output. Non-file-safe characters of key, such as a pattern's *, become _.
func CommandLogPath(id, key string) (string, error) {
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, key)
	return path(id, ".command-"+safe+".log")
}

// Forget deletes everything berth keeps about berth id's clones. The caller holds the
// clone lock, so no clone process is using them.
func Forget(id string) error {
	for _, ext := range []string{".json", ".pid", ".log", ".lock"} {
		p, err := path(id, ext)
		if err != nil {
			return err
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	d, err := dir()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(d)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), id+".command-") && strings.HasSuffix(e.Name(), ".log") {
			if err := os.Remove(filepath.Join(d, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}
