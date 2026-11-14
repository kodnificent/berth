package cli

import (
	"bufio"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/mysql"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/volume"
	"github.com/kodnificent/berth/internal/worktree"
)

// dbRecord is the record of database key, whose seed file or command the clone process
// runs from.
func dbRecord(rec registry.Record, key string) (registry.Database, error) {
	for _, db := range databases(rec) {
		if db.Key == key {
			return db, nil
		}
	}
	return registry.Database{}, fmt.Errorf("berth %s has no record of database %s", rec.ID, key)
}

// refreshDatabaseSources points rec's seed-file and command databases at what the config
// says now, keeping the strategy recorded, so reset loads the file or script the config
// names instead of the one from when add last ran.
func refreshDatabaseSources(rec *registry.Record, cfg *config.Config) {
	host, table := cfg.HostDB()
	if rec.DB() == nil || host == nil || engineFor(*rec).Table != table {
		return
	}
	for i := range rec.DB().Databases {
		db := &rec.DB().Databases[i]
		if db.Strategy != config.File && db.Strategy != config.Command {
			continue
		}
		entry := host.Entry(db.Key)
		if entry.Strategy == db.Strategy {
			db.File, db.Command = entry.File, entry.Command
		}
	}
}

// countingReader counts the bytes read through it, for how far a load is.
type countingReader struct {
	r io.Reader
	n atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// seedPath is seed file path, relative to the main worktree unless absolute.
func seedPath(rec registry.Record, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(rec.Repository, path)
}

// openSeed opens seed file path and returns its SQL, gunzipped when its first bytes say
// gzip, with what counts its bytes and its size on disk.
func openSeed(rec registry.Record, path string) (io.Reader, *countingReader, int64, func() error, error) {
	f, err := os.Open(seedPath(rec, path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, 0, nil, fmt.Errorf("seed file %s doesn't exist in the main worktree", path)
	}
	if err != nil {
		return nil, nil, 0, nil, fmt.Errorf("opening seed file %s: %v", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, 0, nil, fmt.Errorf("opening seed file %s: %v", path, err)
	}
	counter := &countingReader{r: f}
	br := bufio.NewReaderSize(counter, 1<<20)
	var r io.Reader = br
	if head, _ := br.Peek(2); len(head) == 2 && head[0] == 0x1f && head[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			f.Close()
			return nil, nil, 0, nil, fmt.Errorf("seed file %s: it starts as gzip but can't be read: %v", path, err)
		}
		r = gz
	}
	return r, counter, info.Size(), f.Close, nil
}

// seedDatabase loads e's seed file into its target.
func seedDatabase(rec registry.Record, server mysql.Server, login mysql.Login, e *volume.Entry, delay time.Duration, save func()) error {
	db, err := dbRecord(rec, e.Key)
	if err != nil {
		return err
	}
	r, counter, size, closeFile, err := openSeed(rec, db.File)
	if err != nil {
		return err
	}
	defer closeFile()
	e.Total, e.Copied, e.Notes = size, 0, nil
	var lastSave time.Time
	out, err := mysql.Seed(server, login, e.Target, r, mysql.SeedOptions{
		Progress: func() {
			e.Copied = counter.n.Load()
			if time.Since(lastSave) >= progressEvery {
				save()
				lastSave = time.Now()
			}
		},
	})
	if err != nil {
		return fmt.Errorf("seed file %s: %w", db.File, err)
	}
	if delay > 0 {
		save()
		time.Sleep(delay)
	}
	e.Bytes, e.Copied = out.Bytes, 0
	return nil
}

func scriptMark() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "berth-command-" + hex.EncodeToString(b), nil
}

// commandDatabase drops and creates e's target, then runs the database's command with
// the server's settings in its environment, its output in a log status names. Its exit
// status decides ready or failed.
func commandDatabase(rec registry.Record, server mysql.Server, login mysql.Login, e *volume.Entry, delay time.Duration, save func()) error {
	db, err := dbRecord(rec, e.Key)
	if err != nil {
		return err
	}
	engine := engineFor(rec)
	conn, err := engine.Open(server, login)
	if err != nil {
		return err
	}
	if err := conn.Drop(e.Target); err != nil {
		conn.Close()
		return err
	}
	if err := conn.Create(e.Target); err != nil {
		conn.Close()
		return err
	}
	conn.Close()
	logPath, err := volume.CommandLogPath(rec.ID, e.Key)
	if err != nil {
		return err
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	e.Log, e.Copied, e.Total = logPath, 0, 0
	save()
	gate, release, err := os.Pipe()
	if err != nil {
		return err
	}
	defer release.Close()
	mark, err := scriptMark()
	if err != nil {
		gate.Close()
		return err
	}
	cmd, err := gatedScript(append(worktree.CleanEnv(os.Environ()),
		"BERTH_SOURCE_DB="+e.Source,
		"BERTH_TARGET_DB="+e.Target,
		"BERTH_DB_HOST="+server.Host,
		"BERTH_DB_PORT="+strconv.Itoa(server.Port),
		"BERTH_DB_USER="+login.User,
		"BERTH_DB_PASSWORD="+login.Password,
	), gate, mark, db.Command)
	if err != nil {
		gate.Close()
		return fmt.Errorf("starting the command: %v", err)
	}
	cmd.Dir = rec.Repository
	cmd.Stdout, cmd.Stderr = log, log
	err = cmd.Start()
	gate.Close()
	if err != nil {
		return fmt.Errorf("starting the command: %v", err)
	}
	e.ScriptPID, e.ScriptMark = cmd.Process.Pid, mark
	save()
	if _, err := release.WriteString("go\n"); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("starting the command: %v", err)
	}
	release.Close()
	runErr := cmd.Wait()
	e.ScriptPID, e.ScriptMark = 0, ""
	if delay > 0 {
		save()
		time.Sleep(delay)
	}
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
	case errors.As(runErr, &exitErr):
		return fmt.Errorf("the command failed (%s); its output is in %s", exitErr.ProcessState, logPath)
	default:
		return fmt.Errorf("running the command: %v", runErr)
	}
	conn, err = engine.Open(server, login)
	if err != nil {
		return err
	}
	defer conn.Close()
	if n, err := conn.Size(e.Target); err == nil {
		e.Bytes = n
	}
	return nil
}
