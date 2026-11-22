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
	"strings"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/clone"
	"github.com/kodnificent/berth/internal/compose"
	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/docker"
	"github.com/kodnificent/berth/internal/ports"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/worktree"
)

// OverrideFile is the compose file berth writes in each berth, applied last.
const OverrideFile = "compose.berth.yml"

// composePlan is how a berth's compose project is made from main's.
type composePlan struct {
	main *compose.Project
	// own is the berth's project before berth's own override, nil when it has none.
	own *compose.Project
	// files are COMPOSE_FILE's entries before the override, relative to the worktree.
	files []string
	// clone are main's gitignored compose files the worktree lacks.
	clone []string
	sep   string
}

// planCompose picks the berth's compose files the way docker compose would pick them in
// main: COMPOSE_FILE when main's environment sets it, otherwise compose's default names.
// Compose files git ignores, such as a private compose.override.yml, exist only in main,
// so they are cloned like env files. [compose] files come after.
func planCompose(w *worktree.Worktree, cfg *config.Config) (*composePlan, error) {
	main, err := compose.Load(w.Repository, compose.Options{ComposeFileEnv: true})
	if err != nil {
		return nil, err
	}
	plan := &composePlan{main: main, sep: string(os.PathListSeparator)}

	var candidates []string
	if main != nil && main.FilesFromEnv {
		plan.sep = main.PathSeparator
		for _, f := range main.Files {
			candidates = append(candidates, relativeTo(w.Repository, f))
		}
	} else {
		candidates = append(append(candidates, compose.DefaultFileNames...), compose.DefaultOverrideFileNames...)
	}
	cloning := map[string]bool{}
	var lacking []string
	for _, f := range candidates {
		if !filepath.IsAbs(f) && !fileExists(filepath.Join(w.Path, f)) && fileExists(filepath.Join(w.Repository, f)) {
			lacking = append(lacking, f)
		}
	}
	if plan.clone, err = ignoredIn(w.Repository, lacking); err != nil {
		return nil, err
	}
	for _, f := range plan.clone {
		cloning[f] = true
	}
	present := func(f string) bool { return filepath.IsAbs(f) || cloning[f] || fileExists(filepath.Join(w.Path, f)) }

	if main != nil && main.FilesFromEnv {
		plan.files = candidates
	} else {
		for _, names := range [][]string{compose.DefaultFileNames, compose.DefaultOverrideFileNames} {
			for _, n := range names {
				if present(n) {
					plan.files = append(plan.files, n)
					break
				}
			}
		}
	}
	plan.files = append(plan.files, cfg.Compose.Files...)
	if len(plan.files) == 0 {
		return plan, nil
	}

	load := make([]string, len(plan.files))
	for i, f := range plan.files {
		load[i] = f
		if cloning[f] {
			load[i] = filepath.Join(w.Repository, f)
		}
	}
	opts := compose.Options{EnvFiles: envFiles(w), Files: load}
	if first := plan.files[0]; cloning[first] {
		opts.ProjectDir = filepath.Dir(first)
	}
	plan.own, err = compose.Load(w.Path, opts)
	return plan, err
}

const composeUpdate = "update Docker Compose to " + compose.MinVersion + " or later, from a newer Docker Desktop or the compose plugin at https://github.com/docker/compose/releases"

func composeTooOldMessage(version string) string {
	return fmt.Sprintf("docker compose %s misreads %s: before %s, compose publishes main's ports in a berth as well as the berth's own, so the berth collides with main; berth needs Docker Compose %s or later",
		version, OverrideFile, compose.MinVersion, compose.MinVersion)
}

// A docker berth can't run, or a compose whose answer isn't a version, doesn't block add:
// compose names its own problem when it runs, and doctor reports it as not verified.
func composeTooOld(version string, err error) error {
	if err != nil {
		return nil
	}
	if reads, known := compose.ReadsOverride(version); !known || reads {
		return nil
	}
	return apperr.New(apperr.ComposeTooOld, "%s", composeTooOldMessage(version)).WithNext(composeUpdate + ", then run berth add again")
}

func checkComposeVersion() error { return composeTooOld(docker.ComposeVersion()) }

func composeVersionFindings(version string, err error) []finding {
	const where = "docker compose version"
	reads, known := compose.ReadsOverride(version)
	switch {
	case err != nil:
		return []finding{{"compose_version", notVerified, where, fmt.Sprintf("could not run docker compose version, so berth can't tell whether compose reads %s; berth needs Docker Compose %s or later: %v", OverrideFile, compose.MinVersion, err), nil}}
	case !known:
		return []finding{{"compose_version", notVerified, where, fmt.Sprintf("docker compose version printed %q, which berth can't read as a version; berth needs Docker Compose %s or later", version, compose.MinVersion), nil}}
	case !reads:
		return []finding{{"compose_version", problem, where, composeTooOldMessage(version) + "; " + composeUpdate, nil}}
	}
	return nil
}

// composeFile is the berth's COMPOSE_FILE: its files, then berth's override.
func (p *composePlan) composeFile() string {
	if p.own == nil {
		return ""
	}
	return strings.Join(append(append([]string{}, p.files...), OverrideFile), p.sep)
}

// writeCompose clones main's ignored compose files and writes the berth's override,
// returning the paths it created.
func writeCompose(w *worktree.Worktree, cfg *config.Config, plan *composePlan, rec *registry.Record, refused *refusals) ([]string, error) {
	var created []string
	for _, rel := range plan.clone {
		r, err := clone.Merge(w.Repository, w.Path, rel)
		created = append(created, r.Created...)
		if refused.add(rel, err) {
			continue
		}
		if err != nil {
			return created, fmt.Errorf("cloning %s: %w", rel, err)
		}
	}
	rec.ComposeFile = plan.composeFile()
	if plan.own == nil {
		return created, nil
	}

	hostPorts := map[string]int{}
	for _, p := range rec.Ports {
		if p.Service == "" {
			continue
		}
		switch p.Policy {
		case ports.Assign:
			hostPorts[p.Name] = p.HostPort
		case ports.Ephemeral:
			hostPorts[p.Name] = 0
		case ports.None:
			hostPorts[p.Name] = compose.Unpublished
		}
	}
	out := plan.own.Override(compose.HostSide{
		Project: rec.ComposeProject,
		Ports:   hostPorts,
		Exclude: cfg.Compose.Exclude,
		Share:   sharedVolumes(cfg, plan),
	})

	path := filepath.Join(w.Path, OverrideFile)
	old, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		created = append(created, OverrideFile)
	case err != nil:
		return created, err
	case bytes.Equal(old, out):
		return created, nil
	}
	return created, writeFileAtomic(path, out, 0o644)
}

// sharedVolumes maps each share volume to main's volume of the same key. A volume main
// doesn't declare, or that is external already, has nothing to share.
func sharedVolumes(cfg *config.Config, plan *composePlan) map[string]string {
	out := map[string]string{}
	if plan.main == nil {
		return out
	}
	for key, role := range cfg.Compose.Volumes {
		if role != "share" {
			continue
		}
		if name, external, ok := plan.main.Volume(key); ok && !external {
			out[key] = name
		}
	}
	return out
}

// ensureShared creates main's share volumes that don't exist yet, labelled as main's
// compose project would label them, so the berth's external reference finds them and
// main later adopts them as its own. Docker being unreachable is not an error: compose
// will name the missing volume when it runs.
func ensureShared(stderr io.Writer, cfg *config.Config, plan *composePlan) {
	for key, name := range sharedVolumes(cfg, plan) {
		exists, err := docker.VolumeExists(name)
		if err == nil && !exists {
			driver, opts, labels := plan.main.VolumeSpec(key)
			all := map[string]string{"com.docker.compose.project": plan.main.Name, "com.docker.compose.volume": key}
			for k, v := range labels {
				all[k] = v
			}
			err = docker.CreateVolume(name, driver, opts, all)
		}
		if err != nil {
			fmt.Fprintf(stderr, "berth: could not check or create main's volume %s for share volume %s: %v\n", name, key, err)
		}
	}
}

// createFresh creates the berth's own empty volumes, every one its project declares
// that isn't external, shared or cloned, labelled as the berth's compose would label
// them, and records them, so removal deletes volumes berth knows it made. Compose
// adopts them on up. A volume that already exists and isn't recorded is left alone.
// Docker being unreachable is not an error: compose creates them on up instead.
func createFresh(stderr io.Writer, cfg *config.Config, plan *composePlan, rec *registry.Record) {
	if plan.own == nil {
		return
	}
	var keys []string
	for _, key := range plan.own.VolumeKeys() {
		if _, external, _ := plan.own.Volume(key); external {
			continue
		}
		if role := cfg.Compose.Volumes[key]; role == "share" || role == "clone" {
			continue
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return
	}
	existing, err := docker.Volumes()
	if err != nil {
		fmt.Fprintf(stderr, "berth: could not reach Docker to create the berth's volumes, so compose creates them on up and berth remove won't know them: %v\n", err)
		return
	}
	for _, key := range keys {
		name := rec.ComposeProject + "_" + key
		if slices.Contains(rec.Volumes, name) {
			continue
		}
		if existing[name] {
			fmt.Fprintf(stderr, "berth: volume %s already exists and berth didn't create it; the berth uses it, and berth remove leaves it\n", name)
			continue
		}
		driver, opts, labels := plan.own.VolumeSpec(key)
		all := map[string]string{"com.docker.compose.project": rec.ComposeProject, "com.docker.compose.volume": key}
		for k, v := range labels {
			all[k] = v
		}
		if err := docker.CreateVolume(name, driver, opts, all); err != nil {
			fmt.Fprintf(stderr, "berth: could not create the berth's volume %s, so compose creates it on up and berth remove won't know it: %v\n", name, err)
			continue
		}
		rec.Volumes = union(rec.Volumes, []string{name})
	}
}

// ignoredIn lists the paths among rels that git ignores in dir.
func ignoredIn(dir string, rels []string) ([]string, error) {
	if len(rels) == 0 {
		return nil, nil
	}
	out, err := worktree.GitAt(dir, append([]string{"--literal-pathspecs", "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--"}, rels...)...)
	if err != nil {
		return nil, fmt.Errorf("listing ignored compose files in %s: %w", dir, err)
	}
	ignored := map[string]bool{}
	for _, p := range strings.Split(out, "\x00") {
		ignored[filepath.FromSlash(p)] = true
	}
	var keep []string
	for _, r := range rels {
		if ignored[r] {
			keep = append(keep, r)
		}
	}
	return keep, nil
}

// relativeTo makes path relative to dir when it lies inside it, so it names the same
// file in a worktree; a path outside stays absolute.
func relativeTo(dir, path string) string {
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return rel
}

// ephemeralPorts fills in the host port Docker picked for each running ephemeral port.
func ephemeralPorts(rec registry.Record) []ports.Port {
	out := append([]ports.Port{}, rec.Ports...)
	var want bool
	for _, p := range out {
		want = want || (p.Policy == ports.Ephemeral && p.Service != "")
	}
	if !want || rec.ComposeProject == "" {
		return out
	}
	// Docker picks a new port on every container start, so the answer is never recorded.
	picked, err := docker.PublishedPorts(rec.ComposeProject)
	if err != nil {
		return out
	}
	for i, p := range out {
		if p.Policy == ports.Ephemeral {
			out[i].HostPort = picked[p.Name]
		}
	}
	return out
}
