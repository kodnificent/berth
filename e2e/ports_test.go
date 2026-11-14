//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

type portEntry struct {
	Name          string `json:"name"`
	Service       string `json:"service"`
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port"`
	Policy        string `json:"policy"`
}

type portsStatus struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Host           string            `json:"host"`
	URL            string            `json:"url"`
	ComposeProject string            `json:"compose_project"`
	Ports          []portEntry       `json:"ports"`
	Env            map[string]string `json:"env"`
	Unresolved     map[string]string `json:"unresolved"`
}

func portsStatusOf(t *testing.T, repo *Repo, dir string) portsStatus {
	t.Helper()
	res := mustBerth(t, repo, dir, "status", "--json")
	var s portsStatus
	if err := json.Unmarshal([]byte(res.Stdout), &s); err != nil {
		t.Fatalf("berth status --json: %v\n%s", err, res.Stdout)
	}
	return s
}

func (s portsStatus) port(t *testing.T, service string, containerPort int) portEntry {
	t.Helper()
	for _, p := range s.Ports {
		if p.Service == service && p.ContainerPort == containerPort {
			return p
		}
	}
	t.Fatalf("status reports no port for %s:%d: %+v", service, containerPort, s.Ports)
	return portEntry{}
}

func (s portsStatus) named(t *testing.T, name string) portEntry {
	t.Helper()
	for _, p := range s.Ports {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("status reports no port named %s: %+v", name, s.Ports)
	return portEntry{}
}

// hostPorts lists every host port berth assigned to the berth.
func (s portsStatus) hostPorts() []int {
	var out []int
	for _, p := range s.Ports {
		if p.Policy == "assign" {
			out = append(out, p.HostPort)
		}
	}
	return out
}

// BerthWith runs berth with extra environment variables on top of the repo's own.
func (r *Repo) BerthWith(t *testing.T, env []string, dir string, args ...string) Result {
	t.Helper()
	saved := r.env
	r.env = append(append([]string{}, r.env...), env...)
	defer func() { r.env = saved }()
	return r.Berth(t, dir, args...)
}

// freePortBlock finds n consecutive ports nothing listens on, below the range Docker and
// the OS hand out as ephemeral ports.
func freePortBlock(t *testing.T, n int) []int {
	t.Helper()
	for attempt := 0; attempt < 200; attempt++ {
		start := 30000 + rand.IntN(2500)
		ok := true
		for p := start; p < start+n; p++ {
			if !portFree(p) {
				ok = false
				break
			}
		}
		if ok {
			out := make([]int, n)
			for i := range out {
				out[i] = start + i
			}
			return out
		}
	}
	t.Fatalf("no block of %d free ports found", n)
	return nil
}

func portFree(p int) bool {
	for _, addr := range []struct{ network, host string }{{"tcp4", "0.0.0.0"}, {"tcp4", "127.0.0.1"}, {"tcp6", "::"}, {"tcp6", "::1"}} {
		l, err := net.Listen(addr.network, net.JoinHostPort(addr.host, strconv.Itoa(p)))
		if err != nil {
			if addrInUse(err) {
				return false
			}
			continue
		}
		l.Close()
	}
	return true
}

func poolConfig(pool []int) string {
	return fmt.Sprintf("[pool]\nports = \"%d-%d\"\n", pool[0], pool[len(pool)-1])
}

func readDotEnv(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			out[k] = v
		}
	}
	return out
}

func writeDotEnv(t *testing.T, path string, env map[string]string) {
	t.Helper()
	var b strings.Builder
	for _, k := range sortedKeys(env) {
		fmt.Fprintf(&b, "%s=%s\n", k, env[k])
	}
	writeFile(t, path, b.String())
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("%q is not a port: %v", s, err)
	}
	return n
}

// onlyWeb assigns the fixture's web port and publishes nothing else, so each berth needs
// exactly one port from the pool.
func onlyWeb(t *testing.T, repo *Repo) string {
	t.Helper()
	search := readDotEnv(t, filepath.Join(repo.Dir, ".env"))["SEARCH_PORT"]
	return fmt.Sprintf("[compose.ports]\n\"web:80\" = \"assign\"\n\"db:3306\" = \"none\"\n\"search:%s\" = \"none\"\n", search)
}

func hold(t *testing.T, network, host string, port int) {
	t.Helper()
	l, err := net.Listen(network, net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("listening on %s %s:%d: %v", network, host, port, err)
	}
	t.Cleanup(func() { l.Close() })
}

// Two berths never receive the same assigned port, and no assigned port
// equals a port published by the main worktree's project.
func TestAssignedPortsNeverCollide(t *testing.T) {
	repo := NewRepo(t)
	other := NewRepo(t)
	other.ShareState(repo)
	pool := freePortBlock(t, 30)

	// Main's project is not running, so nothing listens on the ports it publishes; berth
	// must still keep them out of every berth.
	mainPublished := map[*Repo]map[int]string{}
	for i, r := range []*Repo{repo, other} {
		envPath := filepath.Join(r.Dir, ".env")
		env := readDotEnv(t, envPath)
		env["WEB_PORT"] = strconv.Itoa(pool[i])
		env["DB_PORT"] = strconv.Itoa(pool[10+i])
		writeDotEnv(t, envPath, env)
		mainPublished[r] = map[int]string{}
		for _, k := range []string{"WEB_PORT", "DB_PORT", "SEARCH_PORT"} {
			mainPublished[r][atoi(t, env[k])] = r.Dir + " " + k
		}
		writeFile(t, r.userConfig(), poolConfig(pool))
		writeFile(t, r.privateConfig(), "[compose.ports]\n\"web:80\" = \"assign\"\n\"db:3306\" = \"assign\"\n\n[ports]\nvite = { env = \"VITE_PORT\" }\n")
	}

	// A berth avoids its own main's ports, and those of every repository the machine
	// already has a berth in. A repository berth has never seen can't be known, so the
	// first berth of each repository is added before the second repository's berths.
	worktrees := []struct {
		repo *Repo
		path string
	}{
		{repo, repo.AddWorktree(t, "one")},
		{other, other.AddWorktree(t, "one")},
		{repo, repo.AddWorktree(t, "two")},
		{repo, repo.AddWorktree(t, "three")},
		{other, other.AddWorktree(t, "two")},
	}
	known := map[*Repo]bool{}

	owner := map[int]string{}
	for _, wt := range worktrees {
		known[wt.repo] = true
		mustBerth(t, wt.repo, wt.path, "add")
		s := portsStatusOf(t, wt.repo, wt.path)
		for _, want := range []struct {
			service string
			port    int
		}{{"web", 80}, {"db", 3306}} {
			if p := s.port(t, want.service, want.port); p.Policy != "assign" || p.HostPort == 0 {
				t.Errorf("%s: %s:%d = %+v, want an assigned host port", wt.path, want.service, want.port, p)
			}
		}
		if p := s.named(t, "vite"); p.Policy != "assign" || p.HostPort == 0 {
			t.Errorf("%s: vite = %+v, want an assigned host port", wt.path, p)
		}
		for _, hp := range s.hostPorts() {
			if prev, dup := owner[hp]; dup {
				t.Errorf("port %d assigned to both %s and %s", hp, prev, wt.path)
			}
			owner[hp] = wt.path
			for r := range known {
				if what, isMain := mainPublished[r][hp]; isMain {
					t.Errorf("%s was assigned port %d, which a main worktree publishes (%s)", wt.path, hp, what)
				}
			}
			if hp < pool[0] || hp > pool[len(pool)-1] {
				t.Errorf("%s was assigned port %d, outside the pool %d-%d", wt.path, hp, pool[0], pool[len(pool)-1])
			}
		}
	}
}

// A port that something is already listening on, on 0.0.0.0, 127.0.0.1 or
// ::, is never assigned.
func TestBusyPortsAreNeverAssigned(t *testing.T) {
	repo := NewRepo(t)
	pool := freePortBlock(t, 5)
	writeFile(t, repo.userConfig(), poolConfig(pool))
	writeFile(t, repo.privateConfig(), onlyWeb(t, repo))

	hold(t, "tcp4", "0.0.0.0", pool[0])
	hold(t, "tcp4", "127.0.0.1", pool[1])
	if l, err := net.Listen("tcp6", net.JoinHostPort("::", strconv.Itoa(pool[2]))); err == nil {
		t.Cleanup(func() { l.Close() })
	} else {
		t.Logf("no IPv6 on this host (%v); holding %d on 0.0.0.0 instead", err, pool[2])
		hold(t, "tcp4", "0.0.0.0", pool[2])
	}

	// A port Docker publishes for a container is held by Docker, not by the container.
	holder := repo.Project + "-holder"
	out, err := exec.Command("docker", "run", "-d", "--name", holder, "-p", fmt.Sprintf("%d:80", pool[3]), "busybox:1.36.1", "sleep", "600").CombinedOutput()
	if err != nil {
		t.Fatalf("starting a container that publishes %d: %v\n%s", pool[3], err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", holder).Run() })
	waitBusy(t, pool[3])

	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add")
	if got := portsStatusOf(t, repo, wt).port(t, "web", 80).HostPort; got != pool[4] {
		t.Errorf("web was assigned %d, want %d, the only pool port nothing listens on (pool %v)", got, pool[4], pool)
	}

	next := repo.AddWorktree(t, "feat-y")
	if res := repo.Berth(t, next, "add"); res.Exit != 2 {
		t.Errorf("add with every pool port busy or assigned: exit %d, want 2\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
}

func waitBusy(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for portFree(port) {
		if time.Now().After(deadline) {
			t.Fatalf("port %d never became busy", port)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// A berth keeps its assigned ports across repeated add runs and across a
// Docker restart. After a restart the berth's own containers come back listening on its
// ports, and Docker may not be reachable at all while it starts; neither may move them.
func TestPortsAreKeptAcrossAddAndDockerRestart(t *testing.T) {
	repo := NewRepo(t)
	writeFile(t, repo.privateConfig(), "[compose.ports]\n\"web:80\" = \"assign\"\n\"db:3306\" = \"assign\"\n\n[ports]\nvite = { env = \"VITE_PORT\" }\n")
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add")
	first := portsStatusOf(t, repo, wt)
	if len(first.hostPorts()) < 3 {
		t.Fatalf("want at least three assigned ports, got %+v", first.Ports)
	}

	same := func(when string) {
		t.Helper()
		got := portsStatusOf(t, repo, wt)
		if fmt.Sprint(got.Ports) != fmt.Sprint(first.Ports) {
			t.Errorf("%s: ports = %+v, want %+v", when, got.Ports, first.Ports)
		}
	}

	mustBerth(t, repo, wt, "add")
	mustBerth(t, repo, wt, "add")
	same("after repeated add")

	for _, p := range first.hostPorts() {
		hold(t, "tcp4", "0.0.0.0", p)
	}
	mustBerth(t, repo, wt, "add")
	same("after add while the berth's own ports are in use")

	res := repo.BerthWith(t, []string{"DOCKER_HOST=unix:///nonexistent/docker.sock"}, wt, "add")
	if res.Exit != 0 {
		t.Fatalf("add with Docker unreachable: exit %d\nstderr:\n%s", res.Exit, res.Stderr)
	}
	same("after add with Docker unreachable")
}

// With a real Docker restart. Restarting Docker Desktop stops every
// container on the machine, so this runs only when BERTH_E2E_RESTART_DOCKER=1.
func TestPortsSurviveRealDockerRestart(t *testing.T) {
	if os.Getenv("BERTH_E2E_RESTART_DOCKER") != "1" {
		t.Skip("restarts Docker Desktop; set BERTH_E2E_RESTART_DOCKER=1 to run")
	}
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add")
	before := portsStatusOf(t, repo, wt)
	web := before.port(t, "web", 80).HostPort

	own := repo.Project + "-own"
	if out, err := exec.Command("docker", "run", "-d", "--restart", "always", "--name", own, "-p", fmt.Sprintf("%d:80", web), "busybox:1.36.1", "sleep", "3600").CombinedOutput(); err != nil {
		t.Fatalf("starting a container on the berth's port: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", own).Run() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	_ = exec.CommandContext(ctx, "osascript", "-e", `quit app "Docker"`).Run()
	for exec.CommandContext(ctx, "docker", "info").Run() == nil && ctx.Err() == nil {
		time.Sleep(time.Second)
	}
	if out, err := exec.CommandContext(ctx, "open", "-a", "Docker").CombinedOutput(); err != nil {
		t.Fatalf("starting Docker: %v\n%s", err, out)
	}
	for exec.CommandContext(ctx, "docker", "info").Run() != nil {
		if ctx.Err() != nil {
			t.Fatal("Docker did not come back within five minutes")
		}
		time.Sleep(2 * time.Second)
	}
	waitBusy(t, web)

	mustBerth(t, repo, wt, "add")
	if after := portsStatusOf(t, repo, wt); fmt.Sprint(after.Ports) != fmt.Sprint(before.Ports) {
		t.Errorf("after a Docker restart, ports = %+v, want %+v", after.Ports, before.Ports)
	}
}

// A port stays reserved while its berth exists, including while the
// worktree is locked and missing.
func TestPortStaysReservedWhileLockedAndMissing(t *testing.T) {
	repo := NewRepo(t)
	pool := freePortBlock(t, 2)
	writeFile(t, repo.userConfig(), poolConfig(pool))
	writeFile(t, repo.privateConfig(), onlyWeb(t, repo))

	locked := repo.AddWorktree(t, "locked")
	mustBerth(t, repo, locked, "add")
	before := portsStatusOf(t, repo, locked)
	held := before.port(t, "web", 80).HostPort

	repo.Git(t, "worktree", "lock", locked)
	away := locked + "-away"
	if err := os.Rename(locked, away); err != nil {
		t.Fatal(err)
	}

	second := repo.AddWorktree(t, "second")
	mustBerth(t, repo, second, "add")
	if got := portsStatusOf(t, repo, second).port(t, "web", 80).HostPort; got == held {
		t.Errorf("a new berth was given port %d, still reserved by the locked, missing worktree's berth", got)
	}
	third := repo.AddWorktree(t, "third")
	if res := repo.Berth(t, third, "add"); res.Exit != 2 {
		t.Errorf("add with the pool held by a live and a locked berth: exit %d, want 2\nstderr:\n%s", res.Exit, res.Stderr)
	}
	if b := findBerth(t, list(t, repo), before.ID); *b.Orphaned {
		t.Errorf("locked, missing worktree's berth is listed as orphaned")
	}

	if err := os.Rename(away, locked); err != nil {
		t.Fatal(err)
	}
	if after := portsStatusOf(t, repo, locked); fmt.Sprint(after.Ports) != fmt.Sprint(before.Ports) {
		t.Errorf("after the worktree came back, ports = %+v, want %+v", after.Ports, before.Ports)
	}
}

// When the pool has no free port, add exits 2 and names the pool range.
func TestFullPoolExitsTwo(t *testing.T) {
	repo := NewRepo(t)
	pool := freePortBlock(t, 2)
	rangeText := fmt.Sprintf("%d-%d", pool[0], pool[1])
	writeFile(t, repo.userConfig(), poolConfig(pool))
	writeFile(t, repo.privateConfig(), onlyWeb(t, repo))

	first := repo.AddWorktree(t, "first")
	mustBerth(t, repo, first, "add")
	taken := portsStatusOf(t, repo, first).port(t, "web", 80).HostPort
	for _, p := range pool {
		if p != taken {
			hold(t, "tcp4", "0.0.0.0", p)
		}
	}

	wt := repo.AddWorktree(t, "feat-x")
	res := repo.Berth(t, wt, "add")
	if res.Exit != 2 {
		t.Errorf("add with a full pool: exit %d, want 2\nstderr:\n%s", res.Exit, res.Stderr)
	}
	if !strings.Contains(res.Stderr, rangeText) {
		t.Errorf("add with a full pool: message %q does not name the pool %s", res.Stderr, rangeText)
	}

	res = repo.Berth(t, wt, "add", "--json")
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &body); err != nil {
		t.Fatalf("add --json: %v\n%s", err, res.Stdout)
	}
	if res.Exit != 2 || body.Error.Code != "no_free_ports" || !strings.Contains(body.Error.Message, rangeText) {
		t.Errorf("add --json with a full pool: exit %d, error %+v; want 2, no_free_ports, naming %s", res.Exit, body.Error, rangeText)
	}
	if got := berthsAt(list(t, repo), wt); len(got) != 0 {
		t.Errorf("a failed add left a berth behind: %+v", got)
	}
}

// For branch names with slashes, uppercase, non-ASCII characters and 200
// characters, the compose project name and hostname are valid for Docker Compose and for
// a DNS label, and two different branches never produce the same name. Same-named
// branches in two repositories are different branches too.
func TestGeneratedNamesAreValidAndUnique(t *testing.T) {
	repo := NewRepo(t)
	other := NewRepo(t)
	other.ShareState(repo)

	long := strings.Repeat("a", 200)
	branches := []string{
		"feature/Login-Page",
		// Differs from the branch above only in case and separators. A case-only twin such
		// as FEATURE/login-page can't coexist with it on a case-insensitive filesystem.
		"Feature-LOGIN-page",
		"feat/x",
		"feat-x",
		"feat_x",
		"Fix/ünïcödé-ñame",
		"日本語ブランチ",
		"émoji-🚀-branch",
		"UPPER/CASE/ONLY",
		long,
		long[:199] + "b",
		strings.Repeat("deep/", 39) + "end",
		"localhost",
		"123",
	}

	compose := regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	label := regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	seen := map[string]map[string]string{"name": {}, "host": {}, "compose_project": {}}
	unique := func(field, value, where string) {
		if prev, dup := seen[field][value]; dup {
			t.Errorf("%s %q given to both %s and %s", field, value, prev, where)
		}
		seen[field][value] = where
	}
	mains := map[string]bool{repo.Project: true, other.Project: true}

	check := func(r *Repo, branch, path string) {
		t.Helper()
		mustBerth(t, r, path, "add")
		s := portsStatusOf(t, r, path)
		where := fmt.Sprintf("branch %q", branch)
		if r == other {
			where += " in the second repository"
		}

		if !compose.MatchString(s.ComposeProject) || !label.MatchString(s.ComposeProject) {
			t.Errorf("%s: compose project %q is not a valid compose project name and DNS label", where, s.ComposeProject)
		}
		if mains[s.ComposeProject] {
			t.Errorf("%s: compose project %q is a main worktree's project", where, s.ComposeProject)
		}
		if len(s.Host) > 253 {
			t.Errorf("%s: host %q is longer than 253 characters", where, s.Host)
		}
		for _, l := range strings.Split(s.Host, ".") {
			if !label.MatchString(l) {
				t.Errorf("%s: host %q has label %q, which is not a valid DNS label", where, s.Host, l)
			}
		}
		if s.Host == "localhost" {
			t.Errorf("%s: host is localhost, which is main's", where)
		}
		if s.Name == "" {
			t.Errorf("%s: empty name", where)
		}
		unique("name", s.Name, where)
		unique("host", s.Host, where)
		unique("compose_project", s.ComposeProject, where)

		// Compose itself must accept the project name.
		res, err := r.run(r.Dir, "docker", "compose", "--project-name", s.ComposeProject, "config", "--quiet")
		if err != nil || res.Exit != 0 {
			t.Errorf("%s: docker compose rejects project name %q: %v\n%s", where, s.ComposeProject, err, res.Stderr)
		}
	}

	for i, b := range branches {
		path := filepath.Join(filepath.Dir(repo.Dir), "worktrees", fmt.Sprintf("b%d", i), worktreeDirName(filepath.Base(b)))
		repo.Git(t, "worktree", "add", "-q", "-b", b, path)
		check(repo, b, path)
	}
	check(other, "feat-x", other.AddWorktree(t, "feat-x"))
}

// berth status --json reports each port with its service, container port,
// host port and policy. Templates naming an assigned port now resolve.
func TestStatusReportsPorts(t *testing.T) {
	repo := NewRepo(t)
	search := atoi(t, readDotEnv(t, filepath.Join(repo.Dir, ".env"))["SEARCH_PORT"])
	writeFile(t, repo.privateConfig(), fmt.Sprintf(`[berth]
url = "http://{host}:{port.web}"

[compose.ports]
"web:80" = "assign"
"db:3306" = "none"
"search:%d" = "ephemeral"

[ports]
vite = { env = "VITE_PORT" }

[env.set]
APP_URL = "{url}"
VITE_PORT_VALUE = "{port.vite}"
WEB_PORT_VALUE = "{port.web:80}"
`, search))
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add")
	s := portsStatusOf(t, repo, wt)

	web := s.port(t, "web", 80)
	if web.Policy != "assign" || web.HostPort == 0 {
		t.Errorf("web:80 = %+v, want policy assign with a host port", web)
	}
	if db := s.port(t, "db", 3306); db.Policy != "none" || db.HostPort != 0 {
		t.Errorf("db:3306 = %+v, want policy none with no host port", db)
	}
	if sp := s.port(t, "search", search); sp.Policy != "ephemeral" || sp.HostPort != 0 {
		t.Errorf("search:%d = %+v, want policy ephemeral with no host port until Docker picks one", search, sp)
	}
	vite := s.named(t, "vite")
	if vite.Policy != "assign" || vite.HostPort == 0 || vite.Service != "" {
		t.Errorf("vite = %+v, want an assigned host port outside compose", vite)
	}

	if len(s.Unresolved) != 0 {
		t.Errorf("unresolved = %v, want every template resolved once ports are assigned", s.Unresolved)
	}
	if want := fmt.Sprintf("http://%s:%d", s.Host, web.HostPort); s.URL != want || s.Env["APP_URL"] != want {
		t.Errorf("url = %q, APP_URL = %q, want %q", s.URL, s.Env["APP_URL"], want)
	}
	if got := s.Env["VITE_PORT_VALUE"]; got != strconv.Itoa(vite.HostPort) {
		t.Errorf("VITE_PORT_VALUE = %q, want %d", got, vite.HostPort)
	}
	if got := s.Env["WEB_PORT_VALUE"]; got != strconv.Itoa(web.HostPort) {
		t.Errorf("WEB_PORT_VALUE = %q, want %d", got, web.HostPort)
	}
}
