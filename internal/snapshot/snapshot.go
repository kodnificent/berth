// Package snapshot keeps each repository's database snapshots in berth's state
// directory: a directory per generation, and a manifest naming the complete one. A new
// snapshot is written as a new generation and becomes current only when the manifest is
// replaced, in one rename, so whatever stops the write leaves the previous snapshot as it
// was. Files are private to the user, since they hold the user's data.
package snapshot

import (
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/safefile"
)

const (
	// format is bumped when the stream's layout changes; a manifest of another format
	// reads as no snapshot, so the next berth snapshot replaces it.
	format = 1

	manifestName = "manifest.json"
	generationOf = "gen-"
)

// Database is one database's data in a snapshot. Nothing in it names a login.
type Database struct {
	Key       string `json:"key"`
	Source    string `json:"source"`
	File      string `json:"file"`
	Charset   string `json:"charset,omitempty"`
	Collation string `json:"collation,omitempty"`
	// TakenAt is when the consistent read of the database began.
	TakenAt time.Time `json:"taken_at"`
	// Bytes is the source's size as the server counted it, FileBytes the file's.
	Bytes     int64 `json:"bytes"`
	FileBytes int64 `json:"file_bytes"`
	// SourceMissing is true when main had no such database, so the snapshot holds it empty.
	SourceMissing bool     `json:"source_missing,omitempty"`
	Notes         []string `json:"notes,omitempty"`
}

// Manifest names a repository's current snapshot. Nothing in it names a login.
type Manifest struct {
	Format     int        `json:"format"`
	Host       string     `json:"host"`
	Port       int        `json:"port"`
	Generation string     `json:"generation"`
	Databases  []Database `json:"databases"`
}

func (m Manifest) Database(key string) (Database, bool) {
	for _, d := range m.Databases {
		if d.Key == key {
			return d, true
		}
	}
	return Database{}, false
}

// dir is repository's snapshot directory, keyed by a hash of its path so a repository
// that shares berth's state with others never mixes with them.
func dir(repository string) (string, error) {
	root, err := registry.StateDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(repository))
	return filepath.Join(root, "snapshots", hex.EncodeToString(sum[:8])), nil
}

// Load reads repository's current manifest. ok is false when it has no snapshot yet.
func Load(repository string) (m Manifest, ok bool, err error) {
	d, err := dir(repository)
	if err != nil {
		return m, false, err
	}
	data, err := os.ReadFile(filepath.Join(d, manifestName))
	if errors.Is(err, fs.ErrNotExist) {
		return m, false, nil
	}
	if err != nil {
		return m, false, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, false, fmt.Errorf("reading snapshot manifest %s: %w", filepath.Join(d, manifestName), err)
	}
	if m.Format != format {
		return Manifest{}, false, nil
	}
	return m, true, nil
}

// Set is a generation's files for one clone run. Held open, they outlive that generation
// being replaced, so every database a run restores comes from the same snapshot.
type Set struct {
	Manifest
	files map[string]*os.File
}

// Open opens the current generation's files for those of keys it holds. ok is false when
// the repository has no snapshot.
func Open(repository string, keys []string) (*Set, bool, error) {
	d, err := dir(repository)
	if err != nil {
		return nil, false, err
	}
	for attempt := 0; ; attempt++ {
		m, ok, err := Load(repository)
		if err != nil || !ok {
			return nil, false, err
		}
		set, err := openFiles(m, filepath.Join(d, m.Generation), keys)
		// A berth snapshot replacing this generation may remove it between reading the
		// manifest and opening its files, so the new manifest is read instead.
		if errors.Is(err, fs.ErrNotExist) && attempt < 3 {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		return set, true, nil
	}
}

func openFiles(m Manifest, generation string, keys []string) (*Set, error) {
	set := &Set{Manifest: m, files: map[string]*os.File{}}
	for _, key := range keys {
		db, ok := m.Database(key)
		if !ok {
			continue
		}
		f, err := openShared(filepath.Join(generation, db.File))
		if err != nil {
			set.Close()
			return nil, fmt.Errorf("opening the snapshot of database %s: %w", key, err)
		}
		set.files[key] = f
	}
	return set, nil
}

// Close closes every file of the set. A nil Set is empty.
func (s *Set) Close() {
	if s == nil {
		return
	}
	for _, f := range s.files {
		f.Close()
	}
}

// Reader reads database key's data from the set.
func (s *Set) Reader(key string) (*Reader, error) {
	f, ok := s.files[key]
	if !ok {
		return nil, fmt.Errorf("the snapshot holds no database %s", key)
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("reading the snapshot of database %s: %w", key, err)
	}
	return &Reader{gz: gz, file: f}, nil
}

type Reader struct {
	gz   *gzip.Reader
	file *os.File
}

func (r *Reader) Read(p []byte) (int, error) { return r.gz.Read(p) }

// Copied is how far into the file reading is, for how far a restore has got.
func (r *Reader) Copied() int64 {
	pos, _ := r.file.Seek(0, io.SeekCurrent)
	return pos
}

// Generation is a snapshot being written, not yet the repository's current one.
type Generation struct {
	repository string
	dir, name  string
	files      int
	done       bool
}

// Begin starts a new generation for repository, first removing any left by a berth
// snapshot that didn't finish. berth snapshot holds the main worktree's lock around it
// so that two runs never interleave.
func Begin(repository string) (*Generation, error) {
	d, err := dir(repository)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return nil, fmt.Errorf("creating the snapshot directory %s: %w", d, err)
	}
	for _, p := range []string{filepath.Dir(d), d} {
		if err := private(p); err != nil {
			return nil, fmt.Errorf("making the snapshot directory %s private: %w", p, err)
		}
	}
	m, ok, err := Load(repository)
	if err != nil {
		return nil, err
	}
	current := ""
	if ok {
		current = m.Generation
	}
	// A leftover that can't go now doesn't stop this run; RemoveReplaced reports it after.
	_ = removeGenerations(d, current)
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return nil, err
	}
	name := generationOf + hex.EncodeToString(suffix[:])
	if err := os.Mkdir(filepath.Join(d, name), 0o700); err != nil {
		return nil, fmt.Errorf("creating a snapshot in %s: %w", d, err)
	}
	return &Generation{repository: repository, dir: d, name: name}, nil
}

// removeGenerations deletes every generation in d other than keep.
func removeGenerations(d, keep string) error {
	entries, err := os.ReadDir(d)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), generationOf) && e.Name() != keep {
			if err := os.RemoveAll(filepath.Join(d, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// Create opens a new file in the generation for database key's data, returning its name
// for the manifest.
func (g *Generation) Create(key string) (*Writer, string, error) {
	g.files++
	name := fmt.Sprintf("%03d.gz", g.files)
	f, err := os.OpenFile(filepath.Join(g.dir, g.name, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, "", fmt.Errorf("creating the snapshot of database %s: %w", key, err)
	}
	return &Writer{f: f, gz: gzip.NewWriter(f)}, name, nil
}

type Writer struct {
	f  *os.File
	gz *gzip.Writer
}

func (w *Writer) Write(p []byte) (int, error) { return w.gz.Write(p) }

// Close finishes the file, flushed to the disk, and returns its size.
func (w *Writer) Close() (int64, error) {
	if err := w.gz.Close(); err != nil {
		w.f.Close()
		return 0, err
	}
	if err := w.f.Sync(); err != nil {
		w.f.Close()
		return 0, err
	}
	info, err := w.f.Stat()
	if err != nil {
		w.f.Close()
		return 0, err
	}
	return info.Size(), w.f.Close()
}

// Commit makes this generation the repository's current snapshot, in one rename.
func (g *Generation) Commit(m Manifest) error {
	m.Format, m.Generation = format, g.name
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(g.dir, manifestName+".tmp")
	if err := writeSynced(tmp, append(data, '\n')); err != nil {
		return err
	}
	if err := safefile.Replace(tmp, filepath.Join(g.dir, manifestName)); err != nil {
		return err
	}
	g.done = true
	syncDir(g.dir)
	return nil
}

// RemoveReplaced removes every other generation, once Commit has made this one current.
// A failure leaves it for the next Begin.
func (g *Generation) RemoveReplaced() error {
	return removeGenerations(g.dir, g.name)
}

func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// syncDir makes a rename in dir durable. Not every filesystem can sync a directory, and
// the rename already took effect in this process's view, so a failure is ignored.
func syncDir(path string) {
	if d, err := os.Open(path); err == nil {
		_ = d.Sync()
		d.Close()
	}
}

// Abort removes this generation unless Commit made it current.
func (g *Generation) Abort() {
	if !g.done {
		_ = os.RemoveAll(filepath.Join(g.dir, g.name))
	}
}
