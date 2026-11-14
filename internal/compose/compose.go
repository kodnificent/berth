// Package compose reads a directory's compose project the way docker compose resolves
// it, using compose-go, the library compose itself is built on.
package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
)

type Port struct {
	Service string
	Target  int
	// Published is the host side as compose resolved it: empty when Docker picks the
	// port, a number, or a range such as "8000-8010".
	Published string
}

// HostPorts lists the fixed host ports p publishes.
func (p Port) HostPorts() []int {
	lo, hi, isRange := strings.Cut(p.Published, "-")
	a, err := strconv.Atoi(lo)
	if err != nil {
		return nil
	}
	b := a
	if isRange {
		if b, err = strconv.Atoi(hi); err != nil || b < a {
			return nil
		}
	}
	out := make([]int, 0, b-a+1)
	for n := a; n <= b; n++ {
		out = append(out, n)
	}
	return out
}

type Project struct {
	Name  string
	Ports []Port
	// Files are the compose files the project was read from, in order.
	Files []string
	// FilesFromEnv is true when COMPOSE_FILE chose Files.
	FilesFromEnv bool
	// PathSeparator splits COMPOSE_FILE for this project.
	PathSeparator string
	model         *types.Project
}

type Options struct {
	// EnvFiles replace dir's own .env for interpolation.
	EnvFiles []string
	// Files, relative to dir, are read instead of the files compose would find there.
	Files []string
	// ComposeFileEnv lets COMPOSE_FILE from the environment or .env choose the files,
	// as it would for docker compose run in dir.
	ComposeFileEnv bool
	// ProjectDir, relative to dir, is the folder the first file belongs in when it is
	// read from somewhere else.
	ProjectDir string
}

// Load reads the compose project in dir with every profile active, so a service under
// a profile still counts. It returns nil when dir has no compose file.
func Load(dir string, o Options) (*Project, error) {
	opts, err := cli.NewProjectOptions(nil,
		cli.WithWorkingDirectory(dir),
		cli.WithOsEnv,
		cli.WithEnvFiles(o.EnvFiles...),
		cli.WithDotEnv,
		cli.WithProfiles([]string{"*"}),
		cli.WithLoadOptions(func(l *loader.Options) {
			// Only ports matter here, and a new worktree may not have the env files or
			// build contexts its services name yet.
			l.SkipResolveEnvironment = true
			l.SkipConsistencyCheck = true
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("reading the compose environment in %s: %w", dir, err)
	}
	sep := opts.Environment["COMPOSE_PATH_SEPARATOR"]
	if sep == "" {
		sep = string(os.PathListSeparator)
	}
	// Not cli.WithConfigFileEnv: it resolves relative entries against berth's own working
	// directory, where docker compose run in dir resolves them against dir.
	if f := opts.Environment["COMPOSE_FILE"]; o.ComposeFileEnv && f != "" {
		for _, entry := range strings.Split(f, sep) {
			if !filepath.IsAbs(entry) {
				entry = filepath.Join(dir, entry)
			}
			opts.ConfigPaths = append(opts.ConfigPaths, entry)
		}
	}
	fromEnv := len(opts.ConfigPaths) > 0
	for _, f := range o.Files {
		if !filepath.IsAbs(f) {
			f = filepath.Join(dir, f)
		}
		opts.ConfigPaths = append(opts.ConfigPaths, f)
	}
	if len(opts.ConfigPaths) == 0 {
		// cli.WithDefaultConfigPath also searches parent directories, which would find the
		// main worktree's files from a worktree nested inside it.
		opts.ConfigPaths = DefaultFiles(dir)
		if len(opts.ConfigPaths) == 0 {
			return nil, nil
		}
	}

	// docker compose names an unnamed project after its first file's folder and resolves
	// relative paths there, while still reading .env from the directory it runs in.
	opts.WorkingDir = filepath.Dir(opts.ConfigPaths[0])
	if o.ProjectDir != "" {
		opts.WorkingDir = filepath.Join(dir, o.ProjectDir)
	}

	p, err := opts.LoadProject(context.Background())
	if err != nil {
		return nil, fmt.Errorf("reading the compose project in %s: %w", dir, err)
	}
	out := &Project{Name: p.Name, Files: opts.ConfigPaths, FilesFromEnv: fromEnv, PathSeparator: sep, model: p}
	for _, name := range p.ServiceNames() {
		for _, port := range p.Services[name].Ports {
			out.Ports = append(out.Ports, Port{Service: name, Target: int(port.Target), Published: port.Published})
		}
	}
	return out, nil
}

// DefaultFiles are the files docker compose reads in dir when nothing names them: the
// first default file name present, then the first override file name present.
func DefaultFiles(dir string) []string {
	return append(find(dir, DefaultFileNames), find(dir, DefaultOverrideFileNames)...)
}

// DefaultFileNames and DefaultOverrideFileNames are the names docker compose looks for,
// in order, when nothing names the files.
var (
	DefaultFileNames         = cli.DefaultFileNames
	DefaultOverrideFileNames = cli.DefaultOverrideFileNames
)

func find(dir string, candidates []string) []string {
	for _, c := range candidates {
		if info, err := os.Stat(filepath.Join(dir, c)); err == nil && info.Mode().IsRegular() {
			return []string{filepath.Join(dir, c)}
		}
	}
	return nil
}

// Build is one service's image build as compose would run it.
type Build struct {
	Service string
	// Image is the name compose gives the image it builds.
	Image string
	// Context is an absolute directory, or a remote context as written.
	Context string
	// Dockerfile is the Dockerfile's path, or "" when DockerfileInline holds it.
	Dockerfile       string
	DockerfileInline string
	Target           string
	// AdditionalContexts are named contexts, local ones as absolute directories.
	AdditionalContexts map[string]string
	// Spec is the whole build section with root stripped from every path, so the same
	// section in two worktrees gives the same bytes.
	Spec []byte
}

// Builds lists the services p builds, with paths under root made comparable to another
// worktree's.
func (p *Project) Builds(root string) []Build {
	var out []Build
	for _, name := range p.model.ServiceNames() {
		s := p.model.Services[name]
		if s.Build == nil {
			continue
		}
		b := Build{
			Service:            name,
			Image:              ImageName(p.Name, name, s.Image),
			Context:            s.Build.Context,
			DockerfileInline:   s.Build.DockerfileInline,
			Target:             s.Build.Target,
			AdditionalContexts: s.Build.AdditionalContexts,
		}
		if b.DockerfileInline == "" {
			b.Dockerfile = s.Build.Dockerfile
			if b.Dockerfile == "" {
				b.Dockerfile = "Dockerfile"
			}
			if !filepath.IsAbs(b.Dockerfile) && filepath.IsAbs(b.Context) {
				b.Dockerfile = filepath.Join(b.Context, b.Dockerfile)
			}
		}
		spec, _ := json.Marshal(s.Build)
		quoted, _ := json.Marshal(root + string(filepath.Separator))
		b.Spec = bytes.ReplaceAll(spec, quoted[1:len(quoted)-1], nil)
		quoted, _ = json.Marshal(root)
		b.Spec = bytes.ReplaceAll(b.Spec, quoted[1:len(quoted)-1], []byte("."))
		out = append(out, b)
	}
	return out
}

// ImageName is the image compose uses for a service of project: the one it names, or
// compose's own "<project>-<service>" for one it builds.
func ImageName(project, service, image string) string {
	if image != "" {
		return image
	}
	return strings.ToLower(project + "-" + service)
}
