//go:build e2e && windows

package e2e

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/kodnificent/berth/internal/shell"
	"github.com/kodnificent/berth/internal/winsys"
	"golang.org/x/sys/windows"
)

// binSh is the sh a test hands to berth exec by its path: Git for Windows' own.
var binSh = func() string {
	sh, err := shell.Path()
	if err != nil {
		panic(err)
	}
	return sh
}()

const exe = ".exe"

func killNow(pid int) error { return winsys.Terminate(pid) }

// alive reports whether the process a script's $$ named still runs. Git for Windows'
// sh numbers its processes itself, and only its own kill knows those numbers.
func alive(pid int) bool {
	return exec.Command(binSh, "-c", "kill -0 "+strconv.Itoa(pid)).Run() == nil
}

// killScriptPid ends the process a script's $$ named, through sh's own kill, since the
// number is sh's and not Windows'.
func killScriptPid(pid int) { _ = exec.Command(binSh, "-c", "kill -9 "+strconv.Itoa(pid)).Run() }

// killScriptGroup ends the process group of the script whose $$ was pid.
func killScriptGroup(pid int) {
	_ = exec.Command(binSh, "-c", "kill -9 -- -"+strconv.Itoa(pid)+" "+strconv.Itoa(pid)).Run()
}

// lockLikeBerth takes the lock berth takes on its registry and worktree lock files.
func lockLikeBerth(f *os.File) error {
	_, err := winsys.LockFile(f, true)
	return err
}

func unlockLikeBerth(f *os.File) error { return winsys.UnlockFile(f) }

func ownGroup(*exec.Cmd) {}

// killGroupOf ends cmd and every process under it.
func killGroupOf(cmd *exec.Cmd) error {
	return exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
}

// makeExecutable lets the sh script at path be run by its name: Windows runs only
// programs, so a copy of the shim program beside it, under the same name, runs it.
func makeExecutable(path string) error {
	src, err := os.Open(shimBin)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(path+".exe", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	return dst.Close()
}

// lchtimes sets path's access and modification times to at, of a symlink itself.
func lchtimes(path string, at time.Time) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.FILE_WRITE_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	ft := windows.NsecToFiletime(at.UnixNano())
	return windows.SetFileTime(h, nil, &ft, &ft)
}

// scriptCommand runs the sh script at path as the program that started it would: on
// Windows, git and Claude Code run a script with Git for Windows' sh.
func scriptCommand(path string, args ...string) *exec.Cmd {
	return exec.Command(binSh, append([]string{path}, args...)...)
}

func addrInUse(err error) bool { return errors.Is(err, windows.WSAEADDRINUSE) }

// Git for Windows checks symlinks out as plain files, and refuses paths longer than 260
// characters, unless told otherwise.
const gitPlatformConfig = "[core]\n\tsymlinks = true\n\tlongpaths = true\n"

// endProcessesIn ends every process running in root, such as the clone and post-add
// processes a test left running and what their scripts started, since Windows can't
// delete the test's temporary directory while they have files open in it, or have a
// directory in it as their own.
func endProcessesIn(root string) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return
	}
	defer windows.CloseHandle(snap)
	e := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		pid := int(e.ProcessID)
		if pid == os.Getpid() {
			continue
		}
		dir, err := winsys.WorkingDir(pid)
		if err != nil || dir == "" {
			continue
		}
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			dir = real
		}
		if rel, err := filepath.Rel(root, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			_ = winsys.Terminate(pid)
		}
	}
}

// shPwd prints the working directory as Windows spells it, where Git for Windows' sh
// spells it /c/...
const shPwd = `cygpath -w "$(pwd -P)"`

// maxWorktreeDirName is how long a worktree's directory name may be: git for Windows
// refuses a worktree whose administrative directory path is longer than a Windows path.
const maxWorktreeDirName = 100

func expectPrivate(t *testing.T, path string, _ fs.FileInfo) {
	t.Helper()
	only, err := winsys.OnlyUserMayAccess(path)
	if err != nil {
		t.Errorf("reading who may access %s: %v", path, err)
	} else if !only {
		t.Errorf("%s grants access to someone other than the user", path)
	}
}

// blockRemovalOf keeps the directory from being removed for the rest of the test: Windows
// refuses to delete a file another process has open, where elsewhere the test would
// rely on the directory's mode.
func blockRemovalOf(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("reading %s: %d entries, %v", dir, len(entries), err)
	}
	f, err := os.Open(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
}
