//go:build e2e && darwin

package e2e

import (
	"encoding/binary"
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func requireAPFS(t *testing.T, dir string) {
	t.Helper()
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		t.Fatal(err)
	}
	if fs := unix.ByteSliceToString(st.Fstypename[:]); fs != "apfs" {
		t.Skipf("block sharing is checked on APFS; %s is on %s", dir, fs)
	}
}

func blockOffsets(t *testing.T, path string) []int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	size := info.Size() &^ 4095
	return []int64{0, size / 2 &^ 4095, size - 4096}
}

// physicalOffset asks the filesystem where on the device the block holding byte off of
// path lives. x/sys has no usable struct for F_LOG2PHYS_EXT, so this packs the 4-byte
// aligned struct log2phys from sys/fcntl.h by hand.
func physicalOffset(t *testing.T, path string, off int64) int64 {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var buf [20]byte
	binary.LittleEndian.PutUint64(buf[4:], 4096)
	binary.LittleEndian.PutUint64(buf[12:], uint64(off))
	if _, _, errno := unix.Syscall(unix.SYS_FCNTL, f.Fd(), unix.F_LOG2PHYS_EXT, uintptr(unsafe.Pointer(&buf[0]))); errno != 0 {
		t.Fatalf("F_LOG2PHYS_EXT on %s at %d: %v", path, off, errno)
	}
	return int64(binary.LittleEndian.Uint64(buf[12:]))
}
