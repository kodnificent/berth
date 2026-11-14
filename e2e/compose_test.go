//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// regexpPublished matches a published host port in docker ps's Ports column, such as
// "0.0.0.0:20001->80/tcp".
var regexpPublished = regexp.MustCompile(`:(\d+)->`)

// composeConfig is docker compose config in dir, as compose itself resolves it.
func composeConfig(t *testing.T, repo *Repo, dir string) map[string]any {
	t.Helper()
	var cfg map[string]any
	out := repo.Compose(t, dir, "config", "--format", "json")
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("docker compose config --format json in %s: %v\n%s", dir, err, out)
	}
	return cfg
}

func services(t *testing.T, cfg map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	raw, _ := cfg["services"].(map[string]any)
	for name, s := range raw {
		out[name], _ = s.(map[string]any)
	}
	return out
}

func serviceEnv(t *testing.T, cfg map[string]any, service string) map[string]any {
	t.Helper()
	s, ok := services(t, cfg)[service]
	if !ok {
		t.Fatalf("docker compose config has no service %s", service)
	}
	env, _ := s["environment"].(map[string]any)
	return env
}

// publishedPort is the host port docker compose reports for a running service's port.
func publishedPort(t *testing.T, repo *Repo, dir, service string, target int) int {
	t.Helper()
	out := strings.TrimSpace(repo.Compose(t, dir, "port", service, strconv.Itoa(target)))
	port := out[strings.LastIndex(out, ":")+1:]
	if port == "" || port == "0" {
		t.Fatalf("docker compose port %s %d printed %q", service, target, out)
	}
	return atoi(t, port)
}

// running lists the services of a compose project that have a running container.
func running(t *testing.T, project string) []string {
	t.Helper()
	out, err := exec.Command("docker", "ps", "--filter", "label=com.docker.compose.project="+project, "--format", `{{.Label "com.docker.compose.service"}}`).Output()
	if err != nil {
		t.Fatalf("docker ps: %v", err)
	}
	svcs := strings.Fields(string(out))
	sort.Strings(svcs)
	return svcs
}

func searchPort(t *testing.T, repo *Repo) int {
	t.Helper()
	return atoi(t, readDotEnv(t, filepath.Join(repo.Dir, ".env"))["SEARCH_PORT"])
}

// In a berth, docker compose config reports the berth's project name, even
// though the fixture's compose file pins name:.
func TestComposeUsesBerthProject(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-project")
	mustBerth(t, repo, wt, "add")
	s := portsStatusOf(t, repo, wt)

	if s.ComposeProject == "" || s.ComposeProject == repo.Project {
		t.Fatalf("status compose_project = %q, want a project of the berth's own, not main's %q", s.ComposeProject, repo.Project)
	}
	if got := composeConfig(t, repo, wt)["name"]; got != s.ComposeProject {
		t.Errorf("docker compose config in the berth reports project %v, want the berth's %q", got, s.ComposeProject)
	}
	if got := composeConfig(t, repo, repo.Dir)["name"]; got != repo.Project {
		t.Errorf("docker compose config in main reports project %v, want main's pinned %q", got, repo.Project)
	}
}

// docker compose config in a berth includes the repository's
// compose.override.yml when one exists, then the files under [compose] files, in that
// order. The override counts whether git tracks it or only main has it, ignored.
func TestComposeFilesApplyInOrder(t *testing.T) {
	for _, tracked := range []bool{true, false} {
		t.Run(fmt.Sprintf("tracked=%v", tracked), func(t *testing.T) {
			repo := NewRepo(t)
			writeFile(t, filepath.Join(repo.Dir, "compose.override.yml"), "services:\n  web:\n    environment:\n      ORDER: override\n      FROM_OVERRIDE: \"yes\"\n")
			writeFile(t, filepath.Join(repo.Dir, ".worktree", "compose.yml"), "services:\n  web:\n    environment:\n      ORDER: berth-file\n")
			if tracked {
				repo.Git(t, "add", "compose.override.yml")
			} else {
				appendFile(t, filepath.Join(repo.Dir, ".gitignore"), "compose.override.yml\n")
				repo.Git(t, "add", ".gitignore")
			}
			repo.Git(t, "add", ".worktree/compose.yml")
			repo.Git(t, "commit", "-q", "-m", "override and berth-only file")
			writeFile(t, repo.privateConfig(), "[compose]\nfiles = [\".worktree/compose.yml\"]\n")

			wt := repo.AddWorktree(t, "feat-files")
			mustBerth(t, repo, wt, "add")

			env := serviceEnv(t, composeConfig(t, repo, wt), "web")
			if env["FROM_OVERRIDE"] != "yes" {
				t.Errorf("compose.override.yml does not apply in the berth: web environment = %v", env)
			}
			if env["ORDER"] != "berth-file" {
				t.Errorf("web ORDER = %v in the berth, want \"berth-file\": [compose] files must apply after compose.override.yml", env["ORDER"])
			}
			if got := serviceEnv(t, composeConfig(t, repo, repo.Dir), "web")["ORDER"]; got != "override" {
				t.Errorf("web ORDER = %v in main, want \"override\": [compose] files apply in berths only", got)
			}
		})
	}
}

// Compared with main, docker compose config in a berth differs only in
// project name, published host ports, container names, shared volumes and excluded
// services. Paths differ only because relative paths resolve to the worktree.
func TestComposeConfigDiffersOnlyOnHostSide(t *testing.T) {
	repo := NewRepo(t)
	compose := filepath.Join(repo.Dir, "compose.yml")
	data := readString(t, compose)
	data = strings.Replace(data, "\n  db:\n", "\n  db:\n    container_name: "+repo.Project+"-fixed-db\n", 1)
	writeFile(t, compose, data)
	repo.Git(t, "commit", "-q", "-am", "pin a container name")
	writeFile(t, repo.privateConfig(), "[compose]\nexclude = [\"search\"]\n\n[compose.volumes]\ncache = \"share\"\n")

	wt := repo.AddWorktree(t, "feat-diff")
	mustBerth(t, repo, wt, "add")
	s := portsStatusOf(t, repo, wt)

	berthOut := repo.Compose(t, wt, "config", "--format", "json")
	mainOut := repo.Compose(t, repo.Dir, "config", "--format", "json")
	// Names compose derives from the project, such as the default network's, follow the
	// project name; relative paths resolve to the worktree.
	berthOut = strings.ReplaceAll(berthOut, jsonText(wt), jsonText(repo.Dir))
	berthOut = strings.ReplaceAll(berthOut, s.ComposeProject, repo.Project)

	var berth, main map[string]any
	if err := json.Unmarshal([]byte(berthOut), &berth); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(mainOut), &main); err != nil {
		t.Fatal(err)
	}

	bs, ms := services(t, berth), services(t, main)
	if _, ok := bs["search"]; ok {
		t.Errorf("excluded service search is still in the berth's config")
	}
	if bs["db"]["container_name"] != nil {
		t.Errorf("db keeps container_name %v in the berth, which would collide with main", bs["db"]["container_name"])
	}
	bv, _ := berth["volumes"].(map[string]any)
	mv, _ := main["volumes"].(map[string]any)
	bc, _ := bv["cache"].(map[string]any)
	mc, _ := mv["cache"].(map[string]any)
	if bc["name"] == nil || bc["name"] != mc["name"] {
		t.Errorf("share volume cache is named %v in the berth, want main's %v", bc["name"], mc["name"])
	}

	delete(main["services"].(map[string]any), "search")
	for _, svcs := range []map[string]map[string]any{bs, ms} {
		for _, svc := range svcs {
			delete(svc, "container_name")
			if deps, ok := svc["depends_on"].(map[string]any); ok {
				delete(deps, "search")
			}
			ports, _ := svc["ports"].([]any)
			for _, p := range ports {
				if m, ok := p.(map[string]any); ok {
					delete(m, "published")
				}
			}
		}
	}
	delete(bv, "cache")
	delete(mv, "cache")
	delete(berth, "name")
	delete(main, "name")

	if !reflect.DeepEqual(berth, main) {
		b, _ := json.MarshalIndent(berth, "", "  ")
		m, _ := json.MarshalIndent(main, "", "  ")
		t.Errorf("the berth's compose config differs from main's beyond the host side\nberth:\n%s\nmain:\n%s", b, m)
	}
}

// A healthcheck that reads a published-port variable inside the container
// passes in a berth whose published port was reassigned.
func TestHealthcheckPassesWithReassignedPort(t *testing.T) {
	repo := NewRepo(t)
	search := searchPort(t, repo)
	wt := repo.AddWorktree(t, "feat-health")
	mustBerth(t, repo, wt, "add")
	s := portsStatusOf(t, repo, wt)
	assigned := s.port(t, "search", search)
	if assigned.Policy != "assign" || assigned.HostPort == 0 || assigned.HostPort == search {
		t.Fatalf("search:%d = %+v, want a reassigned host port", search, assigned)
	}

	repo.Compose(t, wt, "up", "-d", "--wait", "search")
	if got := strings.TrimSpace(repo.Compose(t, wt, "exec", "-T", "search", "printenv", "SEARCH_PORT")); got != strconv.Itoa(search) {
		t.Errorf("SEARCH_PORT inside the berth's container = %q, want main's %d", got, search)
	}
	if got := publishedPort(t, repo, wt, "search", search); got != assigned.HostPort {
		t.Errorf("search is published on %d, want the assigned %d", got, assigned.HostPort)
	}
	health := strings.TrimSpace(repo.Compose(t, wt, "ps", "--format", "{{.Health}}", "search"))
	if health != "healthy" {
		t.Errorf("search health in the berth = %q, want healthy", health)
	}
}

// Assign ports are published on the berth's assigned port, ephemeral ports
// on a port Docker picks and reported by berth status, and none ports are not published.
func TestPortPoliciesArePublished(t *testing.T) {
	repo := NewRepo(t)
	search := searchPort(t, repo)
	writeFile(t, repo.privateConfig(), fmt.Sprintf("[compose.ports]\n\"web:80\" = \"assign\"\n\"search:%d\" = \"ephemeral\"\n\"db:3306\" = \"none\"\n", search))
	wt := repo.AddWorktree(t, "feat-policies")
	mustBerth(t, repo, wt, "add")
	web := portsStatusOf(t, repo, wt).port(t, "web", 80)

	repo.Compose(t, wt, "up", "-d", "--wait")

	if got := publishedPort(t, repo, wt, "web", 80); got != web.HostPort {
		t.Errorf("web:80 is published on %d, want the assigned %d", got, web.HostPort)
	}
	picked := publishedPort(t, repo, wt, "search", search)
	if picked == search {
		t.Errorf("ephemeral search:%d is published on main's port %d", search, search)
	}
	if got := portsStatusOf(t, repo, wt).port(t, "search", search); got.Policy != "ephemeral" || got.HostPort != picked {
		t.Errorf("status reports search:%d as %+v, want ephemeral on the port Docker picked, %d", search, got, picked)
	}
	if ports := repo.Compose(t, wt, "ps", "--format", "{{.Ports}}", "db"); regexpPublished.MatchString(ports) {
		t.Errorf("db:3306 has policy none but db publishes %s", ports)
	}
}

// Excluding a service that another service depends on leaves docker
// compose config and docker compose up succeeding, with the excluded service not
// running.
func TestExcludedDependencyStillStarts(t *testing.T) {
	repo := NewRepo(t)
	writeFile(t, repo.privateConfig(), "[compose]\nexclude = [\"search\"]\n")
	wt := repo.AddWorktree(t, "feat-exclude")
	mustBerth(t, repo, wt, "add")
	project := portsStatusOf(t, repo, wt).ComposeProject

	repo.Compose(t, wt, "config", "--quiet")
	repo.Compose(t, wt, "up", "-d", "--wait")
	if got, want := running(t, project), []string{"db", "web"}; !reflect.DeepEqual(got, want) {
		t.Errorf("running services in the berth = %v, want %v", got, want)
	}
}

// A share volume in a berth is the source's volume: a file written to it in
// the berth is visible in main. Taking the berth down with its volumes keeps it.
func TestShareVolumeIsSourceVolume(t *testing.T) {
	repo := NewRepo(t)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"share\"\n")
	wt := repo.AddWorktree(t, "feat-share")
	mustBerth(t, repo, wt, "add")

	// Built first, since run prints a build's log to stdout ahead of the command's output.
	repo.Compose(t, wt, "build", "web")
	repo.Compose(t, repo.Dir, "build", "web")
	repo.Compose(t, wt, "run", "--rm", "--no-deps", "web", "sh", "-c", "echo from-berth > /cache/shared.txt")
	if got := strings.TrimSpace(repo.Compose(t, repo.Dir, "run", "--rm", "--no-deps", "web", "cat", "/cache/shared.txt")); got != "from-berth" {
		t.Errorf("main reads %q from the shared volume, want what the berth wrote", got)
	}

	repo.Compose(t, wt, "down", "--volumes")
	if got := strings.TrimSpace(repo.Compose(t, repo.Dir, "run", "--rm", "--no-deps", "web", "cat", "/cache/shared.txt")); got != "from-berth" {
		t.Errorf("after down --volumes in the berth, main reads %q from its volume, want it kept", got)
	}
}

// Main and three berths of the fixture project run docker compose up -d at
// the same time with no port, container, network or volume conflict.
func TestMainAndThreeBerthsRunTogether(t *testing.T) {
	repo := NewRepo(t)
	dirs := []string{repo.Dir}
	projects := []string{repo.Project}
	for i := 1; i <= 3; i++ {
		wt := repo.AddWorktree(t, fmt.Sprintf("feat-%d", i))
		mustBerth(t, repo, wt, "add")
		dirs = append(dirs, wt)
		projects = append(projects, portsStatusOf(t, repo, wt).ComposeProject)
	}

	var wg sync.WaitGroup
	results := make([]Result, len(dirs))
	errs := make([]error, len(dirs))
	for i, dir := range dirs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = repo.run(dir, "docker", "compose", "up", "-d", "--wait")
		}()
	}
	wg.Wait()
	for i := range dirs {
		if errs[i] != nil || results[i].Exit != 0 {
			t.Errorf("docker compose up -d in %s: exit %d %v\n%s", dirs[i], results[i].Exit, errs[i], results[i].Stderr)
		}
	}
	if t.Failed() {
		return
	}

	hostPorts := map[string]string{}
	names := map[string]bool{}
	for _, project := range projects {
		if got, want := running(t, project), []string{"db", "search", "web"}; !reflect.DeepEqual(got, want) {
			t.Errorf("project %s runs %v, want %v", project, got, want)
		}
		out, err := exec.Command("docker", "ps", "--filter", "label=com.docker.compose.project="+project, "--format", "{{.Names}}\t{{.Ports}}").Output()
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			name, ports, _ := strings.Cut(line, "\t")
			if names[name] {
				t.Errorf("container name %s is used twice", name)
			}
			names[name] = true
			seen := map[string]bool{}
			for _, m := range regexpPublished.FindAllStringSubmatch(ports, -1) {
				if seen[m[1]] {
					continue
				}
				seen[m[1]] = true
				if other, taken := hostPorts[m[1]]; taken {
					t.Errorf("host port %s is published by both %s and %s", m[1], other, name)
				}
				hostPorts[m[1]] = name
			}
		}
		for _, kind := range []string{"network", "volume"} {
			out, err := exec.Command("docker", kind, "ls", "--filter", "label=com.docker.compose.project="+project, "--format", "{{.Name}}").Output()
			if err != nil {
				t.Fatal(err)
			}
			if len(strings.Fields(string(out))) == 0 {
				t.Errorf("project %s has no %s of its own", project, kind)
			}
		}
	}
}
