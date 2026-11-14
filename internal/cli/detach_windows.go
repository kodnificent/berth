package cli

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/kodnificent/berth/internal/lockfile"
	"github.com/kodnificent/berth/internal/winsys"
	"golang.org/x/sys/windows"
)

// detach starts cmd with a console of its own that has no window, so it outlives add
// and the terminal or hook that ran it, and what it starts opens no window either.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP}
}

// handOffEnv names the passable lock and the event that says it was taken over: Windows
// passes a child no descriptors past the standard three, and a lock can't pass with one.
const handOffEnv = "BERTH_LOCK_HANDOFF"

type lockPass struct{ taken windows.Handle }

// passLock lets go of lock's second byte for cmd to take over, and tells cmd where to
// find it and the event to set once it has. lock's first byte stays held until wait
// returns, so nothing else can take the lock in between.
func passLock(cmd *exec.Cmd, lock *os.File) (lockPass, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return lockPass{}, err
	}
	name := `Local\berth-lock-` + hex.EncodeToString(b)
	taken, err := windows.CreateEvent(nil, 1, 0, windows.StringToUTF16Ptr(name))
	if err != nil {
		return lockPass{}, err
	}
	if err := winsys.HandOff(lock); err != nil {
		windows.CloseHandle(taken)
		return lockPass{}, err
	}
	cmd.Env = append(os.Environ(), handOffEnv+"="+name+"|"+lock.Name())
	return lockPass{taken}, nil
}

// wait returns once p holds the lock, or fails when p ends first.
func (l lockPass) wait(p *os.Process) error {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(p.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForMultipleObjects([]windows.Handle{l.taken, h}, false, windows.INFINITE)
	switch {
	case err != nil:
		return err
	case ev == windows.WAIT_OBJECT_0:
		return nil
	default:
		return errors.New("it ended before taking over the lock")
	}
}

func (l lockPass) close() { windows.CloseHandle(l.taken) }

// held keeps the lock taken over open, and so held, for as long as this process lives.
var takenOver *os.File

// holdInheritedLock takes over the lock add passed down and tells add it has. When add
// is gone by then, so is its event, and the lock may since have been recovered by
// another berth, so it reports false and this process does nothing.
func holdInheritedLock() bool {
	v, ok := os.LookupEnv(handOffEnv)
	os.Unsetenv(handOffEnv)
	name, path, found := strings.Cut(v, "|")
	if !ok || !found {
		return false
	}
	f, err := lockfile.Open(path, false)
	if err != nil {
		return false
	}
	if err := winsys.TakeOver(f); err != nil {
		f.Close()
		return false
	}
	taken, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, windows.StringToUTF16Ptr(name))
	if err != nil {
		f.Close()
		return false
	}
	defer windows.CloseHandle(taken)
	if err := windows.SetEvent(taken); err != nil {
		f.Close()
		return false
	}
	takenOver = f
	return true
}

// confineChildren puts the clone process in a job object, so what it starts, a
// command's script and everything the script starts included, ends when it does.
func confineChildren() error { return winsys.ConfineChildren() }

func freeSpace(dir string) (int64, bool) { return winsys.FreeSpace(dir) }

// killClone ends berth id's clone process, pid, and with it, through its job object,
// everything it started. The pid comes from a file that outlives the process, and
// Windows soon gives a dead process's pid to another, so a process that isn't berth's
// executable running the clone of id is left alone. The handle checked is the handle
// ended, so the pid can't change hands in between.
func killClone(id string, pid int) error {
	p, ok, err := winsys.OpenProcess(pid)
	if err != nil || !ok {
		return err
	}
	defer p.Close()
	if !isClone(p, id) {
		return nil
	}
	return p.Terminate()
}

func isClone(p *winsys.Process, id string) bool {
	image, err := p.Image()
	if err != nil {
		return false
	}
	self, err := os.Executable()
	if err != nil {
		return false
	}
	a, errA := os.Stat(image)
	b, errB := os.Stat(self)
	if errA != nil || errB != nil || !os.SameFile(a, b) {
		return false
	}
	line, err := p.CommandLine()
	return err == nil && strings.HasSuffix(line, " "+windows.EscapeArg(cloneCommand)+" "+windows.EscapeArg(id))
}
