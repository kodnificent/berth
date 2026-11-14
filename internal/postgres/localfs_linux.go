package postgres

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const (
	btrfsMagic = 0x9123683e
	xfsMagic   = 0x58465342
)

// localDataDir is the data directory of the server berth's session pid runs in, with its
// filesystem: btrfs, xfs or other. ok is false unless berth can see that directory here
// and it is the one the server on port runs from: a server in a container has pids berth
// can't see, and one run by another user hides its directory.
func localDataDir(pid, port int) (dir, fs string, ok bool) {
	dir, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
	if err != nil {
		return "", "", false
	}
	data, err := os.ReadFile(filepath.Join(dir, "postmaster.pid"))
	if err != nil {
		return "", "", false
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) < 4 || strings.TrimSpace(lines[3]) != strconv.Itoa(port) {
		return "", "", false
	}
	recorded, err := os.Stat(strings.TrimSpace(lines[1]))
	here, err2 := os.Stat(dir)
	if err != nil || err2 != nil || !os.SameFile(recorded, here) {
		return "", "", false
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return "", "", false
	}
	switch uint32(st.Type) {
	case btrfsMagic:
		return dir, btrfs, true
	case xfsMagic:
		return dir, xfs, true
	}
	return dir, otherFS, true
}

// freeBytes is the free disk where dir is, for anyone.
func freeBytes(dir string) (int64, bool) {
	if dir == "" {
		return 0, false
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true
}
