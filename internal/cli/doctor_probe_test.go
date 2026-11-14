package cli

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/ports"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/worktree"
)

const probedHost = "feat-x.myapp.localhost"

func wideCookie() *http.Cookie {
	return &http.Cookie{Name: "wide_session", Value: "s3cret-session-value", Domain: ".myapp.localhost"}
}

func TestCookieDomainFixSetsTheOneKeyHoldingTheDomain(t *testing.T) {
	env := map[string]string{"COOKIE_DOMAIN": "MyApp.localhost", "APP_NAME": "myapp"}

	f := cookieDomainFinding("http://"+probedHost+":20001/", probedHost, wideCookie(), env)

	if f == nil || f.Fix == nil || f.Fix.Config != "[env.set]\nCOOKIE_DOMAIN = \"{host}\"\n" {
		t.Errorf("finding %+v, want a fix setting COOKIE_DOMAIN to {host}", f)
	}
}

func TestCookieDomainOffersNoFixWhenSeveralKeysHoldTheDomain(t *testing.T) {
	env := map[string]string{"SESSION_DOMAIN": "localhost", "DB_HOST": "localhost", "REDIS_HOST": "localhost"}

	f := cookieDomainFinding("http://"+probedHost+":20001/", probedHost, &http.Cookie{Name: "s", Domain: "localhost"}, env)

	if f == nil || f.Fix != nil {
		t.Fatalf("finding %+v, want one with no fix", f)
	}
	for _, k := range []string{"DB_HOST", "REDIS_HOST", "SESSION_DOMAIN"} {
		if !strings.Contains(f.Message, k) {
			t.Errorf("message %q does not name the candidate %s", f.Message, k)
		}
	}
}

func TestCookieDomainOffersNoFixForAKeyBerthCannotSet(t *testing.T) {
	env := map[string]string{"cookie.domain": ".myapp.localhost"}

	if f := cookieDomainFinding("http://"+probedHost+":20001/", probedHost, wideCookie(), env); f == nil || f.Fix != nil {
		t.Errorf("finding %+v, want one with no fix", f)
	}
}

func TestCookieDomainFindingNeverCarriesTheCookiesValue(t *testing.T) {
	f := cookieDomainFinding("http://"+probedHost+":20001/", probedHost, wideCookie(), map[string]string{"COOKIE_DOMAIN": ".myapp.localhost"})

	if f == nil || f.Fix == nil {
		t.Fatalf("finding %+v, want one with a fix", f)
	}
	if text := f.Message + f.Location + f.Fix.Config; strings.Contains(text, "s3cret-session-value") {
		t.Errorf("the finding carries the cookie's value: %+v", f)
	}
}

func TestMainAddressesComeFromMainsMappedKeysWithoutImpliedPorts(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("APP_URL=http://myapp.test:8000\nASSET_URL=https://cdn.myapp.test\nAPP_HOST=api.myapp.test:8080\nVITE_PORT=5173\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(filepath.Join(dir, "berth.toml"), []byte("[env]\nurl = [\"APP_URL\", \"ASSET_URL\"]\nhost = [\"APP_HOST\"]\n[ports.vite]\nenv = \"VITE_PORT\"\n"))
	if err != nil {
		t.Fatal(err)
	}

	hosts, ports := mainAddresses(&worktree.Worktree{Repository: dir}, cfg)

	for _, h := range []string{"localhost", "myapp.test", "cdn.myapp.test", "api.myapp.test"} {
		if !slices.Contains(hosts, h) {
			t.Errorf("main's hosts %v lack %s", hosts, h)
		}
	}
	slices.Sort(ports)
	if !slices.Equal(ports, []int{5173, 8000, 8080}) {
		t.Errorf("main's ports %v, want 5173, 8000 and 8080, and not 443 for a URL that names no port", ports)
	}
}

// listen stands in for an app on a free loopback port, counting the requests it gets.
func listen(t *testing.T) (port int, requests func() int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ = strconv.Atoi(u.Port())
	return port, func() int { mu.Lock(); defer mu.Unlock(); return n }
}

func probeBerth(t *testing.T, berthURL string, mainPort, assigned int) []finding {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte("services:\n  web:\n    image: busybox\n    ports:\n      - \""+strconv.Itoa(mainPort)+":80\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(filepath.Join(dir, "berth.toml"), []byte("[berth]\nhost = \"{name}.myapp.localhost\"\nurl = \""+berthURL+"\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	rec := registry.Record{Name: "feat-x", Ports: []ports.Port{{Name: "web:80", Service: "web", ContainerPort: 80, HostPort: assigned, Policy: ports.Assign}}}
	return probeFindings(&worktree.Worktree{Path: dir, Repository: dir}, cfg, rec)
}

func TestProbeNeverRequestsABerthURLOnMainsPort(t *testing.T) {
	mainPort, mainRequests := listen(t)
	berthURL := "http://{host}:" + strconv.Itoa(mainPort)

	found := probeBerth(t, berthURL, mainPort, mainPort+1)

	if n := mainRequests(); n != 0 {
		t.Errorf("main's app got %d requests", n)
	}
	if len(found) != 1 || found[0].Check != "probe" || found[0].Level != notVerified || !strings.Contains(found[0].Message, "main's") {
		t.Errorf("findings %+v, want one not verified probe saying the port is main's", found)
	}
}

func TestProbeDoesNotRequestABerthURLOnAPortTheBerthWasNotAssigned(t *testing.T) {
	other, otherRequests := listen(t)

	found := probeBerth(t, "http://{host}:"+strconv.Itoa(other), other+1, other+2)

	if n := otherRequests(); n != 0 {
		t.Errorf("the app on port %d, assigned to no berth, got %d requests", other, n)
	}
	if len(found) != 1 || found[0].Check != "probe" || !strings.Contains(found[0].Message, "not one of this berth's ports") {
		t.Errorf("findings %+v, want one not verified probe saying the port is not the berth's", found)
	}
}
