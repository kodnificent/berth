package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/compose-spec/compose-go/v2/dotenv"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/clone"
	"github.com/kodnificent/berth/internal/compose"
	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/docker"
	"github.com/kodnificent/berth/internal/envfile"
	"github.com/kodnificent/berth/internal/hook"
	"github.com/kodnificent/berth/internal/image"
	"github.com/kodnificent/berth/internal/initconf"
	"github.com/kodnificent/berth/internal/ports"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/worktree"
)

func init() {
	commands = append(commands, Command{Name: "doctor", Summary: "check a berth, or main's config, for problems", Usage: "Usage: berth doctor [--probe] [path]\n\nIn the main worktree, checks the config and compose files a berth would use.\n\nFlags:\n  --probe    also request the running app at the berth's URL and each [doctor] probe URL, and check its cookies and redirects\n", Run: runDoctor})
}

const (
	problem     = "problem"
	notVerified = "not_verified"
	// warning is worth a look but may be intended, so it never fails doctor.
	warning = "warning"
)

type finding struct {
	Check    string `json:"check"`
	Level    string `json:"level"`
	Location string `json:"location"`
	Message  string `json:"message"`
	Fix      *fix   `json:"fix,omitempty"`
}

// fix is a change that resolves a finding, for a tool to apply as it is rather than
// work one out from the message.
type fix struct {
	// Config is TOML to merge into berth's config.
	Config string `json:"config,omitempty"`
}

func runDoctor(ctx *Context, args []string) error {
	probing := slices.Contains(args, "--probe")
	args = slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--probe" })
	path, err := pathArg("doctor", args)
	if err != nil {
		return err
	}
	w, err := worktree.Resolve(path)
	if err != nil {
		return err
	}
	var found []finding
	cfg, err := config.Load(w)
	configValid := err == nil
	if err != nil {
		if apperr.CodeOf(err) != apperr.ConfigInvalid {
			return err
		}
		found = append(found, configFindings(err)...)
		if cfg, err = config.LoadFiles(); err != nil {
			return err
		}
	}
	found = append(found, hookFindings(w)...)
	if w.IsMain() {
		found = append(found, mainFindings(w, cfg)...)
		found = append(found, mainDatabaseFindings(w, cfg)...)
		found = append(found, copyOnWriteFindings(w, cfg)...)
		if probing {
			found = append(found, finding{"probe", notVerified, w.Path, "the probe checks are skipped in main: main has no berth to probe; run berth doctor --probe in a linked worktree with a berth", nil})
		}
	} else if rec, ok, err := lookupBerth(w); err != nil {
		return err
	} else if !ok {
		found = append(found, finding{"berth", problem, w.Path, "this worktree has no berth; run berth add", nil})
		if probing {
			found = append(found, finding{"probe", notVerified, w.Path, "the probe checks are skipped: this worktree has no berth to probe", nil})
		}
	} else {
		found = append(found, shadowFindings(managedEnv(cfg, rec), secretEnvKey(rec))...)
		if rec.ComposeFile != "" {
			found = append(found, composeVersionFindings(docker.ComposeVersion())...)
			found = append(found, composeFindings(w, rec)...)
		}
		found = append(found, portFindings(w, rec)...)
		found = append(found, databaseFindings(w, rec)...)
		found = append(found, sourcePathFindings(w, rec)...)
		found = append(found, buildEnvFindings(w, cfg, rec)...)
		found = append(found, ignoredFileFindings(w, cfg)...)
		found = append(found, strayPersonalFindings(w)...)
		found = append(found, copyOnWriteFindings(w, cfg)...)
		env, _ := dotenv.Read(filepath.Join(w.Path, ".env"))
		found = append(found, proxyCookieFindings(rec, env)...)
		switch {
		case probing && !configValid:
			found = append(found, finding{"probe", notVerified, w.Path, "the probe checks are skipped: the config is invalid, so the berth's URL and [doctor] probe can't be read; fix it and run berth doctor --probe again", nil})
		case probing:
			found = append(found, probeFindings(w, cfg, rec)...)
		}
	}
	if found == nil {
		found = []finding{}
	}

	var problems int
	for _, f := range found {
		if f.Level == problem {
			problems++
		}
	}
	if ctx.JSON {
		body := map[string]any{"findings": found}
		if problems > 0 {
			body["error"] = map[string]string{"code": string(apperr.Problems), "message": fmt.Sprintf("berth doctor found %d problem(s)", problems)}
		}
		if err := writeJSON(ctx.Stdout, body); err != nil {
			return err
		}
	} else {
		printFindings(ctx, found)
	}
	if problems > 0 {
		return &apperr.Reported{Code: apperr.Problems}
	}
	return nil
}

func hookFindings(w *worktree.Worktree) []finding {
	where, why, err := hook.Problem(w)
	switch {
	case err != nil:
		return []finding{{"hook", warning, w.CommonDir, err.Error(), nil}}
	case why == "":
		return nil
	}
	return []finding{{"hook", warning, where, why, nil}}
}

func lookupBerth(w *worktree.Worktree) (registry.Record, bool, error) {
	reg, err := registry.Open()
	if err != nil {
		return registry.Record{}, false, err
	}
	defer reg.Close()
	rec, ok, err := reg.Find(w)
	if ok {
		noteUse(rec.ID)
	}
	return rec, ok, err
}

func printFindings(ctx *Context, found []finding) {
	if len(found) == 0 {
		fmt.Fprintln(ctx.Stdout, "No problems found.")
		return
	}
	tw := tabwriter.NewWriter(ctx.Stdout, 0, 4, 2, ' ', 0)
	for _, f := range found {
		label := f.Check
		switch f.Level {
		case notVerified:
			label += " (not verified)"
		case warning:
			label += " (warning)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", label, f.Location, f.Message)
	}
	tw.Flush()
}

// configFindings splits a config error into one finding per problem; each line starts
// with the file that holds the key.
func configFindings(err error) []finding {
	var out []finding
	for _, line := range strings.Split(err.Error(), "\n") {
		loc := "config"
		if file, rest, ok := strings.Cut(line, ": "); ok && filepath.IsAbs(file) {
			loc, line = file, rest
		}
		out = append(out, finding{"config", problem, loc, line, nil})
	}
	return out
}

func shadowFindings(env map[string]string, secret string) []finding {
	var out []finding
	for _, s := range shadowed(env, secret) {
		out = append(out, finding{"shadowing", problem, "shell: " + s.Key,
			fmt.Sprintf("%s is exported as %q in this shell, which overrides the berth's %q for compose and most frameworks; unset it here", s.Key, s.Exported, s.Value), nil})
	}
	return out
}

// composeFindings asks compose itself whether the berth's project is valid, then looks
// for fixed container names in the repository's own compose files.
func composeFindings(w *worktree.Worktree, rec registry.Record) []finding {
	where := fmt.Sprintf("%s (COMPOSE_FILE=%s)", w.Path, rec.ComposeFile)
	if f := composeInvalid(w.Path, where, "the berth's project"); f != nil {
		return f
	}

	sep := string(os.PathListSeparator)
	if p, err := compose.Load(w.Path, compose.Options{ComposeFileEnv: true}); err == nil && p != nil {
		sep = p.PathSeparator
	}
	var files []string
	for _, f := range strings.Split(rec.ComposeFile, sep) {
		if f != OverrideFile {
			files = append(files, f)
		}
	}
	own, err := compose.Load(w.Path, compose.Options{Files: files})
	if err != nil || own == nil {
		return nil
	}
	effective, _ := compose.Load(w.Path, compose.Options{ComposeFileEnv: true})
	var still map[string]string
	if effective != nil {
		still = effective.ContainerNames()
	}
	return containerNameFindings(w.Path, files, own, still)
}

// composeInvalid asks docker compose, run in dir with flags, whether the project is
// valid, and reports why not.
func composeInvalid(dir, where, what string, flags ...string) []finding {
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "docker", append(append([]string{"compose"}, flags...), "config", "--quiet")...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return []finding{{"compose", notVerified, where, fmt.Sprintf("could not run docker compose config: %v", err), nil}}
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return []finding{{"compose", problem, where, "docker compose calls " + what + " invalid: " + msg, nil}}
	}
	return nil
}

// containerNameFindings reports each service own's files give a fixed container name.
// still holds the names the berth's effective project keeps; nil in main, which has no
// override to reset them.
func containerNameFindings(dir string, files []string, own *compose.Project, still map[string]string) []finding {
	var out []finding
	names := own.ContainerNames()
	for _, svc := range sortedKeys(names) {
		name := names[svc]
		msg := fmt.Sprintf("service %s sets container_name: %s, one name for main and every berth. berth resets it in this berth, but anything that runs docker exec %s reaches main's container; drop container_name and use docker compose exec %s", svc, name, name, svc)
		if still[svc] != "" {
			msg = fmt.Sprintf("service %s sets container_name: %s and the berth's project keeps it, so it collides with main's container; run berth add to rewrite %s", svc, still[svc], OverrideFile)
		}
		out = append(out, finding{"container_name", problem, containerNameAt(dir, files, name), msg, nil})
	}
	return out
}

// mainFindings checks, in the main worktree, the compose files a berth would read and
// the config's references into them, so a config can be checked before any berth exists.
// Everything that needs a berth is left out.
func mainFindings(w *worktree.Worktree, cfg *config.Config) []finding {
	plan, err := planCompose(w, cfg)
	if err != nil {
		where := w.Path
		if len(cfg.Compose.Files) > 0 {
			where += " ([compose] files " + strings.Join(cfg.Compose.Files, ", ") + ")"
		}
		return []finding{{"compose", problem, where, err.Error(), nil}}
	}
	if plan.own == nil {
		return nil
	}
	found := composeVersionFindings(docker.ComposeVersion())
	where := fmt.Sprintf("%s (%s)", w.Path, strings.Join(plan.files, ", "))
	var flags []string
	for _, f := range plan.files {
		flags = append(flags, "-f", f)
	}
	if f := composeInvalid(w.Path, where, "the project a berth would use", flags...); f != nil {
		return append(found, f...)
	}
	found = append(found, containerNameFindings(w.Path, plan.files, plan.own, nil)...)
	found = append(found, configRefFindings(cfg, plan.own)...)
	return append(found, buildContextFindings(w, plan.main)...)
}

// buildContextFindings warns about builds that count every file in the repository as an
// input: Docker scans or sends all of it on every build, and a berth's own .env and
// override are in it, so no berth can use main's image.
func buildContextFindings(w *worktree.Worktree, main *compose.Project) []finding {
	if main == nil {
		return nil
	}
	var out []finding
	for _, b := range main.Builds(w.Repository) {
		if image.ReadsWholeRepo(b, w.Repository) {
			out = append(out, finding{"build_context", warning, "services." + b.Service + ".build", fmt.Sprintf("%s builds with the whole repository as its context and no .dockerignore, so every file in it is a build input: Docker reads it all on each build, and every berth builds its own image, since the berth's .env and %s are in the context; add a .dockerignore that leaves out what the build doesn't need", b.Service, OverrideFile), nil})
		}
	}
	return out
}

// configRefFindings reports [compose] entries that name a service or volume the
// project doesn't have, which would otherwise be ignored without a word.
func configRefFindings(cfg *config.Config, own *compose.Project) []finding {
	services := map[string]bool{}
	for _, s := range own.ServiceNames() {
		services[s] = true
	}
	var out []finding
	for _, s := range cfg.Compose.Exclude {
		if !services[s] {
			out = append(out, finding{"config", problem, "[compose] exclude", fmt.Sprintf("excludes %s, which is not a service of the compose project", s), nil})
		}
	}
	for _, key := range sortedKeys(cfg.Compose.Ports) {
		if svc, _, _ := strings.Cut(key, ":"); !services[svc] {
			out = append(out, finding{"config", problem, "[compose.ports] " + key, fmt.Sprintf("names service %s, which is not a service of the compose project", svc), nil})
		}
	}
	volumes := own.VolumeKeys()
	for _, key := range sortedKeys(cfg.Compose.Volumes) {
		if !slices.Contains(volumes, key) {
			out = append(out, finding{"config", problem, "[compose.volumes] " + key, fmt.Sprintf("names volume %s, which the compose project doesn't declare", key), nil})
		}
	}
	return out
}

// buildEnvFindings warns about managed keys bundlers inline into build output that the
// berth sets differently from main. Build output cloned or copied from main still holds
// main's value, and berth never rewrites it.
func buildEnvFindings(w *worktree.Worktree, cfg *config.Config, rec registry.Record) []finding {
	mainEnv, err := dotenv.Read(filepath.Join(w.Repository, ".env"))
	if err != nil {
		mainEnv = map[string]string{}
	}
	env := managedEnv(cfg, rec)
	var keys []string
	for _, k := range sortedKeys(env) {
		if envfile.BuildTime(k) && env[k] != mainEnv[k] {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	return []finding{{"build_env", warning, ".env (managed block)", fmt.Sprintf("the berth sets %s to values that differ from main's, and bundlers inline these keys into build output when it is built. Build output copied from main still carries main's values; build it again in this berth", strings.Join(keys, ", ")), nil}}
}

// ignoredFileMax bounds the gitignored files ignoredFileFindings mentions: local config
// is small, while large ignored files are lockfiles, dumps and caches.
const ignoredFileMax = 64 << 10

// ignoredFileNoise are extensions of gitignored files that are logs and caches, never
// config a berth needs.
var ignoredFileNoise = []string{".log", ".cache", ".pid", ".tmp", ".swp", ".bak", ".orig"}

// ignoredFileFindings warns about small gitignored files at the top of main that the
// berth lacks and [files] clone doesn't list. Local config such as a test runner's or a
// package manager's lives there, and a berth without it quietly falls back to defaults.
func ignoredFileFindings(w *worktree.Worktree, cfg *config.Config) []finding {
	listed := map[string]bool{}
	for _, f := range cfg.Files.Clone {
		listed[filepath.ToSlash(filepath.Clean(f))] = true
	}
	var found []finding
	for _, name := range smallIgnoredFiles(w.Repository) {
		if listed[name] {
			continue
		}
		if _, err := os.Lstat(filepath.Join(w.Path, name)); err == nil {
			continue
		}
		found = append(found, finding{"ignored_file", warning, name, fmt.Sprintf("main has the gitignored file %s, which this berth lacks and [files] clone doesn't list; if the berth needs it, add it to [files] clone and run berth add", name), nil})
	}
	return found
}

// smallIgnoredFiles lists the small gitignored files at the top of dir that could be
// local config. Dotfiles are left out: berth clones .env* itself, and the rest are tool
// caches.
func smallIgnoredFiles(dir string) []string {
	out, err := worktree.GitAt(dir, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		return nil
	}
	var files []string
	for _, name := range strings.Split(out, "\x00") {
		if name == "" || strings.Contains(name, "/") || strings.HasPrefix(name, ".") || strings.HasSuffix(name, "~") ||
			slices.Contains(ignoredFileNoise, filepath.Ext(name)) || name == config.LocalName {
			continue
		}
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() > ignoredFileMax {
			continue
		}
		files = append(files, name)
	}
	return files
}

// copyOnWriteFindings warns when the dependency directories [files] clone lists will be
// copied byte for byte, so a slow add is expected rather than a surprise. It tries a
// clone from main into the worktree, since the filesystem's name alone doesn't say: XFS
// can be made without reflink, and main and the berth can sit on different filesystems.
func copyOnWriteFindings(w *worktree.Worktree, cfg *config.Config) []finding {
	var deps []string
	for _, rel := range cfg.Files.Clone {
		if initconf.DependencyDir(rel) {
			deps = append(deps, rel)
		}
	}
	if len(deps) == 0 {
		return nil
	}
	into := "this berth"
	if w.IsMain() {
		into = "main's own directory"
	}
	s, err := clone.Probe(w.Repository, w.Path)
	if err != nil {
		return []finding{{"copy_on_write", notVerified, w.Path, fmt.Sprintf("could not try a copy-on-write clone from main into %s: %v", into, err), nil}}
	}
	if s.Cow {
		return nil
	}
	list := strings.Join(deps, ", ")
	if s.Source != "" && s.Dest != "" && s.Source != s.Dest {
		return []finding{{"copy_on_write", warning, w.Path, fmt.Sprintf("main is on %s and this berth on %s: dependency clones (%s) will be full copies", s.Source, s.Dest, list), nil}}
	}
	fsName := s.Dest
	if fsName == "" {
		fsName = "this filesystem"
	}
	message := fmt.Sprintf("%s: dependency clones (%s) will be full copies, as a copy-on-write clone from main into %s is not possible", fsName, list, into)
	if hint := clone.FullCopyHint(w.Path); hint != "" {
		message += "; " + hint
	}
	return []finding{{"copy_on_write", warning, w.Path, message, nil}}
}

// strayPersonalFindings warns about a berth.local.toml in a berth, which berth never
// reads: the personal layer is main's, and a user may expect one per worktree as with
// .env.local.
func strayPersonalFindings(w *worktree.Worktree) []finding {
	stray := filepath.Join(w.Path, config.LocalName)
	if _, err := os.Lstat(stray); err != nil {
		return nil
	}
	personal, _, err := config.Personal(w)
	if err != nil {
		return nil
	}
	return []finding{{"personal_config", warning, stray, fmt.Sprintf("berth reads the personal config from main only, so this file has no effect; move its keys to %s", personal), nil}}
}

// containerNameAt finds the line that sets container_name to name, for a location a
// reader can open.
func containerNameAt(dir string, files []string, name string) string {
	re := regexp.MustCompile(`^\s*container_name:\s*["']?` + regexp.QuoteMeta(name) + `["']?\s*(#.*)?$`)
	for _, f := range files {
		path := f
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, f)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				return fmt.Sprintf("%s:%d", f, i+1)
			}
		}
	}
	return strings.Join(files, ", ")
}

// portFindings reports each assigned port something other than the berth listens on.
// The berth's own containers publishing it are fine, and so is a process running in
// the worktree for a [ports] entry.
func portFindings(w *worktree.Worktree, rec registry.Record) []finding {
	var own map[int]bool
	var out []finding
	for _, p := range rec.Ports {
		if p.Policy != ports.Assign || p.HostPort == 0 || ports.Free(p.HostPort) {
			continue
		}
		where := fmt.Sprintf("%s (port %d)", p.Name, p.HostPort)
		if p.Service != "" {
			if own == nil {
				own = map[int]bool{}
				if published, err := docker.PublishedPorts(rec.ComposeProject); err == nil {
					for _, hp := range published {
						own[hp] = true
					}
				}
			}
			if own[p.HostPort] {
				continue
			}
		}
		procs, err := listeners(p.HostPort)
		if err != nil || len(procs) == 0 {
			if p.Service == "" {
				out = append(out, finding{"ports", notVerified, where, fmt.Sprintf("port %d assigned to %s is in use, and berth couldn't tell by which process: %v", p.HostPort, p.Name, err), nil})
			} else {
				out = append(out, finding{"ports", problem, where, fmt.Sprintf("port %d assigned to %s is in use by another process, so docker compose up can't publish it", p.HostPort, p.Name), nil})
			}
			continue
		}
		if p.Service == "" && slices.ContainsFunc(procs, func(pr listener) bool { return within(w.Path, pr.cwd) }) {
			continue
		}
		var who []string
		for _, pr := range procs {
			who = append(who, fmt.Sprintf("%s (pid %d)", pr.command, pr.pid))
		}
		msg := fmt.Sprintf("port %d assigned to %s is in use by %s", p.HostPort, p.Name, strings.Join(who, ", "))
		if p.Service != "" {
			msg += ", so docker compose up can't publish it"
		}
		if slices.ContainsFunc(procs, func(pr listener) bool {
			return strings.Contains(pr.command, "docker") || strings.Contains(pr.command, "vpnkit") || strings.Contains(pr.command, "wslrelay")
		}) {
			msg += fmt.Sprintf("; a container publishes it, which docker ps --filter publish=%d names", p.HostPort)
		}
		out = append(out, finding{"ports", problem, where, msg, nil})
	}
	return out
}

type listener struct {
	pid     int
	command string
	cwd     string
}

func within(dir, path string) bool {
	if path == "" {
		return false
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// sourceForms are the ways a file can spell the source's absolute path: the path
// itself, the same path through an alias that resolves to it (/var for /private/var on
// macOS), each with forward slashes and with backslashes, since tools on Windows write
// either, and with JSON's escaping of each.
func sourceForms(source string) []string {
	bases := []string{source}
	if rest, ok := strings.CutPrefix(source, "/private/"); ok {
		if real, err := filepath.EvalSymlinks("/" + rest); err == nil && real == source {
			bases = append(bases, "/"+rest)
		}
	}
	var out []string
	for _, b := range bases {
		b = filepath.ToSlash(b)
		out = append(out, b, strings.ReplaceAll(b, "/", `\/`), strings.ReplaceAll(b, "/", `\`), strings.ReplaceAll(b, "/", `\\`))
	}
	return union(nil, out)
}

// sourcePathFindings searches the text files berth cloned into the berth for the
// source's path. Binary files are skipped: rewriting them is never safe, and a path in
// one can't be told from data.
func sourcePathFindings(w *worktree.Worktree, rec registry.Record) []finding {
	needles := sourceForms(w.Repository)
	// A path that continues into the berth's own location is the berth, not the source:
	// a worktree may sit inside the main one.
	selfForms := sourceForms(w.Path)
	var files []string
	for _, f := range rec.Files {
		if f == OverrideFile {
			continue
		}
		_ = filepath.WalkDir(filepath.Join(w.Path, f), func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				files = append(files, p)
			}
			return nil
		})
	}

	jobs := make(chan string)
	var mu sync.Mutex
	var out []finding
	var wg sync.WaitGroup
	for range runtime.NumCPU() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				if line, form, ok := findSource(p, needles, selfForms); ok {
					rel, _ := filepath.Rel(w.Path, p)
					rel = filepath.ToSlash(rel)
					mu.Lock()
					out = append(out, finding{"source_path", problem, fmt.Sprintf("%s:%d", rel, line),
						fmt.Sprintf("%s contains %s (the source path), so it may read or write main's files; leave it out of [files] clone or regenerate it in post-add", rel, form), nil})
					mu.Unlock()
				}
			}
		}()
	}
	for _, p := range files {
		jobs <- p
	}
	close(jobs)
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].Location < out[j].Location })
	return out
}

// findSource reports the first line of the file at path holding one of needles as a
// whole path, not as the start of a longer name or of one of the self paths.
func findSource(path string, needles, self []string) (line int, form string, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil || bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		return 0, "", false
	}
	data = foldPathCase(data)
	best := -1
	for _, n := range needles {
		for from := 0; ; {
			i := bytes.Index(data[from:], foldPathCase([]byte(n)))
			if i < 0 {
				break
			}
			at := from + i
			from = at + 1
			if !pathEnds(data, at+len(n)) || slices.ContainsFunc(self, func(s string) bool {
				return bytes.HasPrefix(data[at:], foldPathCase([]byte(s))) && len(s) > len(n)
			}) {
				continue
			}
			if best < 0 || at < best {
				best, form = at, n
			}
			break
		}
	}
	if best < 0 {
		return 0, "", false
	}
	return bytes.Count(data[:best], []byte("\n")) + 1, form, true
}

// foldPathCase lowers ASCII letters on Windows, where paths ignore case and tools often
// lower the drive letter; elsewhere case is significant and b is returned as it is.
func foldPathCase(b []byte) []byte {
	if runtime.GOOS != "windows" {
		return b
	}
	out := bytes.Clone(b)
	for i, c := range out {
		if 'A' <= c && c <= 'Z' {
			out[i] = c + 'a' - 'A'
		}
	}
	return out
}

func pathEnds(data []byte, at int) bool {
	if at >= len(data) {
		return true
	}
	c := data[at]
	return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_')
}
