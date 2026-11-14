package winsys

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ConfineChildren puts this process in a new job object that only it holds a handle
// to, and that ends every process in it when that handle closes. Every process it
// starts from then on joins the job, so however this process ends, taskkill /F
// included, nothing it started outlives it.
func ConfineChildren() error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("creating a job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("setting up a job object: %w", err)
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("joining a job object: %w", err)
	}
	return nil
}

// Terminate ends process pid at once, as taskkill /F does.
func Terminate(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}

// Process is an open handle to a running process, which keeps its pid from being
// given to another process until Close.
type Process struct{ h windows.Handle }

// OpenProcess opens process pid; ok is false when no process has that pid.
func OpenProcess(pid int) (p *Process, ok bool, err error) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	p = &Process{h}
	if p.Wait(0) {
		p.Close()
		return nil, false, nil
	}
	return p, true, nil
}

func (p *Process) Close() { windows.CloseHandle(p.h) }

// Wait reports whether the process ended within d.
func (p *Process) Wait(d time.Duration) bool {
	ev, err := windows.WaitForSingleObject(p.h, uint32(d.Milliseconds()))
	return err == nil && ev == windows.WAIT_OBJECT_0
}

func (p *Process) Terminate() error { return windows.TerminateProcess(p.h, 1) }

// CommandLine is the command line the process was started with.
func (p *Process) CommandLine() (string, error) {
	// A command line is at most 32767 UTF-16 units, after the string's header.
	buf := make([]byte, 1<<17)
	if err := windows.NtQueryInformationProcess(p.h, windows.ProcessCommandLineInformation, unsafe.Pointer(&buf[0]), uint32(len(buf)), nil); err != nil {
		return "", err
	}
	return (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0])).String(), nil
}

// Image is the path of the process's executable.
func (p *Process) Image() (string, error) { return imagePath(p.h) }

// Image is the base name of the executable of process pid, such as node.exe.
func Image(pid int) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	path, err := imagePath(h)
	return filepath.Base(path), err
}

func imagePath(h windows.Handle) (string, error) {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:n]), nil
}

// WorkingDir is the current directory of process pid, read from its process parameters,
// which needs the same bitness and enough rights over that process.
func WorkingDir(pid int) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	var info windows.PROCESS_BASIC_INFORMATION
	if err := windows.NtQueryInformationProcess(h, windows.ProcessBasicInformation, unsafe.Pointer(&info), uint32(unsafe.Sizeof(info)), nil); err != nil {
		return "", err
	}
	var peb windows.PEB
	if err := readMemory(h, uintptr(unsafe.Pointer(info.PebBaseAddress)), unsafe.Pointer(&peb), unsafe.Sizeof(peb)); err != nil {
		return "", err
	}
	var params windows.RTL_USER_PROCESS_PARAMETERS
	if err := readMemory(h, uintptr(unsafe.Pointer(peb.ProcessParameters)), unsafe.Pointer(&params), unsafe.Sizeof(params)); err != nil {
		return "", err
	}
	dir := params.CurrentDirectory.DosPath
	if dir.Length == 0 {
		return "", nil
	}
	buf := make([]uint16, dir.Length/2)
	if err := readMemory(h, uintptr(unsafe.Pointer(dir.Buffer)), unsafe.Pointer(&buf[0]), uintptr(dir.Length)); err != nil {
		return "", err
	}
	path := windows.UTF16ToString(buf)
	if len(path) > 3 {
		path = strings.TrimSuffix(path, `\`)
	}
	return path, nil
}

func readMemory(h windows.Handle, addr uintptr, into unsafe.Pointer, size uintptr) error {
	var n uintptr
	if err := windows.ReadProcessMemory(h, addr, (*byte)(into), size, &n); err != nil {
		return err
	}
	if n != size {
		return errors.New("short read of another process's memory")
	}
	return nil
}

// FreeSpace is the disk space under dir that this user may still write.
func FreeSpace(dir string) (int64, bool) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, false
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return 0, false
	}
	return int64(free), true
}
