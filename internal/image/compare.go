// Package image compares what two compose builds read, so a berth can use main's image
// exactly when building would give it nothing main's image doesn't already have.
package image

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"

	"github.com/kodnificent/berth/internal/bytesize"
	"github.com/kodnificent/berth/internal/compose"
	"github.com/kodnificent/berth/internal/dockerfile"
	"github.com/kodnificent/berth/internal/worktree"
)

// Limits bound how much of a build context berth looks at before it gives up deciding.
type Limits struct {
	// Bytes is the most file content berth reads, the berth's and main's copies together.
	Bytes int64
	// Entries is the most context entries berth looks at in each worktree.
	Entries int
}

// DefaultLimits keep a comparison to about a second on a laptop's SSD.
var DefaultLimits = Limits{Bytes: 100_000_000, Entries: 100_000}

// Result is what Compare decided.
type Result struct {
	Same bool
	// Skipped is set when deciding would go past the limits, so nothing is known.
	Skipped bool
	// Reason says why the builds aren't known to be equal.
	Reason string
	// Read is how many bytes of context files berth read to decide.
	Read int64
}

// Tree is a worktree, whose git index tells which tracked files are unchanged without
// reading them.
type Tree struct {
	Root  string
	once  sync.Once
	blobs map[string]string
}

func NewTree(root string) *Tree { return &Tree{Root: root} }

// blob is the mode and object git records for rel, relative to the tree's root, when
// the file on disk is unchanged from it; "" when git can't vouch for it.
func (t *Tree) blob(rel string) string {
	t.once.Do(func() { t.blobs = cleanBlobs(t.Root) })
	return t.blobs[rel]
}

func cleanBlobs(root string) map[string]string {
	prefix, err := worktree.GitAt(root, "rev-parse", "--show-prefix")
	if err != nil {
		return nil
	}
	prefix = strings.TrimSpace(prefix)
	// diff-files compares the files' stat data with the index, and reads any racily
	// clean file, so a path it doesn't name has the content its index entry records.
	dirty, err := worktree.GitAt(root, "diff-files", "--name-only", "-z")
	if err != nil {
		return nil
	}
	changed := map[string]bool{}
	for _, p := range strings.Split(dirty, "\x00") {
		changed[p] = true
	}
	entries, err := worktree.GitAt(root, "ls-files", "-s", "-v", "--full-name", "-z")
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, e := range strings.Split(entries, "\x00") {
		meta, p, ok := strings.Cut(e, "\t")
		f := strings.Fields(meta)
		// Any other tag is assume-unchanged, skip-worktree or a conflict, which git
		// doesn't check against the file.
		if !ok || len(f) != 4 || f[0] != "H" || f[3] != "0" || changed[p] {
			continue
		}
		rel, ok := strings.CutPrefix(p, prefix)
		if !ok {
			continue
		}
		out[rel] = f[1] + " " + f[2]
	}
	return out
}

// Compare decides whether berth's build reads exactly what main's does: the compose
// build section, the Dockerfile, the ignore file that applies, and the context files and
// named contexts the target stage reads, with their paths, types, permissions, content
// and symlink targets, but not their times. Most files are settled without reading
// them: by git for tracked files, and by size and modification time for the rest, which
// a copy-on-write clone keeps. What still looks different is read, within lim.
func Compare(berth compose.Build, bt *Tree, main compose.Build, mt *Tree, lim Limits) (Result, error) {
	if !bytes.Equal(berth.Spec, main.Spec) {
		return Result{Reason: "the compose build section differs from main's"}, nil
	}
	bdf, err := dockerfileContent(berth)
	if err != nil {
		return Result{}, err
	}
	mdf, err := dockerfileContent(main)
	if err != nil {
		return Result{}, err
	}
	if !bytes.Equal(bdf, mdf) {
		return Result{Reason: "the Dockerfile differs from main's"}, nil
	}
	if !filepath.IsAbs(berth.Context) {
		// A remote context is named in the spec; there is nothing local to read.
		return Result{Same: true}, nil
	}

	c := &comparer{lim: lim, berth: bt, main: mt}
	reads := dockerfile.Analyze(bdf, berth.Target)
	paths := []string{"."}
	if !reads.Whole {
		paths = nil
		for _, p := range reads.Paths {
			paths = append(paths, dockerfile.Clean(p))
		}
	}
	if err := c.context(berth.Context, ignoreFile(berth), main.Context, ignoreFile(main), paths); err != nil {
		return Result{}, err
	}
	names := reads.Contexts
	if reads.Whole {
		// Every local named context may be read.
		names = sortedKeys(berth.AdditionalContexts)
	}
	for _, name := range names {
		bdir, mdir := berth.AdditionalContexts[name], main.AdditionalContexts[name]
		if !filepath.IsAbs(bdir) {
			continue
		}
		if !filepath.IsAbs(mdir) {
			return Result{Reason: fmt.Sprintf("the build context %s differs from main's", name)}, nil
		}
		if err := c.context(bdir, filepath.Join(bdir, ".dockerignore"), mdir, filepath.Join(mdir, ".dockerignore"), []string{"."}); err != nil {
			return Result{}, err
		}
	}
	return c.finish()
}

// ReadsWholeRepo reports whether b's build may read every file in its context, the
// context is root, and no ignore file leaves anything out: every file in the repository
// counts as a build input.
func ReadsWholeRepo(b compose.Build, root string) bool {
	if !filepath.IsAbs(b.Context) || filepath.Clean(b.Context) != filepath.Clean(root) || fileExists(ignoreFile(b)) {
		return false
	}
	content, err := dockerfileContent(b)
	return err == nil && dockerfile.Analyze(content, b.Target).Whole
}

func dockerfileContent(b compose.Build) ([]byte, error) {
	if b.Dockerfile == "" {
		return []byte(b.DockerfileInline), nil
	}
	return os.ReadFile(b.Dockerfile)
}

// ignoreFile is the ignore file BuildKit applies: one named after the Dockerfile beside
// it, else the context's .dockerignore.
func ignoreFile(b compose.Build) string {
	if b.Dockerfile != "" {
		if p := b.Dockerfile + ".dockerignore"; fileExists(p) {
			return p
		}
	}
	return filepath.Join(b.Context, ".dockerignore")
}

// errDecided stops a walk once its outcome is known.
var errDecided = errors.New("decided")

type pair struct {
	rel, berth, main string
	size             int64
}

type comparer struct {
	lim         Limits
	berth, main *Tree
	differ      string
	skipped     string
	pending     []pair
	pendingSize int64
}

func (c *comparer) decided() bool { return c.differ != "" || c.skipped != "" }

// context compares the files under bdir and mdir that one of paths selects and the
// ignore file doesn't exclude.
func (c *comparer) context(bdir, bignore, mdir, mignore string, paths []string) error {
	if c.decided() || bdir == mdir {
		return nil
	}
	bpat, err := readIgnore(bignore)
	if err != nil {
		return err
	}
	mpat, err := readIgnore(mignore)
	if err != nil {
		return err
	}
	if !bytes.Equal(bpat, mpat) {
		c.differ = fmt.Sprintf("%s differs from main's", filepath.Base(bignore))
		return nil
	}
	patterns, err := ignorefile.ReadAll(bytes.NewReader(bpat))
	if err != nil {
		return fmt.Errorf("reading %s: %w", bignore, err)
	}
	pm, err := patternmatcher.New(patterns)
	if err != nil {
		return fmt.Errorf("reading %s: %w", bignore, err)
	}

	seen := map[string]bool{}
	err = c.walk(bdir, pm, paths, func(rel string, d fs.DirEntry) error {
		seen[rel] = true
		info, err := d.Info()
		if err != nil {
			return err
		}
		return c.entry(rel, filepath.Join(bdir, rel), info, filepath.Join(mdir, rel))
	})
	if err != nil || c.decided() {
		return ignoreDecided(err)
	}
	err = c.walk(mdir, pm, paths, func(rel string, _ fs.DirEntry) error {
		if !seen[rel] {
			c.differ = fmt.Sprintf("%s is in main but not in the berth", rel)
			return errDecided
		}
		return nil
	})
	return ignoreDecided(err)
}

// entry compares one context entry of the berth's with main's, by everything short of
// content, and queues it for reading when only content could tell them apart.
func (c *comparer) entry(rel, bpath string, bi fs.FileInfo, mpath string) error {
	mi, err := os.Lstat(mpath)
	if errors.Is(err, fs.ErrNotExist) {
		c.differ = fmt.Sprintf("%s is in the berth but not in main", rel)
		return errDecided
	}
	if err != nil {
		return err
	}
	if bi.Mode() != mi.Mode() {
		c.differ = fmt.Sprintf("%s has a different type or permissions from main's", rel)
		return errDecided
	}
	switch {
	case bi.Mode()&fs.ModeSymlink != 0:
		bl, err := os.Readlink(bpath)
		if err != nil {
			return err
		}
		ml, err := os.Readlink(mpath)
		if err != nil {
			return err
		}
		if bl != ml {
			c.differ = fmt.Sprintf("%s links somewhere other than main's", rel)
			return errDecided
		}
	case bi.Mode().IsRegular():
		if bi.Size() != mi.Size() {
			c.differ = fmt.Sprintf("%s differs from main's", rel)
			return errDecided
		}
		if c.tracked(bpath, mpath) || bi.ModTime().Equal(mi.ModTime()) {
			return nil
		}
		c.pending = append(c.pending, pair{rel, bpath, mpath, bi.Size()})
		c.pendingSize += 2 * bi.Size()
	}
	return nil
}

// tracked reports whether git vouches that both files hold the same committed content.
func (c *comparer) tracked(bpath, mpath string) bool {
	brel, err := filepath.Rel(c.berth.Root, bpath)
	if err != nil || !filepath.IsLocal(brel) {
		return false
	}
	mrel, err := filepath.Rel(c.main.Root, mpath)
	if err != nil || !filepath.IsLocal(mrel) {
		return false
	}
	b := c.berth.blob(filepath.ToSlash(brel))
	return b != "" && b == c.main.blob(filepath.ToSlash(mrel))
}

// finish reads what only content can settle, unless that would go past the limit.
func (c *comparer) finish() (Result, error) {
	if c.differ != "" {
		return Result{Reason: c.differ}, nil
	}
	if c.skipped != "" {
		return Result{Skipped: true, Reason: c.skipped}, nil
	}
	if c.pendingSize > c.lim.Bytes {
		return Result{Skipped: true, Reason: fmt.Sprintf("deciding would read %s of build context files, more than the %s berth reads", bytesize.Format(c.pendingSize), bytesize.Format(c.lim.Bytes))}, nil
	}
	var read int64
	for _, p := range c.pending {
		bsum, err := sum(p.berth)
		if err != nil {
			return Result{}, err
		}
		msum, err := sum(p.main)
		if err != nil {
			return Result{}, err
		}
		read += 2 * p.size
		if !bytes.Equal(bsum, msum) {
			return Result{Reason: fmt.Sprintf("%s differs from main's", p.rel), Read: read}, nil
		}
	}
	return Result{Same: true, Read: read}, nil
}

// walk calls fn for every entry under root that one of paths selects and the ignore
// patterns keep, in lexical order, giving up once it has visited more entries than the
// limit.
func (c *comparer) walk(root string, pm *patternmatcher.PatternMatcher, paths []string, fn func(rel string, d fs.DirEntry) error) error {
	n := 0
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		// Every entry visited counts, selected or not: exclusions in the ignore file and
		// globs in a source make the walk go through directories nothing is read from.
		if n++; n > c.lim.Entries {
			c.skipped = fmt.Sprintf("the build context has more than %d files, more than berth looks at", c.lim.Entries)
			return errDecided
		}
		if d.IsDir() && !leadsTo(rel, paths) {
			return filepath.SkipDir
		}
		ignored, err := pm.MatchesOrParentMatches(rel)
		if err != nil {
			return err
		}
		if ignored {
			if d.IsDir() && !pm.Exclusions() {
				return filepath.SkipDir
			}
			return nil
		}
		if !selected(rel, paths) {
			return nil
		}
		return fn(rel, d)
	})
}

func ignoreDecided(err error) error {
	if errors.Is(err, errDecided) {
		return nil
	}
	return err
}

func readIgnore(p string) ([]byte, error) {
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

func sum(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// selected reports whether rel is one of paths, or lies under one, matching globs as a
// build does.
func selected(rel string, paths []string) bool {
	for _, p := range paths {
		if p == "." {
			return true
		}
		for a := rel; a != "."; a = path.Dir(a) {
			if ok, _ := path.Match(p, a); ok {
				return true
			}
		}
	}
	return false
}

// leadsTo reports whether directory rel may hold something one of paths selects, so
// directories nothing reads are never walked.
func leadsTo(rel string, paths []string) bool {
	for _, p := range paths {
		prefix := literalPrefix(p)
		if prefix == "." || rel == prefix || strings.HasPrefix(rel, prefix+"/") || strings.HasPrefix(prefix, rel+"/") {
			return true
		}
	}
	return false
}

// literalPrefix is the part of p before its first glob segment.
func literalPrefix(p string) string {
	var keep []string
	for _, seg := range strings.Split(p, "/") {
		if strings.ContainsAny(seg, `*?[\`) {
			break
		}
		keep = append(keep, seg)
	}
	if len(keep) == 0 {
		return "."
	}
	return strings.Join(keep, "/")
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
