package sqlite

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kodnificent/berth/internal/winsys"
	"golang.org/x/sys/windows"
)

const reservedByte = pendingByte + 1

func writeSparseDatabase(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var ret uint32
	if err := windows.DeviceIoControl(windows.Handle(f.Fd()), windows.FSCTL_SET_SPARSE, nil, 0, nil, 0, &ret, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 100)
	copy(header, magic)
	if _, err := f.WriteAt(header, 0); err != nil {
		t.Fatal(err)
	}
	for _, at := range []struct {
		off  int64
		text string
	}{{pendingByte - 16, "before the lock "}, {pendingByte + 2 + 8, "after the bytes"}, {size - 16, "near the end   "}} {
		if _, err := f.WriteAt([]byte(at.text), at.off); err != nil {
			t.Fatal(err)
		}
	}
}

// Windows fails a read of bytes another handle has locked exclusively, and on a file
// past 1 GiB main's writers hold exactly those.
func cloneWhileWriterHoldsReserved(t *testing.T, dir string, opts Options) Copied {
	t.Helper()
	src := filepath.Join(dir, "app.sqlite")
	size := int64(pendingByte + 1<<20 + 5)
	writeSparseDatabase(t, src, size)

	writer, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if ok, err := winsys.Lock(writer, reservedByte, 1, true, false); err != nil || !ok {
		t.Fatalf("taking RESERVED: %v, %v", ok, err)
	}
	reader, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.ReadAt(make([]byte, 1), reservedByte); !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		t.Fatalf("reading RESERVED from another handle gave %v, so this proves nothing", err)
	}

	dst := filepath.Join(dir, "copy.sqlite")
	c, err := Clone(src, dst, opts)
	if err != nil {
		t.Fatalf("Clone while a writer holds RESERVED: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(got)) != size {
		t.Fatalf("copy is %d bytes, want %d", len(got), size)
	}
	for _, at := range []struct {
		off  int64
		text string
	}{{pendingByte - 16, "before the lock "}, {pendingByte + 2 + 8, "after the bytes"}, {size - 16, "near the end   "}} {
		if !bytes.Equal(got[at.off:at.off+int64(len(at.text))], []byte(at.text)) {
			t.Errorf("copy differs from the source at %d", at.off)
		}
	}
	return c
}

func TestByteCopyReadsPastReservedWhileAWriterHoldsIt(t *testing.T) {
	cloneWhileWriterHoldsReserved(t, t.TempDir(), Options{NoCoW: true})
}

func TestBlockCloneIsNotRefusedWhileAWriterHoldsReserved(t *testing.T) {
	parent := os.Getenv("BERTH_TEST_DEV_DRIVE")
	if parent == "" {
		t.Skip("BERTH_TEST_DEV_DRIVE is not set")
	}
	dir, err := os.MkdirTemp(parent, "sqlite-lock-bytes-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if c := cloneWhileWriterHoldsReserved(t, dir, Options{}); c.Strategy != CoW {
		t.Errorf("the copy took the %s path, so block cloning was refused across a locked byte", c.Strategy)
	}
}
