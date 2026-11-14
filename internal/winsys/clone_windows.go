package winsys

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// symlinkAllowUnprivileged lets a process make a symlink without the privilege when
// Developer Mode is on. Older releases of x/sys don't name it.
const symlinkAllowUnprivileged = 0x2

// extended prefixes an absolute path so Windows accepts it past MAX_PATH, as package os
// does internally for the paths it opens.
func extended(path string) string {
	if len(path) < windows.MAX_PATH-12 || strings.HasPrefix(path, `\\?\`) {
		return path
	}
	if strings.HasPrefix(path, `\\`) {
		return `\\?\UNC\` + path[2:]
	}
	return `\\?\` + path
}

// Symlink makes link a symbolic link to target. Windows fixes a link as a file or a
// directory when it is made, so the caller says which.
func Symlink(target, link string, dir bool) error {
	from, err := windows.UTF16PtrFromString(extended(link))
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(filepath.FromSlash(target))
	if err != nil {
		return err
	}
	var flags uint32
	if dir {
		flags = windows.SYMBOLIC_LINK_FLAG_DIRECTORY
	}
	err = windows.CreateSymbolicLink(from, to, flags|symlinkAllowUnprivileged)
	if err != nil {
		// Builds of Windows before 14972 reject the flag.
		err = windows.CreateSymbolicLink(from, to, flags)
	}
	if err != nil {
		return &os.LinkError{Op: "symlink", Old: target, New: link, Err: err}
	}
	return nil
}

// LinkIsDir reports whether the symbolic link at path is the directory kind, without
// following it.
func LinkIsDir(path string) bool {
	p, err := windows.UTF16PtrFromString(extended(path))
	if err != nil {
		return false
	}
	attrs, err := windows.GetFileAttributes(p)
	return err == nil && attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0
}

// FinalPath is where path leads once every symlink and junction along it is followed.
// filepath.EvalSymlinks gives up at a junction with more of the path after it.
func FinalPath(path string) (string, error) {
	p, err := windows.UTF16PtrFromString(extended(path))
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(p, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
	if err != nil {
		return "", &os.PathError{Op: "GetFinalPathNameByHandle", Path: path, Err: err}
	}
	final := windows.UTF16ToString(buf[:n])
	if rest, ok := strings.CutPrefix(final, `\\?\UNC\`); ok {
		return `\\` + rest, nil
	}
	return strings.TrimPrefix(final, `\\?\`), nil
}

// MakeJunction makes link a junction leading to the absolute path target. link must not
// exist. No privilege is needed, which is why a junction works where a symlink doesn't.
func MakeJunction(target, link string) error {
	if err := os.Mkdir(link, 0o755); err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(extended(link))
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		_ = os.Remove(link)
		return &os.PathError{Op: "open", Path: link, Err: err}
	}
	defer windows.CloseHandle(h)
	buf := mountPointBuffer(target)
	var ret uint32
	if err := windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT, &buf[0], uint32(len(buf)), nil, 0, &ret, nil); err != nil {
		_ = os.Remove(link)
		return &os.PathError{Op: "making junction", Path: link, Err: err}
	}
	return nil
}

// mountPointBuffer is a REPARSE_DATA_BUFFER for a junction leading to target: the
// reparse tag and its data, whose names are the NT path and the path to display, each
// ending in a null that the lengths don't count.
func mountPointBuffer(target string) []byte {
	target = filepath.Clean(target)
	nt := utf16.Encode([]rune(`\??\` + target))
	display := utf16.Encode([]rune(target))
	ntBytes, displayBytes := uint16(len(nt))*2, uint16(len(display))*2

	dataLen := 8 + ntBytes + 2 + displayBytes + 2
	buf := make([]byte, 8+int(dataLen))
	binary.LittleEndian.PutUint32(buf[0:], windows.IO_REPARSE_TAG_MOUNT_POINT)
	binary.LittleEndian.PutUint16(buf[4:], dataLen)
	binary.LittleEndian.PutUint16(buf[8:], 0)
	binary.LittleEndian.PutUint16(buf[10:], ntBytes)
	binary.LittleEndian.PutUint16(buf[12:], ntBytes+2)
	binary.LittleEndian.PutUint16(buf[14:], displayBytes)
	at := 16
	for _, c := range nt {
		binary.LittleEndian.PutUint16(buf[at:], c)
		at += 2
	}
	at += 2
	for _, c := range display {
		binary.LittleEndian.PutUint16(buf[at:], c)
		at += 2
	}
	return buf
}

// Filesystem names the filesystem path sits on, NTFS or ReFS, or "" when unknown.
func Filesystem(path string) string {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return ""
	}
	root := make([]uint16, windows.MAX_PATH+1)
	if windows.GetVolumePathName(p, &root[0], uint32(len(root))) != nil {
		return ""
	}
	name := make([]uint16, windows.MAX_PATH+1)
	if windows.GetVolumeInformation(&root[0], nil, 0, nil, nil, nil, &name[0], uint32(len(name))) != nil {
		return ""
	}
	return windows.UTF16ToString(name)
}

// maxCloneRegion is the most one FSCTL_DUPLICATE_EXTENTS_TO_FILE is given. The call takes
// a region under 4 GiB, and 1 GiB divides evenly by any cluster size.
const maxCloneRegion = 1 << 30

// integrityInformation is FSCTL_GET_INTEGRITY_INFORMATION's buffer; the first eight bytes
// are also FSCTL_SET_INTEGRITY_INFORMATION's.
type integrityInformation struct {
	checksum  uint16
	_         uint16
	flags     uint32
	chunkSize uint32
	cluster   uint32
}

type duplicateExtents struct {
	file                       windows.Handle
	sourceOffset, targetOffset int64
	byteCount                  int64
}

// BlockClone gives out, empty and on the same ReFS volume as in, a share of in's clusters:
// data ReFS stores once and copies only when either file is written. It fails on a
// filesystem that can't block clone, NTFS among them, and leaves out empty on any failure
// so the caller can copy the bytes instead.
func BlockClone(out, in *os.File) error {
	var ii integrityInformation
	var ret uint32
	if err := windows.DeviceIoControl(windows.Handle(in.Fd()), windows.FSCTL_GET_INTEGRITY_INFORMATION, nil, 0,
		(*byte)(unsafe.Pointer(&ii)), uint32(unsafe.Sizeof(ii)), &ret, nil); err != nil {
		return err
	}
	if ii.cluster == 0 {
		return errors.ErrUnsupported
	}
	info, err := in.Stat()
	if err != nil {
		return err
	}
	attrs := info.Sys().(*syscall.Win32FileAttributeData)
	h := windows.Handle(out.Fd())
	// ReFS refuses a clone between files with different integrity settings, so a failure
	// here surfaces as that refusal and the caller copies.
	_ = windows.DeviceIoControl(h, windows.FSCTL_SET_INTEGRITY_INFORMATION, (*byte)(unsafe.Pointer(&ii)), 8, nil, 0, &ret, nil)
	// Sparse while it is empty, so extending it allocates nothing and a clone needs no
	// free space for its whole size.
	if err := windows.DeviceIoControl(h, windows.FSCTL_SET_SPARSE, nil, 0, nil, 0, &ret, nil); err != nil {
		return err
	}
	if err := shareClusters(out, in, info.Size(), int64(ii.cluster)); err != nil {
		_ = out.Truncate(0)
		_ = clearSparse(out)
		return err
	}
	if attrs.FileAttributes&windows.FILE_ATTRIBUTE_SPARSE_FILE == 0 {
		// Failing to is harmless: a sparse file with no holes reads the same.
		_ = clearSparse(out)
	}
	return nil
}

// shareClusters sets out's length and duplicates in's extents into it. A region must end
// on a cluster boundary, so the last one runs past the end of the file; ReFS accepts that
// and keeps the length set here.
func shareClusters(out, in *os.File, size, cluster int64) error {
	if err := out.Truncate(size); err != nil {
		return err
	}
	end := size + (cluster-size%cluster)%cluster
	for off := int64(0); off < end; off += maxCloneRegion {
		d := duplicateExtents{windows.Handle(in.Fd()), off, off, min(end-off, maxCloneRegion)}
		var ret uint32
		if err := windows.DeviceIoControl(windows.Handle(out.Fd()), windows.FSCTL_DUPLICATE_EXTENTS_TO_FILE,
			(*byte)(unsafe.Pointer(&d)), uint32(unsafe.Sizeof(d)), nil, 0, &ret, nil); err != nil {
			return err
		}
	}
	return nil
}

func clearSparse(f *os.File) error {
	var off byte // FILE_SET_SPARSE_BUFFER's one field, SetSparse false
	var ret uint32
	return windows.DeviceIoControl(windows.Handle(f.Fd()), windows.FSCTL_SET_SPARSE, &off, 1, nil, 0, &ret, nil)
}
