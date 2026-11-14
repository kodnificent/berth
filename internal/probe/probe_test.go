package probe

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The berth's host is under .invalid, which never resolves, so a request that arrives
// proves the probe looked no name up.
const berthHost = "feat-x.myapp.invalid"

type app struct {
	*httptest.Server
	mu   sync.Mutex
	seen []*http.Request
}

func serve(t *testing.T, h http.HandlerFunc) *app {
	t.Helper()
	a := &app{}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.seen = append(a.seen, r.Clone(r.Context()))
		a.mu.Unlock()
		if h != nil {
			h(w, r)
		}
	}))
	t.Cleanup(a.Close)
	return a
}

func (a *app) port(t *testing.T) int {
	u, err := url.Parse(a.URL)
	if err != nil {
		t.Fatal(err)
	}
	return Port(u)
}

func (a *app) requests() []*http.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]*http.Request{}, a.seen...)
}

func (a *app) at(t *testing.T, host, path string) string {
	return "http://" + host + ":" + strconv.Itoa(a.port(t)) + path
}

func TestProbeRequestsTheURLsHostAtTheLoopbackAddressWithoutDNS(t *testing.T) {
	a := serve(t, nil)
	s := Scope{Host: berthHost, Ports: []int{a.port(t)}}

	r := s.Probe([]string{a.at(t, "app."+berthHost, "/login")})

	seen := a.requests()
	if len(seen) != 1 || seen[0].Host != "app."+berthHost+":"+strconv.Itoa(a.port(t)) || seen[0].URL.Path != "/login" {
		t.Fatalf("the app saw %v, want one request for /login with Host app.%s", seen, berthHost)
	}
	if len(r.Pages) != 1 || len(r.Unchecked) != 0 {
		t.Errorf("report %+v, want the one page", r)
	}
}

func TestProbeSendsNoCredentialsFromTheURL(t *testing.T) {
	a := serve(t, nil)
	u := "http://user:secret@" + berthHost + ":" + strconv.Itoa(a.port(t)) + "/"

	r := Scope{Host: berthHost, Ports: []int{a.port(t)}}.Probe([]string{u})

	seen := a.requests()
	if len(seen) != 1 || seen[0].Header.Get("Authorization") != "" {
		t.Errorf("the app saw %v, want one request with no Authorization", seen)
	}
	if strings.Contains(r.Pages[0].URL, "secret") {
		t.Errorf("the page's URL %q keeps the credentials", r.Pages[0].URL)
	}
}

func TestProbeRefusesAURLOutsideTheBerthBeforeAnyRequest(t *testing.T) {
	a := serve(t, nil)
	s := Scope{Host: berthHost, Ports: []int{a.port(t)}}
	for _, u := range []string{
		a.at(t, "localhost", "/"),
		a.at(t, "myapp.invalid", "/"),
		a.at(t, "x"+berthHost, "/"),
		"http://" + berthHost + ":1/",
		"ftp://" + berthHost + ":" + strconv.Itoa(a.port(t)) + "/",
	} {
		r := s.Probe([]string{u})
		if len(r.Unchecked) != 1 || !strings.Contains(r.Unchecked[0].Why, "not requested") {
			t.Errorf("%s: report %+v, want it not requested", u, r)
		}
	}
	if seen := a.requests(); len(seen) != 0 {
		t.Errorf("the app was asked for a URL outside the berth: %v", seen)
	}
}

func TestProbeReportsARedirectToMainsHostWithoutFollowingIt(t *testing.T) {
	var target string
	a := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, target, http.StatusFound)
		}
	})
	target = a.at(t, "localhost", "/dashboard")

	r := Scope{Host: berthHost, Ports: []int{a.port(t)}, MainHosts: []string{"localhost"}}.Probe([]string{a.at(t, berthHost, "/")})

	if len(r.ToMain) != 1 || r.ToMain[0].To != target || r.ToMain[0].From != a.at(t, berthHost, "/") || r.ToMain[0].Why != "main's host localhost" {
		t.Errorf("redirects to main %+v, want one from / to %s on main's host", r.ToMain, target)
	}
	if n := len(a.requests()); n != 1 {
		t.Errorf("the app saw %d requests, want 1: the redirect to main is never followed", n)
	}
}

func TestProbeStopsAfterTheRedirectCap(t *testing.T) {
	a := serve(t, func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		http.Redirect(w, r, "/"+strconv.Itoa(n+1), http.StatusFound)
	})

	r := Scope{Host: berthHost, Ports: []int{a.port(t)}}.Probe([]string{a.at(t, berthHost, "/0")})

	if n := len(a.requests()); n != MaxRedirects+1 {
		t.Errorf("the app saw %d requests, want the URL and %d redirects", n, MaxRedirects)
	}
	if len(r.Unchecked) != 1 || !strings.Contains(r.Unchecked[0].Why, "stopped after") {
		t.Errorf("unchecked %+v, want the cap reported", r.Unchecked)
	}
}

func TestProbeReportsAHungAppAsNotAnswering(t *testing.T) {
	release := make(chan struct{})
	a := serve(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)

	start := time.Now()
	r := Scope{Host: berthHost, Ports: []int{a.port(t)}, Timeout: 200 * time.Millisecond}.Probe([]string{a.at(t, berthHost, "/")})

	if time.Since(start) > 5*time.Second {
		t.Errorf("the probe took %s with a 200ms timeout", time.Since(start))
	}
	if len(r.Unchecked) != 1 || !strings.Contains(r.Unchecked[0].Why, "did not answer within") {
		t.Errorf("unchecked %+v, want the URL reported as not answering", r.Unchecked)
	}
}

func TestProbeDoesNotWaitForTheBody(t *testing.T) {
	release := make(chan struct{})
	a := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "s=1; Domain=myapp.invalid")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)

	start := time.Now()
	r := Scope{Host: berthHost, Ports: []int{a.port(t)}, Timeout: 3 * time.Second}.Probe([]string{a.at(t, berthHost, "/")})

	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("the probe waited %s for a body that never ends", d)
	}
	if len(r.Pages) != 1 || len(r.Pages[0].Cookies) != 1 || len(r.Unchecked) != 0 {
		t.Errorf("report %+v, want the page and its cookie", r)
	}
}

func TestProbeReportsNothingListeningAsNotAnswered(t *testing.T) {
	a := serve(t, nil)
	port := a.port(t)
	a.Close()

	r := Scope{Host: berthHost, Ports: []int{port}}.Probe([]string{"http://" + berthHost + ":" + strconv.Itoa(port) + "/"})

	if len(r.Unchecked) != 1 || !strings.Contains(r.Unchecked[0].Why, "nothing answered") {
		t.Errorf("unchecked %+v, want nothing answered", r.Unchecked)
	}
}

func TestProbeNeverRequestsMainsPortEvenWhenListedAsTheBerths(t *testing.T) {
	main := serve(t, nil)
	berth := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, main.at(t, berthHost, "/"), http.StatusFound)
	})
	s := Scope{Host: berthHost, Ports: []int{berth.port(t), main.port(t)}, MainPorts: []int{main.port(t)}}

	r := s.Probe([]string{main.at(t, berthHost, "/"), berth.at(t, berthHost, "/")})

	if seen := main.requests(); len(seen) != 0 {
		t.Errorf("main's app was requested: %v", seen)
	}
	if len(r.Unchecked) != 1 || r.Unchecked[0].URL != main.at(t, berthHost, "/") || !strings.Contains(r.Unchecked[0].Why, "main's") {
		t.Errorf("unchecked %+v, want the URL on main's port not requested", r.Unchecked)
	}
	if len(r.ToMain) != 1 || r.ToMain[0].Why != "main's port "+strconv.Itoa(main.port(t)) {
		t.Errorf("redirects to main %+v, want the redirect onto main's port reported", r.ToMain)
	}
}

func TestProbeReportsRedirectsOntoMainsPortWithoutFollowingThem(t *testing.T) {
	main := serve(t, nil)
	mainPort := strconv.Itoa(main.port(t))
	for _, target := range []string{"http://127.0.0.1:" + mainPort + "/", "http://" + berthHost + ":" + mainPort + "/"} {
		berth := serve(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target, http.StatusFound) })
		s := Scope{Host: berthHost, Ports: []int{berth.port(t)}, MainHosts: []string{"localhost", "127.0.0.1"}, MainPorts: []int{main.port(t)}}

		r := s.Probe([]string{berth.at(t, berthHost, "/")})

		if len(r.ToMain) != 1 || r.ToMain[0].To != target {
			t.Errorf("redirect to %s: redirects to main %+v, want it reported", target, r.ToMain)
		}
	}
	if seen := main.requests(); len(seen) != 0 {
		t.Errorf("main's app was requested: %v", seen)
	}
}

func TestProbeDoesNotFollowAProtocolRelativeRedirectOutside(t *testing.T) {
	other := serve(t, nil)
	berth := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "//other.example:"+strconv.Itoa(other.port(t))+"/", http.StatusFound)
	})

	r := Scope{Host: berthHost, Ports: []int{berth.port(t), other.port(t)}}.Probe([]string{berth.at(t, berthHost, "/")})

	if seen := other.requests(); len(seen) != 0 || len(r.Pages) != 1 || len(r.ToMain) != 0 || len(r.Unchecked) != 0 {
		t.Errorf("report %+v, other saw %v; want only the redirecting page", r, seen)
	}
}

func TestProbeRefusesAURLWhoseBerthHostIsOnlyItsUserinfo(t *testing.T) {
	a := serve(t, nil)

	r := Scope{Host: berthHost, Ports: []int{a.port(t)}}.Probe([]string{"http://" + berthHost + "@other.example:" + strconv.Itoa(a.port(t)) + "/"})

	if len(a.requests()) != 0 || len(r.Unchecked) != 1 || !strings.Contains(r.Unchecked[0].Why, "other.example") {
		t.Errorf("report %+v, want it not requested since its host is other.example", r)
	}
}

func TestProbeRequestsAPageOnceHoweverItsURLIsWritten(t *testing.T) {
	var root int
	var mu sync.Mutex
	a := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		mu.Lock()
		root++
		mu.Unlock()
		w.Header().Add("Set-Cookie", "s=1; Domain=myapp.invalid")
	})
	base := "http://" + strings.ToUpper(berthHost) + ".:" + strconv.Itoa(a.port(t))

	r := Scope{Host: berthHost, Ports: []int{a.port(t)}}.Probe([]string{base, a.at(t, berthHost, "/old")})

	var roots int
	for _, p := range r.Pages {
		if u, _ := url.Parse(p.URL); u.Path == "" || u.Path == "/" {
			roots++
		}
	}
	if root != 1 || roots != 1 {
		t.Errorf("/ was requested %d times and reported as %d pages, want once: %+v", root, roots, r.Pages)
	}
}

func TestProbeReportsARedirectToTheBerthsHostOnMainsImpliedPort(t *testing.T) {
	berth := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+berthHost+"/login", http.StatusFound)
	})
	s := Scope{Host: berthHost, Ports: []int{berth.port(t)}, MainHosts: []string{"localhost"}, MainPorts: []int{80}}

	r := s.Probe([]string{berth.at(t, berthHost, "/")})

	if len(r.ToMain) != 1 || r.ToMain[0].Why != "main's port 80" || len(r.Unchecked) != 0 {
		t.Errorf("report %+v, want the redirect reported as to main's port 80", r)
	}
	if n := len(berth.requests()); n != 1 {
		t.Errorf("the app saw %d requests, want 1", n)
	}
}

func TestProbeReportsARedirectToTheBerthsHostOnAnUnassignedPortAsUnchecked(t *testing.T) {
	berth := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://"+berthHost+"/login", http.StatusFound)
	})
	s := Scope{Host: berthHost, Ports: []int{berth.port(t)}, MainHosts: []string{"localhost"}, MainPorts: []int{8000}}

	r := s.Probe([]string{berth.at(t, berthHost, "/")})

	if len(r.Unchecked) != 1 || r.Unchecked[0].URL != berth.at(t, berthHost, "/") || !strings.Contains(r.Unchecked[0].Why, "https://"+berthHost+"/login") || !strings.Contains(r.Unchecked[0].Why, "port 443") || len(r.ToMain) != 0 {
		t.Errorf("report %+v, want one unchecked entry at the sending URL naming the target and its port", r)
	}
}

func TestProbeIgnoresARedirectToAnotherSiteThatWritesMainsPort(t *testing.T) {
	berth := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://idp.example.com:8000/authorize", http.StatusFound)
	})
	s := Scope{Host: berthHost, Ports: []int{berth.port(t)}, MainHosts: []string{"localhost"}, MainPorts: []int{8000}}

	r := s.Probe([]string{berth.at(t, berthHost, "/")})

	if len(r.ToMain) != 0 || len(r.Unchecked) != 0 || len(r.Pages) != 1 {
		t.Errorf("report %+v, want only the redirecting page", r)
	}
}
