package cli

import (
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
	"github.com/kodnificent/berth/internal/bytesize"
	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/sqlite"
	"github.com/kodnificent/berth/internal/volume"
	"github.com/kodnificent/berth/internal/worktree"
)

const kindSQLite = "sqlite"

// Test hooks: testSQLiteHold keeps main's database locked that long after each copy, so
// tests can check that main's writers and checkpoints really wait for it; the others
// force a byte copy and pause it after its first chunk, so main can act mid-copy.
const (
	testSQLiteHold  = "BERTH_TEST_SQLITE_HOLD"
	testSQLiteNoCoW = "BERTH_TEST_SQLITE_NO_COW"
	testSQLitePause = "BERTH_TEST_SQLITE_PAUSE"
)

// sqliteCopier gives a berth its copies of main's SQLite databases, recording each in
// rec before writing it.
type sqliteCopier struct {
	stderr  io.Writer
	w       *worktree.Worktree
	cfg     *config.Config
	rec     *registry.Record
	tracked map[string]bool
	// reset makes the berth's copies again rather than keeping those it has.
	reset  bool
	placed []string
	gone   []discarded
}

// copySQLite gives the berth a copy of each [state.sqlite] file it has none of yet. A
// copy that fails is reported and recorded as failed rather than returned, as a volume
// clone is: the berth is usable without it, and status says why.
func copySQLite(stderr io.Writer, w *worktree.Worktree, cfg *config.Config, rec *registry.Record) error {
	files := cfg.SQLiteFiles()
	if len(files) == 0 {
		return nil
	}
	s := &sqliteCopier{stderr: stderr, w: w, cfg: cfg, rec: rec, tracked: trackedFiles(w.Path)}
	for _, rel := range files {
		if err := s.copy(rel); err != nil {
			return err
		}
	}
	return exclude(w, s.placed)
}

// resetSQLite makes each of the berth's copies again from main, and copies any listed
// file the berth has none of. gone lists the copies it discarded.
func resetSQLite(stderr io.Writer, w *worktree.Worktree, cfg *config.Config, rec *registry.Record) ([]discarded, error) {
	files := cfg.SQLiteFiles()
	for _, e := range rec.SQLite {
		if e.Own && !slices.Contains(files, e.Path) {
			files = append(files, e.Path)
		}
	}
	if len(files) == 0 {
		return nil, nil
	}
	s := &sqliteCopier{stderr: stderr, w: w, cfg: cfg, rec: rec, tracked: trackedFiles(w.Path), reset: true}
	for _, rel := range files {
		if err := s.copy(rel); err != nil {
			return s.gone, err
		}
	}
	return s.gone, exclude(w, s.placed)
}

// sqliteDiscards is the berth's copies reset would discard: those berth wrote that are
// still there.
func sqliteDiscards(rec registry.Record, dir string) []discarded {
	tracked := trackedFiles(dir)
	var out []discarded
	for _, e := range rec.SQLite {
		if e.Own && !tracked[filepath.ToSlash(e.Path)] && realDirs(dir, filepath.Dir(e.Path)) == nil && sqliteExists(filepath.Join(dir, e.Path)) {
			out = append(out, discarded{Kind: kindSQLite, Name: e.Path, Target: e.Path})
		}
	}
	return out
}

func (s *sqliteCopier) entry(rel string) *registry.SQLiteFile {
	for i := range s.rec.SQLite {
		if s.rec.SQLite[i].Path == rel {
			return &s.rec.SQLite[i]
		}
	}
	s.rec.SQLite = append(s.rec.SQLite, registry.SQLiteFile{Path: rel})
	return &s.rec.SQLite[len(s.rec.SQLite)-1]
}

func (s *sqliteCopier) copy(rel string) error {
	e := s.entry(rel)
	if e.Own && e.Status == volume.Ready && !s.reset {
		return nil
	}
	fail := func(format string, args ...any) {
		e.Status, e.Error, e.Strategy, e.Bytes, e.Seconds, e.SourceMissing = volume.Failed, fmt.Sprintf(format, args...), "", 0, nil, false
		fmt.Fprintf(s.stderr, "berth: cannot copy SQLite %s: %s\n", rel, e.Error)
	}
	src, dst := filepath.Join(s.w.Repository, rel), filepath.Join(s.w.Path, rel)

	if e.Own {
		if s.tracked[filepath.ToSlash(rel)] {
			e.Own = false
		} else {
			// Saved before the old copy goes, so a reset that dies from here on leaves the
			// entry cloning, which status reports as failed once nothing holds the worktree.
			e.Status, e.Error = volume.Cloning, ""
			if err := saveRecord(*s.rec); err != nil {
				return err
			}
			existed := sqliteExists(dst)
			if err := removeSQLite(s.w.Path, rel, s.tracked); err != nil {
				fail("removing the berth's copy to make it again: %v", err)
				return nil
			}
			if existed {
				s.gone = append(s.gone, discarded{Kind: kindSQLite, Name: rel, Target: rel})
			}
		}
	}

	info, err := os.Stat(src)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		*e = registry.SQLiteFile{Path: rel, Dirs: e.Dirs, Status: volume.Ready, SourceMissing: true}
		fmt.Fprintf(s.stderr, "berth: main has no %s, which [state.sqlite] files lists, so the berth gets none\n", rel)
		return nil
	case err != nil:
		fail("%v", err)
		return nil
	case !info.Mode().IsRegular():
		fail("main's %s is not a file", src)
		return nil
	}
	if err := realDirs(s.w.Path, filepath.Dir(rel)); err != nil {
		fail("%v; take it out of the way and run this again", err)
		return nil
	}
	if !e.Own && sqliteExists(dst) {
		fail("the berth already has %s or SQLite's files beside it, which berth didn't copy, so berth leaves them; delete them and run berth %s again", rel, s.verb())
		return nil
	}

	dirs, err := mkdirsFor(s.w.Path, rel)
	e.Dirs = union(e.Dirs, dirs)
	if err != nil {
		fail("%v", err)
		return nil
	}
	e.Own, e.Status, e.Error, e.SourceMissing = true, volume.Cloning, "", false
	// Saved before the copy is written, so a kill from here on never leaves a file berth
	// wrote but doesn't know is its own.
	if err := saveRecord(*s.rec); err != nil {
		return err
	}
	start := time.Now()
	opts := sqlite.Options{Slow: func(size int64, holdsWriters bool) { s.announce(rel, size, holdsWriters) }, NoCoW: os.Getenv(testSQLiteNoCoW) != ""}
	opts.Hold, _ = time.ParseDuration(os.Getenv(testSQLiteHold))
	opts.Pause, _ = time.ParseDuration(os.Getenv(testSQLitePause))
	c, err := sqlite.Clone(src, dst, opts)
	if err != nil {
		fail("%v", err)
		return nil
	}
	secs := time.Since(start).Seconds()
	e.Strategy, e.Status, e.Bytes, e.Seconds = c.Strategy, volume.Ready, c.Bytes, &secs
	s.placed = append(s.placed, rel)
	note := ""
	if c.Held > 0 {
		note = fmt.Sprintf("; main's writes to it waited %s", c.Held.Round(time.Millisecond))
	}
	fmt.Fprintf(s.stderr, "berth: copied SQLite %s (%s, %s) in %s%s\n", rel, c.Strategy, bytesize.Format(c.Bytes), time.Since(start).Round(time.Millisecond), note)
	return nil
}

func (s *sqliteCopier) verb() string {
	if s.reset {
		return "reset"
	}
	return "add"
}

// announce says, before a copy that isn't copy-on-write begins, that it reads every
// byte, and what main's app meets meanwhile.
func (s *sqliteCopier) announce(rel string, size int64, holdsWriters bool) {
	wait := "main's app carries on meanwhile"
	if holdsWriters {
		wait = "until it ends, main's writes to it, and reads queued behind a waiting write, wait and then fail with \"database is locked\" once their busy timeout passes"
	}
	fmt.Fprintf(s.stderr, "berth: copying main's %s (%s) byte for byte, since its filesystem has no copy-on-write; %s\n", rel, bytesize.Format(size), wait)
	if free, ok := freeSpace(filepath.Join(s.w.Path, filepath.Dir(rel))); ok {
		if left, min := free-size, s.cfg.MinFree(); left < min {
			fmt.Fprintf(s.stderr, "berth: copying SQLite %s (%s) would leave %s of free disk, less than the %s that [clone] min_free asks for\n",
				rel, bytesize.Format(size), bytesize.Format(max(left, 0)), bytesize.Format(min))
		}
	}
}

// mkdirsFor creates rel's missing parent directories under root and returns those it
// created, outermost first.
func mkdirsFor(root, rel string) ([]string, error) {
	var missing []string
	for dir := filepath.Dir(rel); dir != "."; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(root, dir)); err == nil {
			break
		}
		missing = append([]string{dir}, missing...)
	}
	return missing, os.MkdirAll(filepath.Join(root, filepath.Dir(rel)), 0o755)
}

// sqliteFiles is the database at path and the files SQLite keeps beside it.
func sqliteFiles(path string) []string {
	return append([]string{path}, sqlite.Sidecars(path)...)
}

func sqliteExists(path string) bool {
	for _, p := range sqliteFiles(path) {
		if _, err := os.Lstat(p); err == nil {
			return true
		}
	}
	return false
}

// removeSQLite deletes the berth's copy at rel under root and the files SQLite keeps
// beside it, leaving any git tracks.
func removeSQLite(root, rel string, tracked map[string]bool) error {
	if err := realDirs(root, filepath.Dir(rel)); err != nil {
		return fmt.Errorf("%v; take it out of the way and run this again", err)
	}
	for _, p := range sqliteFiles(rel) {
		if tracked[filepath.ToSlash(p)] {
			continue
		}
		if err := os.Remove(filepath.Join(root, p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// removeSQLiteDirs deletes the directories berth created for the berth's copies that are
// now empty, innermost first, so nothing anyone put in them is ever taken.
func removeSQLiteDirs(root string, entries []registry.SQLiteFile) {
	var dirs []string
	for _, e := range entries {
		dirs = union(dirs, e.Dirs)
	}
	slices.SortFunc(dirs, func(a, b string) int { return len(b) - len(a) })
	for _, d := range dirs {
		if realDirs(root, d) == nil {
			_ = os.Remove(filepath.Join(root, d))
		}
	}
}

// realDirs refuses dir, relative to root, unless each part of it that exists is a real
// directory. A part that has become a symlink, as a branch tracking one can make it,
// may lead into main, and nothing is ever deleted or written through it.
func realDirs(root, dir string) error {
	if dir == "." {
		return nil
	}
	if !filepath.IsLocal(dir) {
		return fmt.Errorf("%s leads outside the worktree, so berth deletes and writes nothing there", dir)
	}
	path := root
	for _, part := range strings.Split(filepath.Clean(dir), string(filepath.Separator)) {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			return fmt.Errorf("%s is now a symbolic link or junction rather than the directory berth copied into, so berth deletes and writes nothing through it", path)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is now a file rather than the directory berth copied into, so berth deletes and writes nothing there", path)
		}
	}
	return nil
}

func trackedFiles(dir string) map[string]bool {
	tracked := map[string]bool{}
	if out, err := worktree.GitAt(dir, "ls-files", "-z"); err == nil {
		for _, f := range strings.Split(out, "\x00") {
			if f != "" {
				tracked[f] = true
			}
		}
	}
	return tracked
}

// sqliteEntries is the berth's SQLite files for status. A copy still recorded as under
// way while no berth command holds the worktree was interrupted, and reads as failed.
func sqliteEntries(rec registry.Record) []stateJSON {
	var out []stateJSON
	busy := sqliteCopying(rec) && registry.WorktreeBusy(rec.AdminDir)
	for _, e := range rec.SQLite {
		j := stateJSON{Name: e.Path, Kind: kindSQLite, Source: filepath.Join(rec.Repository, e.Path), Target: filepath.Join(rec.Worktree, e.Path),
			Strategy: e.Strategy, Status: e.Status, Bytes: e.Bytes, Seconds: e.Seconds, SourceMissing: e.SourceMissing, Error: e.Error}
		if e.Status == volume.Cloning && !busy {
			j.Status, j.Error = volume.Failed, "the copy was interrupted; run berth add again"
		}
		out = append(out, j)
	}
	return out
}

func sqliteCopying(rec registry.Record) bool {
	return slices.ContainsFunc(rec.SQLite, func(e registry.SQLiteFile) bool { return e.Status == volume.Cloning })
}

// sqliteFailed is add --wait's failure when a SQLite file could not be copied.
func sqliteFailed(rec registry.Record) error {
	var failed []string
	for _, e := range rec.SQLite {
		if e.Status == volume.Failed {
			failed = append(failed, fmt.Sprintf("%s: %s", e.Path, e.Error))
		}
	}
	if len(failed) > 0 {
		return apperr.New(apperr.StateCloneFailed, "copying failed for SQLite %s", strings.Join(failed, "; SQLite "))
	}
	return nil
}
