package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/clone"
	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/envfile"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/safefile"
	"github.com/kodnificent/berth/internal/worktree"
)

// managedEnv is every key berth writes into the berth's .env, with its value.
func managedEnv(cfg *config.Config, rec registry.Record) map[string]string {
	assigned := map[string]int{}
	for _, p := range rec.Ports {
		if p.HostPort > 0 {
			assigned[p.Name] = p.HostPort
		}
	}
	env := cfg.Resolve(rec.Name, cfg.PortValues(assigned)).Env
	for name, p := range cfg.Ports {
		_, taken := env[p.Env]
		if port, ok := assigned[name]; ok && p.Env != "" && !taken {
			env[p.Env] = strconv.Itoa(port)
		}
	}
	if rec.ComposeFile != "" {
		env["COMPOSE_PROJECT_NAME"] = rec.ComposeProject
		env["COMPOSE_FILE"] = rec.ComposeFile
	}
	databaseEnv(rec, env)
	redisEnv(rec, env)
	return env
}

// checkEnvWritable refuses a tracked .env before anything is provisioned: writing the
// block into it would change a committed file.
func checkEnvWritable(w *worktree.Worktree) error {
	out, err := w.Git("ls-files", "--", ".env")
	if err != nil {
		return fmt.Errorf("checking whether .env is tracked in %s: %w", w.Path, err)
	}
	if strings.TrimSpace(out) != "" {
		return apperr.New(apperr.Generic, "%s is tracked by git, and berth only writes gitignored files; untrack it (git rm --cached .env) and add it to .gitignore", filepath.Join(w.Path, ".env"))
	}
	return nil
}

// materialize writes the berth's files: the source's gitignored .env* files the worktree
// lacks, the [files] clone paths, the compose files and the managed block. It records
// in rec what it created, including when it fails partway, so removal can find
// everything. It reports how each [files] clone path was copied and how long it took.
func materialize(stderr io.Writer, w *worktree.Worktree, cfg *config.Config, plan *composePlan, rec *registry.Record) error {
	var created []string
	defer func() { rec.Files = union(rec.Files, created) }()
	var refused refusals

	envs, err := ignoredEnvFiles(w.Repository)
	if err != nil {
		return err
	}
	for _, rel := range envs {
		if _, err := os.Stat(filepath.Join(w.Path, filepath.Dir(rel))); err != nil {
			continue
		}
		r, err := clone.Merge(w.Repository, w.Path, rel)
		created = append(created, r.Created...)
		recordSizes(rec, r.Sizes)
		if refused.add(rel, err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("cloning %s: %w", rel, err)
		}
	}
	var fullCopies bool
	for _, rel := range cfg.Files.Clone {
		rel = filepath.Clean(rel)
		if slices.Contains(rec.Cloned, rel) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(w.Repository, rel)); err != nil {
			continue
		}
		start := time.Now()
		r, err := clone.Merge(w.Repository, w.Path, rel)
		created = append(created, r.Created...)
		recordSizes(rec, r.Sizes)
		if refused.add(rel, err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("cloning %s: %w", rel, err)
		}
		if len(r.Created) > 0 {
			fmt.Fprintf(stderr, "berth: cloned %s (%s) in %s\n", rel, r.Strategy(), time.Since(start).Round(time.Millisecond))
			fullCopies = fullCopies || r.Shared == 0 && r.Copied > 0
		}
		rec.Cloned = union(rec.Cloned, []string{rel})
	}
	if fullCopies {
		if hint := clone.FullCopyHint(w.Path); hint != "" {
			fmt.Fprintf(stderr, "berth: those clones were full copies, as this filesystem has no copy-on-write; %s\n", hint)
		}
	}

	c, err := writeCompose(w, cfg, plan, rec, &refused)
	created = append(created, c...)
	if err != nil {
		return err
	}

	env := managedEnv(cfg, *rec)
	block, err := envfile.Block(env)
	if err != nil {
		return apperr.New(apperr.ConfigInvalid, "[env.set] %v", err)
	}
	path := filepath.Join(w.Path, ".env")
	content, mode, err := readEnv(path)
	missing := errors.Is(err, fs.ErrNotExist)
	if err != nil && !missing {
		return err
	}
	var out []byte
	var sep bool
	switch {
	case len(env) > 0:
		if missing {
			created = append(created, ".env")
		}
		out, sep = envfile.Apply(content, block, rec.EnvSeparator)
	case !missing:
		out = envfile.Strip(content, rec.EnvSeparator)
	}
	if len(env) > 0 && missing || !missing && !bytes.Equal(out, content) {
		if err := writeFileAtomic(path, out, mode); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
	}
	rec.EnvSeparator = sep

	generated := []string{".env"}
	if rec.ComposeFile != "" {
		generated = append(generated, OverrideFile)
	}
	if err := exclude(w, append(created, generated...)); err != nil {
		return err
	}
	return refused.failure()
}

// refusals are the cloned paths Windows refused a symlink in. They don't stop the other
// paths: add ends with the error once the rest of the berth's files are written.
type refusals struct {
	paths []string
	err   error
}

func (r *refusals) add(rel string, err error) bool {
	if !errors.Is(err, clone.ErrNoSymlinkPrivilege) {
		return false
	}
	r.paths, r.err = append(r.paths, rel), err
	return true
}

func (r *refusals) failure() error {
	if r.err == nil {
		return nil
	}
	return fmt.Errorf("cloning %s: %w", strings.Join(r.paths, ", "), r.err)
}

// recordSizes keeps the apparent size of each directory berth cloned, so list can report
// it without walking the clone again.
func recordSizes(rec *registry.Record, dirs map[string]int64) {
	for rel, n := range dirs {
		if rec.Sizes == nil {
			rec.Sizes = map[string]int64{}
		}
		rec.Sizes[rel] = n
	}
}

func readEnv(path string) ([]byte, fs.FileMode, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0o644, err
	}
	data, err := os.ReadFile(path)
	return data, info.Mode().Perm(), err
}

// ignoredEnvFiles lists the source's .env* files that git ignores, relative to it. Files
// inside an ignored directory are left out, since git reports only the directory.
func ignoredEnvFiles(source string) ([]string, error) {
	out, err := worktree.GitAt(source, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		return nil, fmt.Errorf("listing ignored files in %s: %w", source, err)
	}
	var envs []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" && !strings.HasSuffix(p, "/") && strings.HasPrefix(filepath.Base(p), ".env") {
			envs = append(envs, filepath.FromSlash(p))
		}
	}
	return envs, nil
}

// exclude adds an anchored entry to the repository's info/exclude for each path that
// would show in git status as untracked. That file is shared by every worktree of the
// repository and, unlike .gitignore, is never committed.
func exclude(w *worktree.Worktree, paths []string) error {
	out, err := w.Git(append([]string{"--literal-pathspecs", "ls-files", "-z", "--others", "--exclude-standard", "--directory", "--"}, paths...)...)
	if err != nil {
		return fmt.Errorf("listing untracked files in %s: %w", w.Path, err)
	}
	untracked := map[string]bool{}
	for _, p := range strings.Split(out, "\x00") {
		untracked[strings.TrimSuffix(p, "/")] = true
	}
	var shown []string
	for _, p := range paths {
		if untracked[filepath.ToSlash(p)] {
			shown = append(shown, p)
		}
	}
	if len(shown) == 0 {
		return nil
	}
	path := filepath.Join(w.CommonDir, "info", "exclude")
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	have := map[string]bool{}
	for _, line := range strings.Split(string(existing), "\n") {
		have[line] = true
	}
	var add bytes.Buffer
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		add.WriteByte('\n')
	}
	sort.Strings(shown)
	for _, p := range shown {
		if entry := excludeEntry(p); !have[entry] {
			have[entry] = true
			add.WriteString(entry + "\n")
		}
	}
	if add.Len() == 0 || add.String() == "\n" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(add.Bytes()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// excludeEntry anchors p to the worktree root and escapes the characters gitignore
// patterns treat specially, so the entry matches exactly that path.
func excludeEntry(p string) string {
	var b strings.Builder
	b.WriteByte('/')
	for _, r := range filepath.ToSlash(p) {
		if strings.ContainsRune(`\*?[!# `, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range append(append([]string{}, a...), b...) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".berth-tmp-"+filepath.Base(path)+"-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return safefile.Replace(tmp.Name(), path)
}
