// Package sqlite copies a SQLite database that another process may be writing, as one
// point in time, sharing its blocks with the original where the filesystem can. It
// never writes to the original: it takes SQLite's own file locks for reading and copies
// the files while it holds them.
package sqlite

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Strategies a copy can take.
const (
	CoW  = "cow"
	Copy = "copy"
)

// busyWait bounds how long a copy waits for main's writers to let it read.
const busyWait = 30 * time.Second

// Copied says how a copy went.
type Copied struct {
	Strategy string
	// Bytes is the size of the files copied: the database and its WAL or journal.
	Bytes int64
	// Held is how long main's writers had to wait for the copy. It is zero for a WAL
	// database, whose writers never wait for a reader.
	Held time.Duration
}

// Options adjust a copy.
type Options struct {
	// Slow is called once before a copy that is not copy-on-write begins, with the
	// database file's size and whether its writers wait for the copy.
	Slow func(size int64, holdsWriters bool)
	// Hold keeps the locks this long after the copy, so a test can watch them.
	Hold time.Duration
	// NoCoW and Pause let a test force a byte copy and stop it after the first chunk of
	// each file, so main can act in the middle of one.
	NoCoW bool
	Pause time.Duration
}

// chunk is how much of a file a byte copy reads at a time.
const chunk = 1 << 20

// pendingByte is the first of SQLite's lock bytes, the offset of its lock-byte page.
const pendingByte = 0x40000000

// lockBytesRead are PENDING and RESERVED, the only bytes of the database a writer of
// main's can hold exclusively while this copy holds its shared lock, and Windows fails a
// read of a byte someone else has exclusively locked. They sit on SQLite's lock page,
// which it never writes or reads, so writing zeros in their place changes nothing the
// copy's own SQLite would see.
const lockBytesRead = 2

// Sidecars are the files SQLite keeps beside the database at path. They belong to that
// database: a WAL left beside another copy of it would be replayed into that copy.
func Sidecars(path string) []string {
	return []string{path + "-wal", path + "-shm", path + "-journal"}
}

var magic = []byte("SQLite format 3\x00")

// Clone writes to dst, which must not exist, a copy of the database at src as it stood at
// one commit. It opens nothing of src's for writing and creates nothing beside it.
//
// A rollback-journal database is copied under a SHARED lock, which no writer can commit
// past, with its journal when it has one: a hot journal left by a crash is rolled back
// by the copy's first open, and one a writer is filling holds the pages the file still
// has. A WAL database is copied with its WAL under SHARED and read locks on the
// wal-index's first two reader slots: every checkpoint needs the first exclusively
// before it writes to the database file, and starting the WAL again from the top or
// truncating it needs the second, so the file holds still while the WAL only grows.
//
// A symlink at src is resolved first, since SQLite keeps the WAL and its locks beside
// the file it leads to.
func Clone(src, dst string, opts Options) (c Copied, err error) {
	src, err = filepath.EvalSymlinks(src)
	if err != nil {
		return c, err
	}
	for range 10 {
		var retry bool
		c, retry, err = clone(src, dst, opts)
		if !retry {
			return c, err
		}
	}
	return Copied{}, fmt.Errorf("main's %s kept changing under every copy berth made of it; try again when its app is quieter", src)
}

func clone(src, dst string, opts Options) (c Copied, retry bool, err error) {
	// Every descriptor of src's files stays open until the copy is done: closing any
	// descriptor of a file drops every POSIX lock the process holds on it.
	f, err := os.Open(src)
	if err != nil {
		return c, false, err
	}
	defer f.Close()
	if err := lockShared(f); err != nil {
		return c, false, fmt.Errorf("main's %s: %w", src, err)
	}
	header := make([]byte, 100)
	n, _ := f.ReadAt(header, 0)
	if n > 0 && (n < len(header) || !bytes.HasPrefix(header, magic)) {
		return c, false, fmt.Errorf("%s is not a SQLite database", src)
	}
	wal := header[18] == 2 || header[19] == 2

	var shmMissing bool
	if wal {
		shm, err := os.Open(src + "-shm")
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// No connection has the WAL open, so nothing can checkpoint into the file or
			// rewrite the WAL until one opens it, which creates the -shm; that is checked
			// for after the copy.
			shmMissing = true
		case err != nil:
			return c, false, err
		default:
			defer shm.Close()
			if err := lockReader(shm); err != nil {
				return c, false, fmt.Errorf("main's %s: %w", src, err)
			}
		}
	}

	announced := false
	announce := func() {
		if opts.Slow != nil && !announced {
			size := int64(0)
			if info, err := f.Stat(); err == nil {
				size = info.Size()
			}
			opts.Slow(size, !wal)
		}
		announced = true
	}
	sidecar := src + "-journal"
	if wal {
		sidecar = src + "-wal"
	}
	defer func() {
		if err != nil || retry {
			os.Remove(dst)
			os.Remove(dst + "-wal")
			os.Remove(dst + "-journal")
		}
	}()

	walHeader := header32(sidecar)
	start := time.Now()
	size, cowed, err := cloneFile(f, dst, true, announce, opts)
	if err != nil {
		return c, false, err
	}
	c.Bytes, c.Strategy = size, CoW
	if !cowed {
		c.Strategy = Copy
	}
	size, cowed, err = cloneSidecar(sidecar, dst+sidecar[len(src):], announce, opts)
	if err != nil {
		return c, false, err
	}
	c.Bytes += size
	if size > 0 && !cowed {
		c.Strategy = Copy
	}
	if !wal {
		c.Held = time.Since(start)
	}
	if shmMissing {
		if _, err := os.Lstat(src + "-shm"); err == nil {
			return c, true, nil
		}
	}
	// The locks keep the WAL from being started again or truncated; a header that
	// changed anyway means the copy can't be trusted.
	if wal && !bytes.Equal(header32(sidecar), walHeader) {
		return c, true, nil
	}
	time.Sleep(opts.Hold)
	return c, false, nil
}

// header32 is the first 32 bytes of the file at path, which for a WAL hold its salts, or
// nil when it has none.
func header32(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	b := make([]byte, 32)
	n, _ := f.ReadAt(b, 0)
	return b[:n]
}

func cloneSidecar(src, dst string, slow func(), opts Options) (int64, bool, error) {
	f, err := os.Open(src)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, true, nil
	}
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil {
		return 0, false, err
	} else if info.Size() == 0 {
		return 0, true, nil
	}
	return cloneFile(f, dst, false, slow, opts)
}

// cloneFile copies the open file f to dst, copy-on-write when the filesystem can. A
// database file is read differently from a sidecar on Windows, where a writer's locks
// on it are mandatory.
func cloneFile(f *os.File, dst string, database bool, slow func(), opts Options) (n int64, cowed bool, err error) {
	info, err := f.Stat()
	if err != nil {
		return 0, false, err
	}
	if !opts.NoCoW && cow(f, dst) == nil {
		return info.Size(), true, nil
	}
	slow()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return 0, false, err
	}
	var in io.Reader = io.NewSectionReader(f, 0, info.Size())
	if database {
		in = databaseBytes(f, info.Size())
	}
	n, err = io.CopyN(out, in, chunk)
	if err == nil {
		time.Sleep(opts.Pause)
		var rest int64
		rest, err = io.Copy(out, in)
		n += rest
	} else if err == io.EOF {
		err = nil
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(dst)
		return 0, false, err
	}
	return n, false, os.Chtimes(dst, info.ModTime(), info.ModTime())
}

func databaseBytes(f *os.File, size int64) io.Reader {
	if runtime.GOOS != "windows" || size <= pendingByte {
		return io.NewSectionReader(f, 0, size)
	}
	return io.MultiReader(
		io.NewSectionReader(f, 0, pendingByte),
		bytes.NewReader(make([]byte, min(lockBytesRead, size-pendingByte))),
		io.NewSectionReader(f, pendingByte+lockBytesRead, max(size-pendingByte-lockBytesRead, 0)),
	)
}
