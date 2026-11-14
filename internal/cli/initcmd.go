package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/compose"
	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/initconf"
	"github.com/kodnificent/berth/internal/ports"
	"github.com/kodnificent/berth/internal/preset"
	"github.com/kodnificent/berth/internal/worktree"
)

const initUsage = "Usage: berth init [--private] [-n|--dry-run] [--preset <name>|--no-presets] [path]\n\n" +
	"Writes berth.toml in the main worktree from what berth detects there: compose ports and\n" +
	"volumes, dependency directories and env files. Settings that need your intent are\n" +
	"written as commented suggestions. Presets for the framework a repository uses add their\n" +
	"suggestions under a comment naming them. An existing config is never overwritten.\n\n" +
	"Flags:\n  --private  write the personal berth.local.toml instead, kept out of git\n  -n, --dry-run\n             print the file instead of writing it\n" +
	"  --preset <name>\n             apply that preset alone\n  --no-presets\n             apply none\n"

func init() {
	commands = append(commands, Command{Name: "init", Summary: "write a starting config detected from the main worktree", Usage: initUsage, Run: runInit})
}

func runInit(ctx *Context, args []string) error {
	var private, dryRun, noPresets, named bool
	var presetName string
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		value, isPreset := strings.CutPrefix(a, "--preset=")
		switch {
		case a == "--private":
			private = true
		case a == "-n" || a == "--dry-run":
			dryRun = true
		case a == "--no-presets":
			noPresets = true
		case a == "--preset" || isPreset:
			if !isPreset && i+1 < len(args) {
				i++
				value = args[i]
			}
			if value == "" {
				return apperr.New(apperr.Usage, "--preset needs a preset's name")
			}
			if named {
				return apperr.New(apperr.Usage, "berth init applies one preset by name; give --preset once")
			}
			named, presetName = true, value
		default:
			rest = append(rest, a)
		}
	}
	if named && noPresets {
		return apperr.New(apperr.Usage, "--preset and --no-presets contradict each other; use one")
	}
	p, err := pathArg("init", rest)
	if err != nil {
		return err
	}
	w, err := worktree.Resolve(p)
	if err != nil {
		return err
	}
	if !w.IsMain() {
		return apperr.New(apperr.Usage, "%s is a linked worktree, and berth init writes the main worktree's config; run it in %s", w.Path, w.Repository)
	}

	path := config.RepoPath(w)
	exists := fileExists(path)
	if private {
		if path, exists, err = config.Personal(w); err != nil {
			return err
		}
	}
	if exists {
		return apperr.New(apperr.ConfigExists, "%s already exists, and berth init never overwrites a config; edit it, or move it away and run berth init again", path)
	}

	in, err := detect(w, private)
	if err != nil {
		return err
	}
	if !noPresets {
		if in.Presets, err = preset.Apply(presetName, presetRepo(w.Repository, in)); err != nil {
			return err
		}
	}
	content := initconf.Render(in)
	if _, err := config.Parse(path, []byte(content)); err != nil {
		return fmt.Errorf("berth init made a config its own loader refuses, which is a bug in berth: %w", err)
	}

	if dryRun {
		if ctx.JSON {
			return writeJSON(ctx.Stdout, map[string]any{"path": path, "written": false, "config": content})
		}
		_, err := fmt.Fprint(ctx.Stdout, content)
		return err
	}

	if err := writeNew(path, content); err != nil {
		return err
	}
	if filepath.Base(path) == config.LocalName {
		if err := exclude(w, []string{config.LocalName}); err != nil {
			return err
		}
	}

	var found []finding
	if cfg, err := config.Load(w); err != nil {
		if apperr.CodeOf(err) != apperr.ConfigInvalid {
			return err
		}
		found = configFindings(err)
	} else {
		found = mainFindings(w, cfg)
	}
	if found == nil {
		found = []finding{}
	}
	if ctx.JSON {
		return writeJSON(ctx.Stdout, map[string]any{"path": path, "written": true, "config": content, "findings": found})
	}
	fmt.Fprintf(ctx.Stdout, "Wrote %s\n", path)
	if !private {
		fmt.Fprintln(ctx.Stdout, "Commit it, so new worktrees have it.")
	}
	fmt.Fprintln(ctx.Stdout, "\nberth doctor in the main worktree:")
	printFindings(ctx, found)
	return nil
}

func presetRepo(root string, in initconf.Input) preset.Repo {
	var published []int
	for _, p := range in.Ports {
		published = append(published, p.HostPorts...)
	}
	return preset.Repo{Root: root, Env: in.Env, ComposeVars: in.ComposeVars, Services: in.Services, Published: published}
}

// writeNew creates path with content, failing rather than replacing a file that
// appeared since the check.
func writeNew(path, content string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return apperr.New(apperr.ConfigExists, "%s already exists, and berth init never overwrites a config", path)
	}
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

var composeVar = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)`)

// detect gathers what init writes from main: its compose project as a berth would read
// it, its files and its .env.
func detect(w *worktree.Worktree, private bool) (initconf.Input, error) {
	in := initconf.Input{Private: private, ComposeVars: map[string]bool{}, IgnoredDirs: map[string]bool{}, VolumeDirs: map[string]string{}}
	empty, err := config.LoadFiles()
	if err != nil {
		return in, err
	}
	plan, err := planCompose(w, empty)
	if err != nil {
		return in, err
	}
	main := plan.main
	root := w.Repository

	if main != nil {
		for _, f := range main.Files {
			in.ComposeFiles = append(in.ComposeFiles, relativeTo(root, f))
			if data, err := os.ReadFile(f); err == nil {
				for _, m := range composeVar.FindAllStringSubmatch(string(data), -1) {
					in.ComposeVars[m[1]] = true
				}
			}
		}
		hostPorts := map[string][]int{}
		for _, p := range main.Ports {
			key := fmt.Sprintf("%s:%d", p.Service, p.Target)
			hostPorts[key] = append(hostPorts[key], p.HostPorts()...)
		}
		for _, p := range ports.Wanted(empty, main) {
			if p.Service != "" {
				in.Ports = append(in.Ports, initconf.Port{Service: p.Service, Target: p.ContainerPort, Policy: p.Policy, HostPorts: hostPorts[p.Name]})
			}
		}
		mounts := main.Mounts()
		for _, key := range main.VolumeKeys() {
			_, external, _ := main.Volume(key)
			v := initconf.Volume{Key: key, External: external}
			driver, opts, _ := main.VolumeSpec(key)
			if driver != "" && driver != "local" {
				v.Uncopyable = fmt.Sprintf("uses the %s driver, which berth can't copy", driver)
			} else if dev, ok := opts["device"]; ok {
				v.Uncopyable = fmt.Sprintf("mounts %s, which a copy would share with main", dev)
			}
			for _, m := range mounts {
				if m.Type == "volume" && m.Source == key {
					v.Mounts = append(v.Mounts, m.Target)
				}
			}
			in.Volumes = append(in.Volumes, v)
		}
		in.VolumeDirs = volumeDirs(root, mounts)
		in.Profiles = main.Profiles()
		in.Services = main.ServiceNames()
	}

	if in.OtherCompose, err = otherComposeFiles(root, in.ComposeFiles); err != nil {
		return in, err
	}
	if in.EnvFiles, err = ignoredEnvFiles(root); err != nil {
		return in, err
	}
	if env, err := dotenv.Read(filepath.Join(root, ".env")); err == nil {
		in.Env = env
	}

	tracked, err := worktree.GitAt(root, append([]string{"--literal-pathspecs", "ls-files", "-z", "--"}, initconf.LockfileNames()...)...)
	if err != nil {
		return in, fmt.Errorf("listing lockfiles in %s: %w", root, err)
	}
	for _, f := range strings.Split(tracked, "\x00") {
		if f != "" {
			in.Lockfiles = append(in.Lockfiles, f)
		}
	}
	ignored, err := worktree.GitAt(root, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		return in, fmt.Errorf("listing ignored files in %s: %w", root, err)
	}
	for _, f := range strings.Split(ignored, "\x00") {
		if dir, ok := strings.CutSuffix(f, "/"); ok && !strings.Contains(dir, "/") {
			in.IgnoredDirs[dir] = true
		}
	}
	for _, f := range smallIgnoredFiles(root) {
		if f != "berth.toml" {
			in.SmallFiles = append(in.SmallFiles, f)
		}
	}
	return in, nil
}

// volumeDirs maps each top-level directory of root to the named volume a service
// mounts over it, inside a bind mount of root itself. That service never sees the
// host's copy, so cloning it would only cost disk.
func volumeDirs(root string, mounts []compose.Mount) map[string]string {
	out := map[string]string{}
	for _, bind := range mounts {
		if bind.Type != "bind" || realpath(bind.Source) != root {
			continue
		}
		for _, m := range mounts {
			if m.Service != bind.Service || m.Type != "volume" {
				continue
			}
			rel, ok := strings.CutPrefix(m.Target, strings.TrimSuffix(bind.Target, "/")+"/")
			if ok && rel != "" && !strings.Contains(rel, "/") {
				out[rel] = m.Source
			}
		}
	}
	return out
}

func realpath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// otherComposeFiles lists compose files in root and one directory down that main's
// project doesn't read and berth didn't write, such as a compose file meant for
// worktrees only.
func otherComposeFiles(root string, used []string) ([]string, error) {
	out, err := worktree.GitAt(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "*compose*.yml", "*compose*.yaml")
	if err != nil {
		return nil, fmt.Errorf("listing compose files in %s: %w", root, err)
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		f = filepath.FromSlash(f)
		if f == "" || strings.Count(f, string(filepath.Separator)) > 1 || filepath.Base(f) == OverrideFile || slices.Contains(used, f) || slices.Contains(files, f) {
			continue
		}
		files = append(files, f)
	}
	return files, nil
}
