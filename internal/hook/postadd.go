// Package hook runs the commands a project configures under [hooks] and installs the
// git hook that runs berth add when git creates a worktree.
package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kodnificent/berth/internal/lockfile"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/safefile"
	"github.com/kodnificent/berth/internal/shell"
	"github.com/kodnificent/berth/internal/worktree"
)

const (
	// Pending means post-add waits for the berth's clones before it runs.
	Pending = "pending"
	Running = "running"
	OK      = "ok"
	Failed  = "failed"
	// Skipped means a clone it waited for didn't become ready, so it never ran.
	Skipped = "skipped"
)

// tailLines and tailBytes bound the output status reports; the whole output stays in
// the log.
const (
	tailLines = 20
	tailBytes = 4096
)

// PostAdd is how a berth's post-add stands. Only the holder of the berth's post-add
// lock writes it.
type PostAdd struct {
	Command string   `json:"command"`
	Dir     string   `json:"dir"`
	Source  string   `json:"source"`
	Changed []string `json:"changed"`
	// Waits lists the clone volumes that must be ready before the command runs, and
	// WaitsDatabases the database copies.
	Waits          []string   `json:"waits,omitempty"`
	WaitsDatabases []string   `json:"waits_databases,omitempty"`
	Status         string     `json:"status"`
	ExitCode       *int       `json:"exit_code,omitempty"`
	Output         string     `json:"output,omitempty"`
	Error          string     `json:"error,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
}

func path(id, ext string) (string, error) {
	d, err := registry.StateDir()
	if err != nil {
		return "", err
	}
	d = filepath.Join(d, "hooks")
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(d, id+".post-add"+ext), nil
}

// LogPath holds the whole output of berth id's last post-add run.
func LogPath(id string) (string, error) { return path(id, ".log") }

// Load reads berth id's post-add state; ok is false when post-add never was scheduled.
func Load(id string) (p PostAdd, ok bool, err error) {
	file, err := path(id, ".json")
	if err != nil {
		return PostAdd{}, false, err
	}
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return PostAdd{}, false, nil
	}
	if err != nil {
		return PostAdd{}, false, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return PostAdd{}, false, fmt.Errorf("reading post-add state %s: %w", file, err)
	}
	return p, true, nil
}

func Save(id string, p PostAdd) error {
	file, err := path(id, ".json")
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return safefile.Replace(tmp, file)
}

// TryLock takes berth id's post-add lock without waiting. Whoever schedules post-add
// takes it and hands it to the process that runs the command, which holds it until the
// command ends, so a lock that can be taken means nothing is waiting or running. ok is
// false while something is.
func TryLock(id string) (f *os.File, ok bool, err error) {
	file, err := path(id, ".lock")
	if err != nil {
		return nil, false, err
	}
	f, err = lockfile.Open(file, true)
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

// WaitUnlocked returns once nothing of berth id's post-add is waiting or running.
func WaitUnlocked(id string) error {
	file, err := path(id, ".lock")
	if err != nil {
		return err
	}
	f, err := lockfile.Open(file, true)
	if err != nil {
		return err
	}
	defer f.Close()
	return waitLock(f)
}

// Recover marks a post-add whose process died as failed. The caller holds the lock, so
// a post-add still pending or running has no process left.
func Recover(p *PostAdd) bool {
	if p.Status != Pending && p.Status != Running {
		return false
	}
	p.Status = Failed
	p.Error = "post-add was interrupted before it finished; berth add runs it again"
	return true
}

// Live is berth id's post-add state, with one whose process died reported as failed.
func Live(id string) (p PostAdd, ok bool, err error) {
	lock, free, err := TryLock(id)
	if err != nil {
		return PostAdd{}, false, err
	}
	p, ok, err = Load(id)
	if !free || err != nil || !ok {
		if lock != nil {
			lock.Close()
		}
		return p, ok, err
	}
	defer lock.Close()
	if Recover(&p) {
		err = Save(id, p)
	}
	return p, ok, err
}

// Execute runs p's command for berth id and records how it ended. The caller holds the
// post-add lock. Output goes to the log, and its tail into p.
func Execute(id string, p *PostAdd) error {
	logPath, err := LogPath(id)
	if err != nil {
		return err
	}
	log, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer log.Close()

	now := time.Now().UTC().Truncate(time.Millisecond)
	p.Status, p.StartedAt, p.FinishedAt, p.ExitCode, p.Output, p.Error = Running, &now, nil, nil, "", ""
	if err := Save(id, *p); err != nil {
		return err
	}
	cmd, runErr := shell.Command(append(worktree.CleanEnv(os.Environ()), Env(p.Dir, p.Source, p.Changed)...), p.Command)
	if runErr == nil {
		cmd.Dir = p.Dir
		cmd.Stdout, cmd.Stderr = log, log
		runErr = cmd.Run()
	}

	done := time.Now().UTC().Truncate(time.Millisecond)
	p.FinishedAt = &done
	p.Output = Tail(logPath)
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		code := 0
		p.Status, p.ExitCode = OK, &code
	case errors.As(runErr, &exitErr):
		code := exitErr.ExitCode()
		p.Status, p.ExitCode = Failed, &code
		if code < 0 {
			p.ExitCode = nil
			p.Error = fmt.Sprintf("post-add ended with %s", exitErr.ProcessState)
		}
	default:
		p.Status, p.Error = Failed, fmt.Sprintf("starting post-add: %v", runErr)
	}
	return Save(id, *p)
}

// Env is what a berth's hook commands see on top of berth's own environment.
func Env(dir, source string, changed []string) []string {
	return []string{
		"BERTH_PATH=" + dir,
		"BERTH_SOURCE=" + source,
		"BERTH_CHANGED_FILES=" + strings.Join(changed, "\n"),
	}
}

// Tail is the last lines of the file at path, at most tailLines of them and tailBytes
// long.
func Tail(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	start := max(info.Size()-tailBytes, 0)
	buf := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return ""
	}
	buf = bytes.TrimRight(buf, "\n")
	if start > 0 {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	lines := bytes.Split(buf, []byte("\n"))
	if len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
	}
	return string(bytes.Join(lines, []byte("\n")))
}

// ChangedFiles lists the tracked files that may differ between the source and the
// berth: those the berth's working tree changes from the source's HEAD, and those the
// source's working tree changes from it. A file can be listed and still match, never
// the reverse.
func ChangedFiles(w *worktree.Worktree) []string {
	head, err := worktree.GitAt(w.Repository, "rev-parse", "--verify", "-q", "HEAD")
	if err != nil {
		return []string{}
	}
	head = strings.TrimSpace(head)
	seen := map[string]bool{}
	var out []string
	for _, run := range []func() (string, error){
		func() (string, error) { return w.Git("diff", "--name-only", "--no-relative", "-z", head) },
		func() (string, error) {
			return worktree.GitAt(w.Repository, "diff", "--name-only", "--no-relative", "-z", "HEAD")
		},
	} {
		list, err := run()
		if err != nil {
			continue
		}
		for _, f := range strings.Split(list, "\x00") {
			if f != "" && !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

// Forget deletes berth id's post-add state and log. The caller holds the post-add lock.
func Forget(id string) error {
	for _, ext := range []string{".json", ".log", ".lock"} {
		file, err := path(id, ext)
		if err != nil {
			return err
		}
		if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}
