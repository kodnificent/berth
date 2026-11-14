// Package clone copies paths from the source worktree into a berth, copy-on-write where
// the filesystem supports it, and repoints symlinks that lead into the source.
package clone

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var ErrNoSymlinkPrivilege = errors.New("Windows refused to make a symbolic link; turn on Developer Mode in Settings, or run as an administrator, and run berth add again")

// Result is what a Merge created and how it copied the data.
type Result struct {
	// Created are the paths, relative to the roots, that Merge made; everything under
	// each of them is new.
	Created []string
	// Sizes gives each created directory's apparent size, counted while repointing its
	// links, so nothing has to walk the clone again to report it.
	Sizes map[string]int64
	// Shared counts clones whose data the filesystem shares with the source by
	// copy-on-write: a whole tree on APFS, a file on btrfs or XFS.
	Shared int
	Copied int
}

// Strategy names how the data was copied: cow, copy, or both when the filesystem
// refused to share some files.
func (r Result) Strategy() string {
	switch {
	case r.Shared > 0 && r.Copied > 0:
		return fmt.Sprintf("cow, with %d %s copied in full", r.Copied, plural(r.Copied, "file", "files"))
	case r.Shared > 0:
		return "cow"
	default:
		return "copy"
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// Merge clones rel from srcRoot into dstRoot, filling in only what dstRoot lacks, so files
// git checked out in the berth are never overwritten. A rel missing from srcRoot is
// skipped.
func Merge(srcRoot, dstRoot, rel string) (Result, error) {
	r := Result{Sizes: map[string]int64{}}
	err := merge(srcRoot, dstRoot, rel, &r)
	return r, err
}

func merge(srcRoot, dstRoot, rel string, r *Result) error {
	src, dst := filepath.Join(srcRoot, rel), filepath.Join(dstRoot, rel)
	si, err := os.Lstat(src)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	di, err := os.Lstat(dst)
	if errors.Is(err, fs.ErrNotExist) {
		top, err := mkdirParents(dstRoot, rel)
		if err != nil {
			return err
		}
		shared, copied, err := Tree(src, dst)
		r.Shared += shared
		r.Copied += copied
		var size int64
		if err == nil {
			if size, err = Repoint(dst, srcRoot, dstRoot, rel); err != nil {
				// Some links in it may still lead into main.
				_ = os.RemoveAll(dst)
			}
		}
		if err != nil {
			if top != rel {
				r.Created = append(r.Created, top)
			}
			return err
		}
		r.Created = append(r.Created, top)
		if top != rel || si.IsDir() {
			r.Sizes[top] = size
		}
		return nil
	}
	if err != nil {
		return err
	}
	if isLink(src, si.Mode()) && isLink(dst, di.Mode()) {
		return repointExisting(dst, srcRoot, dstRoot)
	}
	if !si.IsDir() || !di.IsDir() {
		return nil
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := merge(srcRoot, dstRoot, filepath.Join(rel, e.Name()), r); err != nil {
			return err
		}
	}
	return nil
}

// mkdirParents creates rel's missing parent directories and returns the topmost path it
// creates, or rel itself when every parent exists.
func mkdirParents(root, rel string) (string, error) {
	top := rel
	for dir := filepath.Dir(rel); dir != "."; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(root, dir)); err == nil {
			break
		}
		top = dir
	}
	return top, os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755)
}

// Tree clones src to dst, which must not exist. Symlinks are copied as links, never
// followed. It returns how many clones share their data with the source by
// copy-on-write and how many files were copied byte for byte. A failure leaves nothing
// at dst: links copied so far still lead into the source, and repointing happens only
// after a whole copy.
func Tree(src, dst string) (shared, copied int, err error) {
	if err := cowTree(src, dst); err == nil {
		return 1, 0, nil
	}
	_ = os.RemoveAll(dst)
	shared, copied, err = copyTree(src, dst)
	if err != nil {
		_ = os.RemoveAll(dst)
	}
	return shared, copied, err
}

func copyTree(src, dst string) (shared, copied int, err error) {
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return makeSymlink(link, target, linkIsDir(path))
		case isJunction(path, info.Mode()):
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return makeJunction(link, target)
		case info.IsDir():
			return os.Mkdir(target, info.Mode().Perm())
		case info.Mode().IsRegular():
			cow, err := copyFile(path, target, info)
			if cow {
				shared++
			} else if err == nil {
				copied++
			}
			return err
		default:
			return nil
		}
	})
	return shared, copied, err
}

// copyFile shares src's extents with dst where the filesystem can, and copies the bytes
// where it can't. It keeps the source's modification time, as a copy-on-write clone does,
// so a copy can be told unchanged from its source without reading both.
func copyFile(src, dst string, info fs.FileInfo) (cow bool, err error) {
	mode := info.Mode().Perm()
	in, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return false, err
	}
	cow = reflink(out, in) == nil
	if !cow {
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return false, err
		}
	}
	if err := out.Close(); err != nil {
		return cow, err
	}
	if err := os.Chmod(dst, mode); err != nil {
		return cow, err
	}
	return cow, os.Chtimes(dst, info.ModTime(), info.ModTime())
}

// Repoint walks the clone at dst, which came from rel under srcRoot, and rewrites every
// symlink that leads into srcRoot to lead to the same place under dstRoot. A relative
// link that stays inside the source already does, so it is left alone; one that climbs
// out of the source would land somewhere else from the berth's directory, so it is
// pinned to where it pointed. It returns the apparent size of the clone's regular files.
func Repoint(dst, srcRoot, dstRoot, rel string) (size int64, err error) {
	err = filepath.WalkDir(dst, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			size += info.Size()
			return nil
		}
		junction := isJunction(path, d.Type())
		if d.Type()&fs.ModeSymlink == 0 && !junction {
			return nil
		}
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		inClone, _ := filepath.Rel(dst, path)
		linkDir := filepath.Dir(filepath.Join(srcRoot, rel, inClone))
		next, ok := Retarget(target, linkDir, srcRoot, dstRoot)
		if !ok {
			return nil
		}
		if err := replaceLink(path, next, junction); err != nil {
			return fmt.Errorf("repointing %s: %w", path, err)
		}
		return nil
	})
	return size, err
}

func isLink(path string, mode fs.FileMode) bool {
	return mode&fs.ModeSymlink != 0 || isJunction(path, mode)
}

// replaceLink makes the link at path lead to next, keeping what kind of link it is.
func replaceLink(path, next string, junction bool) error {
	dir := linkIsDir(path)
	if err := os.Remove(path); err != nil {
		return err
	}
	if junction {
		return makeJunction(next, path)
	}
	return makeSymlink(next, path, dir)
}

// repointExisting fixes a link the berth already has that an interrupted clone left
// leading into main. Only an absolute one: a relative link may be one git checked out,
// and a link already into the berth stays even when the berth sits inside main.
func repointExisting(path, srcRoot, dstRoot string) error {
	target, err := os.Readlink(path)
	if err != nil {
		return err
	}
	target = withVolume(target, filepath.Dir(path))
	if !filepath.IsAbs(target) {
		return nil
	}
	for _, t := range []string{filepath.Clean(target), resolve(target)} {
		for _, root := range []string{dstRoot, resolve(dstRoot)} {
			if _, inside := within(root, t); inside {
				return nil
			}
		}
	}
	next, ok := Retarget(target, filepath.Dir(path), srcRoot, dstRoot)
	if !ok {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if err := replaceLink(path, next, isJunction(path, info.Mode())); err != nil {
		return fmt.Errorf("repointing %s: %w", path, err)
	}
	return nil
}

// withVolume gives a rooted target with no volume, \repo\x on Windows, the volume its link
// sits on, since that is where Windows looks and filepath.IsAbs says it isn't absolute.
func withVolume(target, linkDir string) string {
	if target != "" && os.IsPathSeparator(target[0]) && filepath.VolumeName(target) == "" && !filepath.IsAbs(target) {
		return filepath.VolumeName(linkDir) + target
	}
	return target
}

// Retarget returns the target a link in the berth needs, for a link in the source at
// linkDir holding target, and false when the target can stay as it is.
func Retarget(target, linkDir, srcRoot, dstRoot string) (string, bool) {
	target = withVolume(target, linkDir)
	relative := !filepath.IsAbs(target)
	if relative {
		target = filepath.Join(linkDir, target)
		if _, inside := within(srcRoot, target); inside {
			return "", false
		}
	}
	for _, t := range []string{filepath.Clean(target), resolve(target)} {
		for _, root := range []string{srcRoot, resolve(srcRoot)} {
			if rel, inside := within(root, t); inside {
				return filepath.Join(dstRoot, rel), true
			}
		}
	}
	if relative {
		return target, true
	}
	return "", false
}

func within(root, path string) (string, bool) {
	if path == root {
		return ".", true
	}
	if rest, ok := strings.CutPrefix(path, root+string(filepath.Separator)); ok {
		return rest, true
	}
	return "", false
}

// resolve follows symlinks in the longest prefix of path that exists, so /var/x and
// /private/var/x compare equal on macOS even when x itself is missing.
func resolve(path string) string {
	path = filepath.Clean(path)
	var rest []string
	for dir := path; ; dir = filepath.Dir(dir) {
		if r, err := realPath(dir); err == nil {
			return filepath.Join(append([]string{r}, rest...)...)
		}
		if dir == filepath.Dir(dir) {
			return path
		}
		rest = append([]string{filepath.Base(dir)}, rest...)
	}
}

// FullCopyHint is what add and doctor suggest when a clone at path had to copy every
// byte, or "" when there is nothing to suggest.
func FullCopyHint(path string) string {
	if runtime.GOOS == "windows" && filesystem(path) == "NTFS" {
		return "a Dev Drive clones them instantly"
	}
	return ""
}
