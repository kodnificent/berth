package cli

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/ports"
	"github.com/kodnificent/berth/internal/proxy"
	"github.com/kodnificent/berth/internal/registry"
)

func proxyConfig(t *testing.T, extra string) *config.Config {
	t.Helper()
	c, err := config.Parse("berth.toml", []byte("[berth]\nhost = \"{name}.myapp.localhost\"\n[compose.ports]\n\"web:80\" = \"assign\"\n[ports]\nvite = { env = \"VITE_PORT\" }\n"+extra))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

var proxiedRecord = registry.Record{Name: "feat-x", Host: "feat-x.myapp.localhost", Ports: []ports.Port{
	{Name: "web:80", Service: "web", ContainerPort: 80, HostPort: 20001, Policy: ports.Assign},
	{Name: "vite", HostPort: 20002, Policy: ports.Assign},
}}

func TestProxySendsTheBerthsNamesToItsAssignedPorts(t *testing.T) {
	b := proxyBerth(proxiedRecord, proxyConfig(t, "[proxy]\nport = \"web\"\n[proxy.routes]\n\"vite.{host}\" = \"vite\"\n"), nil)

	if b.Port.Port != 20001 || b.Routes["vite.feat-x.myapp.localhost"].Port != 20002 {
		t.Errorf("host to %+v and vite to %+v, want 20001 and 20002", b.Port, b.Routes["vite.feat-x.myapp.localhost"])
	}
}

func TestProxyNeverSendsABerthToAPortMainUses(t *testing.T) {
	b := proxyBerth(proxiedRecord, proxyConfig(t, "[proxy]\nport = \"web\"\n"), map[int]bool{20001: true})

	if b.Port.Port != 0 || !strings.Contains(b.Port.Refused, "main") {
		t.Errorf("target %+v, want none, saying the port is main's", b.Port)
	}
}

// A config can name a port the berth was given no value for, such as one added after
// berth add; the proxy refuses rather than guess one.
func TestProxyRefusesAPortTheBerthWasNotAssigned(t *testing.T) {
	rec := proxiedRecord
	rec.Ports = rec.Ports[:1]

	b := proxyBerth(rec, proxyConfig(t, "[proxy]\nport = \"vite\"\n"), nil)

	if b.Port.Port != 0 || !strings.Contains(b.Port.Refused, "berth add") {
		t.Errorf("target %+v, want none, saying to run berth add", b.Port)
	}
}

func TestProxyRefusesABerthWhoseConfigSetsNoPort(t *testing.T) {
	b := proxyBerth(proxiedRecord, proxyConfig(t, ""), nil)

	if b.Port.Port != 0 || !strings.Contains(b.Port.Refused, "[proxy] port") {
		t.Errorf("target %+v, want none, naming [proxy] port", b.Port)
	}
}

// fakeMain is a main worktree at root/name with the given .env and compose file.
func fakeMain(t *testing.T, root, name, env, composeFile string) string {
	t.Helper()
	repo := filepath.Join(root, name)
	mustWrite(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")
	if env != "" {
		mustWrite(t, filepath.Join(repo, ".env"), env)
	}
	if composeFile != "" {
		mustWrite(t, filepath.Join(repo, "compose.yaml"), composeFile)
	}
	return repo
}

// fakeBerth records a berth of repo in the registry at state, with a linked worktree
// whose berth.toml is cfg, as add and git leave them.
func fakeBerth(t *testing.T, state, repo, id, name, host, cfg string, assigned ...ports.Port) {
	t.Helper()
	wt := filepath.Join(filepath.Dir(repo), filepath.Base(repo)+"-"+name)
	admin := filepath.Join(repo, ".git", "worktrees", name)
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+admin+"\n")
	mustWrite(t, filepath.Join(wt, "berth.toml"), cfg)
	mustWrite(t, filepath.Join(admin, "gitdir"), filepath.Join(wt, ".git")+"\n")
	mustWrite(t, filepath.Join(admin, "berth-id"), id+"\n")
	rec := registry.Record{ID: id, Repository: repo, CommonDir: filepath.Join(repo, ".git"), AdminDir: admin, Worktree: wt, Name: name, Host: host, Ports: assigned}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(state, "berths", id+".json"), string(data))
}

func mustWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fakeRegistry(t *testing.T) (root, state string) {
	t.Helper()
	root = t.TempDir()
	state = filepath.Join(root, "state")
	t.Setenv("BERTH_STATE_DIR", state)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	return root, state
}

func targetOf(table proxy.Table, host string) proxy.Target {
	_, target, _ := table.Lookup(host)
	return target
}

const hostConfig = "[berth]\nhost = \"{name}.myapp.localhost\"\n"

// The ports main's .env sets depend on the keys a berth's config names, and two berths
// of one repository can name different keys.
func TestProxyChecksEachBerthAgainstMainsPortsAsItsOwnConfigNamesThem(t *testing.T) {
	root, state := fakeRegistry(t)
	repo := fakeMain(t, root, "myapp", "VITE_PORT=20007\n", "")
	fakeBerth(t, state, repo, "a1", "shop", "shop.myapp.localhost", hostConfig+"[compose.ports]\n\"web:80\" = \"assign\"\n[proxy]\nport = \"web\"\n",
		ports.Port{Name: "web:80", Service: "web", ContainerPort: 80, HostPort: 20008, Policy: ports.Assign})
	fakeBerth(t, state, repo, "b2", "feat-x", "feat-x.myapp.localhost", hostConfig+"[ports]\nvite = { env = \"VITE_PORT\" }\n[proxy]\nport = \"vite\"\n",
		ports.Port{Name: "vite", HostPort: 20007, Policy: ports.Assign})

	table := newProxyRoutes(io.Discard, 80).refresh()

	if got := targetOf(table, "feat-x.myapp.localhost"); got.Port != 0 || !strings.Contains(got.Refused, "main") {
		t.Errorf("feat-x, on the port main's .env sets in VITE_PORT, goes to %+v; want it refused as main's", got)
	}
	if got := targetOf(table, "shop.myapp.localhost"); got.Port != 20008 {
		t.Errorf("shop goes to %+v, want its port 20008", got)
	}
}

func TestProxyNeverSendsABerthToAPortAnotherRepositorysMainPublishes(t *testing.T) {
	root, state := fakeRegistry(t)
	other := fakeMain(t, root, "other", "", "services:\n  web:\n    image: scratch\n    ports:\n      - \"20009:80\"\n")
	repo := fakeMain(t, root, "myapp", "", "")
	fakeBerth(t, state, other, "a1", "feat-y", "feat-y.other.localhost", "[berth]\nhost = \"{name}.other.localhost\"\n")
	fakeBerth(t, state, repo, "b2", "feat-x", "feat-x.myapp.localhost", hostConfig+"[compose.ports]\n\"web:80\" = \"assign\"\n[proxy]\nport = \"web\"\n",
		ports.Port{Name: "web:80", Service: "web", ContainerPort: 80, HostPort: 20009, Policy: ports.Assign})

	table := newProxyRoutes(io.Discard, 80).refresh()

	if got := targetOf(table, "feat-x.myapp.localhost"); got.Port != 0 || !strings.Contains(got.Refused, "main") {
		t.Errorf("feat-x, on a port other's main publishes, goes to %+v; want it refused as main's", got)
	}
}

func TestProxyAnswersForAnOrphanedBerthNestedInAnotherBerthsHost(t *testing.T) {
	root, state := fakeRegistry(t)
	repo := fakeMain(t, root, "myapp", "", "")
	fakeBerth(t, state, repo, "a1", "feat-x", "feat-x.myapp.localhost", hostConfig+"[compose.ports]\n\"web:80\" = \"assign\"\n[proxy]\nport = \"web\"\n",
		ports.Port{Name: "web:80", Service: "web", ContainerPort: 80, HostPort: 20001, Policy: ports.Assign})
	fakeBerth(t, state, repo, "b2", "app", "app.feat-x.myapp.localhost", hostConfig)
	if err := os.Remove(filepath.Join(repo, ".git", "worktrees", "app", "berth-id")); err != nil {
		t.Fatal(err)
	}
	s := proxy.NewServer(80)
	s.SetTable(newProxyRoutes(io.Discard, 80).refresh())
	req := httptest.NewRequest(http.MethodGet, "http://api.app.feat-x.myapp.localhost/", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()

	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("a name under the orphaned berth's host: status %d, want the 404 rather than feat-x's app", rec.Code)
	}
}

func TestProxyRefusesABerthWhoseConfigFailsToLoadForItsOwnNames(t *testing.T) {
	root, state := fakeRegistry(t)
	repo := fakeMain(t, root, "myapp", "", "")
	fakeBerth(t, state, repo, "a1", "feat-x", "feat-x.myapp.localhost", hostConfig+"[compose.ports]\n\"web:80\" = \"assign\"\n[proxy]\nport = \"web\"\n",
		ports.Port{Name: "web:80", Service: "web", ContainerPort: 80, HostPort: 20001, Policy: ports.Assign})
	fakeBerth(t, state, repo, "b2", "app", "app.feat-x.myapp.localhost", "[nope]\n")

	got := targetOf(newProxyRoutes(io.Discard, 80).refresh(), "api.app.feat-x.myapp.localhost")

	if got.Port != 0 || !strings.Contains(got.Refused, "config") {
		t.Errorf("a name under the berth with a broken config goes to %+v; want it refused naming the config", got)
	}
}

// The proxy may run as root, so it must never leave a file in the user's state directory.
func TestProxyRefreshWritesNothingToTheStateDirectory(t *testing.T) {
	root, state := fakeRegistry(t)
	repo := fakeMain(t, root, "myapp", "", "")
	fakeBerth(t, state, repo, "a1", "feat-x", "feat-x.myapp.localhost", hostConfig)
	before := treeOf(t, state)

	newProxyRoutes(io.Discard, 80).refresh()

	if after := treeOf(t, state); !reflect.DeepEqual(after, before) {
		t.Errorf("state directory after a refresh %v, want it as before %v", after, before)
	}
}

func treeOf(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data := ""
		if !d.IsDir() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			data = string(b)
		}
		out[path] = info.Mode().String() + " " + data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestProxySaysOnceThatItCannotReadTheRegistry(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory this user can't read")
	}
	_, state := fakeRegistry(t)
	berths := filepath.Join(state, "berths")
	if err := os.MkdirAll(berths, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(berths, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(berths, 0o755) })
	var log strings.Builder
	routes := newProxyRoutes(&log, 80)

	for range 3 {
		routes.refresh()
	}

	if n := strings.Count(log.String(), "reading the registry"); n != 1 {
		t.Errorf("said %d times that it can't read the registry, want once:\n%s", n, log.String())
	}
}

const webConfig = hostConfig + "[compose.ports]\n\"web:80\" = \"assign\"\n[proxy]\nport = \"web\"\n"

func webPort(port int) ports.Port {
	return ports.Port{Name: "web:80", Service: "web", ContainerPort: 80, HostPort: port, Policy: ports.Assign}
}

func TestProxyKeepsServingTheLastTableWhenTheRegistryCannotBeRead(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory this user can't read")
	}
	root, state := fakeRegistry(t)
	repo := fakeMain(t, root, "myapp", "", "")
	fakeBerth(t, state, repo, "a1", "feat-x", "feat-x.myapp.localhost", webConfig, webPort(20001))
	routes := newProxyRoutes(io.Discard, 80)
	routes.refresh()
	berths := filepath.Join(state, "berths")
	if err := os.Chmod(berths, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(berths, 0o755) })

	if got := targetOf(routes.refresh(), "feat-x.myapp.localhost"); got.Port != 20001 {
		t.Errorf("feat-x while the registry can't be read goes to %+v, want its port 20001 as before", got)
	}
}

func TestProxyKeepsTheHostOfABerthWhoseRecordCannotBeRead(t *testing.T) {
	root, state := fakeRegistry(t)
	repo := fakeMain(t, root, "myapp", "", "")
	fakeBerth(t, state, repo, "a1", "feat-x", "feat-x.myapp.localhost", webConfig, webPort(20001))
	fakeBerth(t, state, repo, "b2", "app", "app.feat-x.myapp.localhost", webConfig, webPort(20002))
	routes := newProxyRoutes(io.Discard, 80)
	routes.refresh()
	mustWrite(t, filepath.Join(state, "berths", "b2.json"), "{")

	b, target, _ := routes.refresh().Lookup("api.app.feat-x.myapp.localhost")

	if b.Name != "app" || target.Port != 20002 {
		t.Errorf("a name under app's host, with app's record unreadable, goes to %s on %+v; want app's own port 20002", b.Name, target)
	}
}

func TestProxyRefusesTheBerthsOfARepositoryWhoseMainsComposeCannotBeRead(t *testing.T) {
	root, state := fakeRegistry(t)
	repo := fakeMain(t, root, "myapp", "", "services: [\n")
	fakeBerth(t, state, repo, "a1", "feat-x", "feat-x.myapp.localhost", webConfig, webPort(20001))

	got := targetOf(newProxyRoutes(io.Discard, 80).refresh(), "feat-x.myapp.localhost")

	if got.Port != 0 || !strings.Contains(got.Refused, "compose project could not be read") {
		t.Errorf("feat-x, with main's compose file broken, goes to %+v; want it refused naming main's compose project", got)
	}
}

func TestProxyKeepsTheLastGoodReadOfMainsPublishedPorts(t *testing.T) {
	root, state := fakeRegistry(t)
	repo := fakeMain(t, root, "myapp", "", "services:\n  web:\n    image: scratch\n    ports:\n      - \"20009:80\"\n")
	fakeBerth(t, state, repo, "a1", "feat-x", "feat-x.myapp.localhost", webConfig, webPort(20009))
	routes := newProxyRoutes(io.Discard, 80)
	routes.refresh()
	mustWrite(t, filepath.Join(repo, "compose.yaml"), "services: [\n")
	read := routes.published[repo]
	read.at = time.Time{}
	routes.published[repo] = read

	if got := targetOf(routes.refresh(), "feat-x.myapp.localhost"); got.Port != 0 || !strings.Contains(got.Refused, "main uses") {
		t.Errorf("feat-x, on the port main last published, goes to %+v once main's compose file breaks; want it refused as main's", got)
	}
}

func TestProxySaysOnceThatMainsComposeCannotBeRead(t *testing.T) {
	root, state := fakeRegistry(t)
	repo := fakeMain(t, root, "myapp", "", "services: [\n")
	fakeBerth(t, state, repo, "a1", "feat-x", "feat-x.myapp.localhost", webConfig, webPort(20001))
	var log strings.Builder
	routes := newProxyRoutes(&log, 80)

	for range 3 {
		routes.refresh()
		read := routes.published[repo]
		read.at = time.Time{}
		routes.published[repo] = read
	}

	if n := strings.Count(log.String(), "reading main's compose project"); n != 1 {
		t.Errorf("said %d times that main's compose project can't be read, want once:\n%s", n, log.String())
	}
}
