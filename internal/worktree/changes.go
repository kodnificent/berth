package worktree

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LastChange is the latest modification time among what git status reports changed in
// the worktree at dir and the worktree's index in adminDir, which staging moves, and the
// time of the newest entry in its HEAD log, which a commit or a checkout adds. git
// compares tracked files against the stat data its index caches and skips ignored
// directories, so dependency trees cost nothing; only the few paths it reports are
// stat'ed here. A clean worktree reports the zero time.
func LastChange(ctx context.Context, dir, adminDir string) (time.Time, error) {
	// --no-optional-locks keeps git from refreshing the index, which would itself look
	// like a change the next time.
	cmd := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", dir, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames")
	cmd.Env = CleanEnv(os.Environ())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return time.Time{}, ctx.Err()
		}
		return time.Time{}, fmt.Errorf("git status in %s: %w: %s", dir, err, strings.TrimSpace(stderr.String()))
	}
	var latest time.Time
	count := func(at time.Time) {
		if at.After(latest) {
			latest = at
		}
	}
	later := func(path string) {
		if info, err := os.Lstat(path); err == nil {
			count(info.ModTime())
		}
	}
	later(filepath.Join(adminDir, "index"))
	count(newestReflogEntry(filepath.Join(adminDir, "logs", "HEAD")))
	for _, entry := range strings.Split(stdout.String(), "\x00") {
		if len(entry) < 4 {
			continue
		}
		later(existing(dir, filepath.FromSlash(entry[3:])))
	}
	return latest, nil
}

// existing is rel under dir, or for a deleted path the nearest directory above it that
// still exists, whose time the deletion moved.
func existing(dir, rel string) string {
	p := filepath.Join(dir, rel)
	for p != dir {
		if _, err := os.Lstat(p); err == nil {
			return p
		}
		p = filepath.Dir(p)
	}
	return dir
}

// newestReflogEntry is the time recorded on the last line of a reflog. The file's own time
// won't do: git gc and git reflog expire, run in any worktree, rewrite every worktree's
// reflog without anyone using it.
func newestReflogEntry(path string) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return time.Time{}
	}
	const tail = 4096
	if info.Size() > tail {
		if _, err := f.Seek(info.Size()-tail, io.SeekStart); err != nil {
			return time.Time{}
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, tail))
	if err != nil {
		return time.Time{}
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	// <old> <new> <name> <email> <seconds> <zone>\t<message>
	header, _, _ := strings.Cut(lines[len(lines)-1], "\t")
	fields := strings.Fields(header)
	if len(fields) < 2 {
		return time.Time{}
	}
	secs, err := strconv.ParseInt(fields[len(fields)-2], 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(secs, 0)
}
