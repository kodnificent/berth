//go:build e2e && windows

package e2e

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func volumeFilesystem(t *testing.T, path string) string {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	root := make([]uint16, windows.MAX_PATH+1)
	if err := windows.GetVolumePathName(p, &root[0], uint32(len(root))); err != nil {
		t.Fatalf("GetVolumePathName %s: %v", path, err)
	}
	name := make([]uint16, windows.MAX_PATH+1)
	if err := windows.GetVolumeInformation(&root[0], nil, 0, nil, nil, nil, &name[0], uint32(len(name))); err != nil {
		t.Fatalf("GetVolumeInformation %s: %v", path, err)
	}
	return windows.UTF16ToString(name)
}

// Two files can only hold the same logical cluster when the volume stores that data once.
func clusterNumbers(t *testing.T, path string) []string {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	defer windows.CloseHandle(h)
	// Data still in the cache has no clusters yet.
	if err := windows.FlushFileBuffers(h); err != nil {
		t.Fatalf("flushing %s: %v", path, err)
	}

	const sparse = -1
	var lcns []string
	var vcn int64
	for {
		// RETRIEVAL_POINTERS_BUFFER is 8-byte aligned: the extent count, the starting VCN,
		// then each extent's next VCN and LCN.
		out := make([]int64, 1<<13)
		var n uint32
		err := windows.DeviceIoControl(h, windows.FSCTL_GET_RETRIEVAL_POINTERS,
			(*byte)(unsafe.Pointer(&vcn)), uint32(unsafe.Sizeof(vcn)),
			(*byte)(unsafe.Pointer(&out[0])), uint32(len(out)*8), &n, nil)
		if err != nil && !errors.Is(err, windows.ERROR_MORE_DATA) {
			if errors.Is(err, windows.ERROR_HANDLE_EOF) {
				break
			}
			t.Fatalf("FSCTL_GET_RETRIEVAL_POINTERS %s: %v", path, err)
		}
		count := int(uint32(out[0]))
		for i := range count {
			if lcn := out[3+2*i]; lcn != sparse {
				lcns = append(lcns, strconv.FormatInt(lcn, 10))
			}
		}
		if err == nil || count == 0 {
			break
		}
		vcn = out[2+2*(count-1)]
	}
	if len(lcns) == 0 {
		t.Fatalf("Windows reports no clusters for %s", path)
	}
	return lcns
}

var createRestrictedToken = windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken")

func symlinkless(t *testing.T) *syscall.SysProcAttr {
	t.Helper()
	var own windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ALL_ACCESS, &own); err != nil {
		t.Fatalf("OpenProcessToken: %v", err)
	}
	defer own.Close()
	name, err := windows.UTF16PtrFromString("SeCreateSymbolicLinkPrivilege")
	if err != nil {
		t.Fatal(err)
	}
	var drop windows.LUIDAndAttributes
	if err := windows.LookupPrivilegeValue(nil, name, &drop.Luid); err != nil {
		t.Fatalf("LookupPrivilegeValue: %v", err)
	}
	var restricted windows.Token
	// Deleted, not disabled: the process and all it starts can't enable it again, like a
	// user who isn't an administrator.
	ok, _, callErr := createRestrictedToken.Call(uintptr(own), 0, 0, 0, 1, uintptr(unsafe.Pointer(&drop)), 0, 0, uintptr(unsafe.Pointer(&restricted)))
	if ok == 0 {
		t.Fatalf("CreateRestrictedToken: %v", callErr)
	}
	t.Cleanup(func() { restricted.Close() })
	return &syscall.SysProcAttr{Token: syscall.Token(restricted)}
}

func mayMakeSymlinks(t *testing.T, attr *syscall.SysProcAttr) bool {
	t.Helper()
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	cmd := exec.Command("cmd", "/c", "mklink", link, filepath.Join(dir, "target"))
	cmd.SysProcAttr = attr
	err := cmd.Run()
	var refused *exec.ExitError
	if err != nil && !errors.As(err, &refused) {
		t.Fatalf("starting a process without the symlink privilege: %v", err)
	}
	_, statErr := os.Lstat(link)
	return statErr == nil
}

// turnOffDeveloperMode exists because Developer Mode lets a process make symlinks with
// no privilege at all, and GitHub's Windows images turn it on, so deleting the privilege
// alone would leave nothing to fail. Only CI changes this machine-wide setting.
func turnOffDeveloperMode(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") == "" {
		t.Skip("Developer Mode is on here, so nothing stops a symlink; turn it off to run this test")
	}
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\AppModelUnlock`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		t.Fatalf("opening the Developer Mode key: %v", err)
	}
	const value = "AllowDevelopmentWithoutDevLicense"
	was, _, err := key.GetIntegerValue(value)
	if err != nil {
		key.Close()
		t.Fatalf("reading %s: %v", value, err)
	}
	if err := key.SetDWordValue(value, 0); err != nil {
		key.Close()
		t.Fatalf("turning off Developer Mode: %v", err)
	}
	t.Cleanup(func() {
		defer key.Close()
		if err := key.SetDWordValue(value, uint32(was)); err != nil {
			t.Errorf("restoring %s to %d: %v", value, was, err)
		}
	})
}

func berthWithoutSymlinkPrivilege(t *testing.T, repo *Repo, dir string, args ...string) Result {
	t.Helper()
	attr := symlinkless(t)
	if mayMakeSymlinks(t, attr) {
		turnOffDeveloperMode(t)
		if mayMakeSymlinks(t, attr) {
			t.Fatal("a process with no symlink privilege and Developer Mode off still made a symlink, so this test can prove nothing")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, berthBin, args...)
	cmd.Dir = dir
	cmd.Env = repo.Env()
	cmd.SysProcAttr = attr
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.Exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("berth %v without the symlink privilege: %v", args, err)
	}
	return res
}
