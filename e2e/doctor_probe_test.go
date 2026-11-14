//go:build e2e

package e2e

import (
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/BurntSushi/toml"
)

type probeFinding struct {
	Check    string          `json:"check"`
	Level    string          `json:"level"`
	Location string          `json:"location"`
	Message  string          `json:"message"`
	Fix      json.RawMessage `json:"fix"`
}

type probeResult struct {
	Findings []probeFinding `json:"findings"`
	Exit     int
	Raw      string
}

func probeDoctor(t *testing.T, repo *Repo, dir string, args ...string) probeResult {
	t.Helper()
	res := repo.Berth(t, dir, append([]string{"doctor", "--json"}, args...)...)
	var d probeResult
	if err := json.Unmarshal([]byte(res.Stdout), &d); err != nil || d.Findings == nil {
		t.Fatalf("berth doctor --json %v: %v\nstdout:\n%s\nstderr:\n%s", args, err, res.Stdout, res.Stderr)
	}
	d.Exit, d.Raw = res.Exit, res.Stdout
	return d
}

func (d probeResult) with(check string) []probeFinding {
	var out []probeFinding
	for _, f := range d.Findings {
		if f.Check == check {
			out = append(out, f)
		}
	}
	return out
}

func (d probeResult) one(t *testing.T, check string) probeFinding {
	t.Helper()
	fs := d.with(check)
	if len(fs) != 1 {
		t.Fatalf("doctor reports %d %s findings, want 1:\n%s", len(fs), check, d.Raw)
	}
	return fs[0]
}

type seenRequest struct {
	Host, Path, Cookie, Authorization string
}

// fixtureApp stands in for the user's running app, which berth never starts.
type fixtureApp struct {
	mu   sync.Mutex
	seen []seenRequest
}

func serveFixtureApp(t *testing.T, port int, routes map[string]http.HandlerFunc) *fixtureApp {
	t.Helper()
	l, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("listening on 127.0.0.1:%d: %v", port, err)
	}
	app := &fixtureApp{}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		app.mu.Lock()
		app.seen = append(app.seen, seenRequest{r.Host, r.URL.Path, r.Header.Get("Cookie"), r.Header.Get("Authorization")})
		app.mu.Unlock()
		if h, ok := routes[r.URL.Path]; ok {
			h(w, r)
			return
		}
		w.Write([]byte("ok\n"))
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return app
}

func (a *fixtureApp) requests() []seenRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]seenRequest{}, a.seen...)
}

func (a *fixtureApp) expectRequest(t *testing.T, host, path string) {
	t.Helper()
	for _, r := range a.requests() {
		if r.Host == host && r.Path == path {
			return
		}
	}
	t.Errorf("the app was not asked for %s with Host %s; it saw %+v", path, host, a.requests())
}

func (a *fixtureApp) expectNoCredentials(t *testing.T) {
	t.Helper()
	for _, r := range a.requests() {
		if r.Cookie != "" || r.Authorization != "" {
			t.Errorf("the probe sent credentials: %+v", r)
		}
	}
}

func setCookie(value string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", value)
		w.Write([]byte("ok\n"))
	}
}

type probedBerth struct {
	Dir, Host, URL string
	Port           int
}

// hostAndPort is the berth's address as the user writes it, which every probe location
// must carry instead of the loopback address the request went to.
func (b probedBerth) hostAndPort() string { return b.Host + ":" + strconv.Itoa(b.Port) }

// newProbedBerth gives the berth a host under the project's name, such as
// feat-x.myapp.localhost, so a cookie for the whole project is wider than the berth.
func newProbedBerth(t *testing.T, repo *Repo, extraConfig string) probedBerth {
	t.Helper()
	writeFile(t, repo.privateConfig(), `[berth]
host = "{name}.`+repo.Project+`.localhost"
url = "http://{host}:{port.web}"

[compose.ports]
"web:80" = "assign"
`+extraConfig)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add")
	s := portsStatusOf(t, repo, wt)
	b := probedBerth{Dir: wt, Host: s.Host, URL: s.URL, Port: s.port(t, "web", 80).HostPort}
	if !strings.HasSuffix(b.Host, "."+repo.Project+".localhost") || b.URL != "http://"+b.hostAndPort() {
		t.Fatalf("status gives host %q and url %q, want a host under %s.localhost and a URL on port %d", b.Host, b.URL, repo.Project, b.Port)
	}
	return b
}

func mainWebPort(t *testing.T, repo *Repo) int {
	t.Helper()
	return atoi(t, readDotEnv(t, filepath.Join(repo.Dir, ".env"))["WEB_PORT"])
}

// A cookie for the whole project's domain is reported, naming the cookie and
// the URL that set it, and doctor without --probe sends nothing.
func TestDoctorProbeFindsCookieDomainWiderThanTheBerth(t *testing.T) {
	repo := NewRepo(t)
	b := newProbedBerth(t, repo, "")
	wide := "." + repo.Project + ".localhost"
	app := serveFixtureApp(t, b.Port, map[string]http.HandlerFunc{
		"/": setCookie("wide_session=abc; Domain=" + wide + "; Path=/"),
	})

	plain := probeDoctor(t, repo, b.Dir)
	if n := len(app.requests()); n != 0 {
		t.Errorf("doctor without --probe made %d requests to the app", n)
	}
	if fs := plain.with("cookie_domain"); len(fs) != 0 {
		t.Errorf("doctor without --probe reports cookie findings: %+v", fs)
	}

	d := probeDoctor(t, repo, b.Dir, "--probe")
	f := d.one(t, "cookie_domain")
	if f.Level != "problem" {
		t.Errorf("cookie_domain level %q, want problem", f.Level)
	}
	if !strings.Contains(f.Location, b.hostAndPort()) || strings.Contains(f.Location, "127.0.0.1") {
		t.Errorf("cookie_domain location %q, want the berth's URL %s", f.Location, b.URL)
	}
	if !strings.Contains(f.Message, "wide_session") || !strings.Contains(f.Message, repo.Project+".localhost") {
		t.Errorf("cookie_domain message %q does not name the cookie wide_session and the domain %s", f.Message, wide)
	}
	if len(f.Fix) != 0 {
		t.Errorf("cookie_domain finding has a fix although no .env key holds %s: %s", wide, f.Fix)
	}
	if d.Exit != 1 {
		t.Errorf("doctor --probe with a wide cookie exited %d, want 1", d.Exit)
	}
	app.expectRequest(t, b.hostAndPort(), "/")
	app.expectNoCredentials(t)
}

// Cookies for the berth's own host, or with no domain, give no finding.
func TestDoctorProbeAcceptsTheBerthsOwnCookieDomain(t *testing.T) {
	repo := NewRepo(t)
	b := newProbedBerth(t, repo, "")
	app := serveFixtureApp(t, b.Port, map[string]http.HandlerFunc{
		"/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("Set-Cookie", "own_session=abc; Domain="+b.Host+"; Path=/")
			w.Header().Add("Set-Cookie", "host_only=1; Path=/")
			w.Write([]byte("ok\n"))
		},
	})

	d := probeDoctor(t, repo, b.Dir, "--probe")
	app.expectRequest(t, b.hostAndPort(), "/")
	if fs := d.with("cookie_domain"); len(fs) != 0 {
		t.Errorf("doctor reports cookies scoped to the berth's own host %s: %+v", b.Host, fs)
	}
	if fs := d.with("probe"); len(fs) != 0 {
		t.Errorf("doctor reports the answering app as not probed: %+v", fs)
	}
}

// A redirect to main's localhost port is reported with the URL that sent
// it, and the probe does not follow it out of the berth.
func TestDoctorProbeReportsRedirectToMain(t *testing.T) {
	repo := NewRepo(t)
	b := newProbedBerth(t, repo, "")
	mainPort := mainWebPort(t, repo)
	target := "http://localhost:" + strconv.Itoa(mainPort) + "/dashboard"
	mainApp := serveFixtureApp(t, mainPort, nil)
	app := serveFixtureApp(t, b.Port, map[string]http.HandlerFunc{
		"/": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target, http.StatusFound) },
	})

	d := probeDoctor(t, repo, b.Dir, "--probe")
	f := d.one(t, "redirect")
	if f.Level != "problem" {
		t.Errorf("redirect level %q, want problem", f.Level)
	}
	if !strings.Contains(f.Location, b.hostAndPort()) || strings.Contains(f.Location, "127.0.0.1") {
		t.Errorf("redirect location %q, want the berth's URL %s that sent it", f.Location, b.URL)
	}
	if !strings.Contains(f.Message, "localhost:"+strconv.Itoa(mainPort)) {
		t.Errorf("redirect message %q does not name the target %s", f.Message, target)
	}
	if d.Exit != 1 {
		t.Errorf("doctor --probe with a redirect to main exited %d, want 1", d.Exit)
	}
	if seen := mainApp.requests(); len(seen) != 0 {
		t.Errorf("the probe requested main's app: %+v", seen)
	}
	for _, r := range app.requests() {
		if strings.HasPrefix(r.Host, "localhost") {
			t.Errorf("the probe followed the redirect to main: %+v", r)
		}
	}
}

// Redirects that stay within the berth, relative or to a name under its host, are
// followed and the cookies of the page they lead to are checked, without sending back
// the cookies earlier pages set.
func TestDoctorProbeFollowsRedirectsWithinTheBerth(t *testing.T) {
	repo := NewRepo(t)
	b := newProbedBerth(t, repo, "")
	app := "app." + b.hostAndPort()
	server := serveFixtureApp(t, b.Port, map[string]http.HandlerFunc{
		"/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("Set-Cookie", "visit=1; Path=/")
			http.Redirect(w, r, "/home", http.StatusFound)
		},
		"/home": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://"+app+"/welcome", http.StatusFound)
		},
		"/welcome": setCookie("wide_session=abc; Domain=." + repo.Project + ".localhost; Path=/"),
	})

	d := probeDoctor(t, repo, b.Dir, "--probe")
	server.expectRequest(t, b.hostAndPort(), "/home")
	server.expectRequest(t, app, "/welcome")
	f := d.one(t, "cookie_domain")
	if !strings.Contains(f.Location, app+"/welcome") {
		t.Errorf("cookie_domain location %q, want the page that set it, http://%s/welcome", f.Location, app)
	}
	if !strings.Contains(f.Message, "wide_session") {
		t.Errorf("cookie_domain message %q does not name wide_session", f.Message)
	}
	if fs := d.with("redirect"); len(fs) != 0 {
		t.Errorf("doctor reports redirects that stay within the berth: %+v", fs)
	}
	server.expectNoCredentials(t)
}

// With nothing listening the probe reports the URL as not verified, starts
// nothing and exits as a warning would.
func TestDoctorProbeWithNothingListeningIsNotVerified(t *testing.T) {
	repo := NewRepo(t)
	b := newProbedBerth(t, repo, "")
	project := stateOf(t, repo, b.Dir).ComposeProject

	d := probeDoctor(t, repo, b.Dir, "--probe")
	f := d.one(t, "probe")
	if f.Level != "not_verified" {
		t.Errorf("probe level %q, want not_verified", f.Level)
	}
	if !strings.Contains(f.Location, b.hostAndPort()) {
		t.Errorf("probe location %q, want the berth's URL %s", f.Location, b.URL)
	}
	if !strings.Contains(strings.ToLower(f.Message), "answer") {
		t.Errorf("probe message %q does not say nothing answered", f.Message)
	}
	if d.Exit != 0 || len(d.Findings) != 1 {
		t.Errorf("doctor --probe with only a not verified probe: exit %d, want 0 and that one finding:\n%s", d.Exit, d.Raw)
	}
	if !portFree(b.Port) {
		t.Errorf("something listens on the berth's port %d after the probe", b.Port)
	}
	if svcs := running(t, project); len(svcs) != 0 {
		t.Errorf("the probe started containers of %s: %v", project, svcs)
	}

	human := repo.Berth(t, b.Dir, "doctor", "--probe")
	if !strings.Contains(human.Stdout, "not verified") || !strings.Contains(human.Stdout, b.hostAndPort()) {
		t.Errorf("human doctor --probe does not report %s as not verified:\n%s", b.URL, human.Stdout)
	}
}

// A [doctor] probe URL under the berth's host is requested with that host at
// the loopback address, so it needs no DNS, and a cookie for that name gives no finding.
func TestDoctorProbeRequestsConfiguredURLsWithTheirHost(t *testing.T) {
	repo := NewRepo(t)
	b := newProbedBerth(t, repo, "\n[doctor]\nprobe = [\"http://app.{host}:{port.web}/login\"]\n")
	app := "app." + b.hostAndPort()
	server := serveFixtureApp(t, b.Port, map[string]http.HandlerFunc{
		"/login": setCookie("login_session=abc; Domain=app." + b.Host + "; Path=/"),
	})

	probeDoctor(t, repo, b.Dir)
	if n := len(server.requests()); n != 0 {
		t.Errorf("doctor without --probe made %d requests to the app", n)
	}

	d := probeDoctor(t, repo, b.Dir, "--probe")
	server.expectRequest(t, app, "/login")
	server.expectRequest(t, b.hostAndPort(), "/")
	if fs := d.with("cookie_domain"); len(fs) != 0 {
		t.Errorf("doctor reports a cookie scoped to app.%s, a name under the berth's host: %+v", b.Host, fs)
	}
	if fs := d.with("probe"); len(fs) != 0 {
		t.Errorf("doctor reports a configured URL as not probed although the app answers: %+v", fs)
	}
	server.expectNoCredentials(t)
}

// In main the probe checks are skipped and say so, with no request made.
func TestDoctorProbeInMainIsSkipped(t *testing.T) {
	repo := NewRepo(t)
	writeFile(t, repo.privateConfig(), `[berth]
host = "{name}.`+repo.Project+`.localhost"
url = "http://{host}:{port.web}"

[compose.ports]
"web:80" = "assign"

[doctor]
probe = ["http://app.{host}:{port.web}/login"]
`)
	mainApp := serveFixtureApp(t, mainWebPort(t, repo), nil)

	d := probeDoctor(t, repo, repo.Dir, "--probe")
	f := d.one(t, "probe")
	if f.Level != "not_verified" || !strings.Contains(f.Message, "skipped") || !strings.Contains(f.Message, "main") {
		t.Errorf("probe finding in main = %+v, want not_verified saying the checks are skipped in main", f)
	}
	if d.Exit != 0 {
		t.Errorf("doctor --probe in main exited %d, want 0:\n%s", d.Exit, d.Raw)
	}
	human := repo.Berth(t, repo.Dir, "doctor", "--probe")
	if !strings.Contains(human.Stdout, "skipped") {
		t.Errorf("human doctor --probe in main does not say the probe is skipped:\n%s%s", human.Stdout, human.Stderr)
	}
	if seen := mainApp.requests(); len(seen) != 0 {
		t.Errorf("doctor --probe in main made requests: %+v", seen)
	}
}

// --json gives a machine-applicable fix when the wide domain comes from a key in the
// berth's .env: set that key to the berth's host.
func TestDoctorProbeOffersAFixForAnEnvCookieDomain(t *testing.T) {
	repo := NewRepo(t)
	wide := "." + repo.Project + ".localhost"
	appendFile(t, filepath.Join(repo.Dir, ".env"), "COOKIE_DOMAIN="+wide+"\n")
	b := newProbedBerth(t, repo, "")
	serveFixtureApp(t, b.Port, map[string]http.HandlerFunc{
		"/": setCookie("wide_session=abc; Domain=" + wide + "; Path=/"),
	})

	f := probeDoctor(t, repo, b.Dir, "--probe").one(t, "cookie_domain")
	var fix struct {
		Config string `json:"config"`
	}
	if err := json.Unmarshal(f.Fix, &fix); err != nil || fix.Config == "" {
		t.Fatalf("cookie_domain finding has no fix with a config snippet (%v): %+v", err, f)
	}
	var snippet struct {
		Env struct {
			Set map[string]string `toml:"set"`
		} `toml:"env"`
	}
	if _, err := toml.Decode(fix.Config, &snippet); err != nil {
		t.Fatalf("fix config is not TOML: %v\n%s", err, fix.Config)
	}
	if got := snippet.Env.Set["COOKIE_DOMAIN"]; got != "{host}" || len(snippet.Env.Set) != 1 {
		t.Errorf("fix sets %v, want only COOKIE_DOMAIN = \"{host}\":\n%s", snippet.Env.Set, fix.Config)
	}
}
