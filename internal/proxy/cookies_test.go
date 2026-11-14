package proxy

import (
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func cookieApp(t *testing.T, setCookie ...string) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, c := range setCookie {
			w.Header().Add("Set-Cookie", c)
		}
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return l.Addr().(*net.TCPAddr).Port
}

// A routed name is under the berth's host, so a wide cookie it sets is the berth's to
// keep, and is scoped to the berth's host rather than the routed name.
func TestAWideCookieFromARoutedNameIsRewrittenToTheBerthsHost(t *testing.T) {
	web := cookieApp(t)
	vite := cookieApp(t, "s=v; Domain=.myapp.localhost; Path=/")
	s := NewServer(80)
	var heard []SetCookie
	s.Cookies = func(_ Berth, c SetCookie) { heard = append(heard, c) }
	s.SetTable(Table{{Name: "feat-x", Host: "feat-x.myapp.localhost", RewriteCookies: true, Port: Target{Port: web},
		Routes: map[string]Target{"vite.feat-x.myapp.localhost": {Port: vite}}}})
	rec := httptest.NewRecorder()

	s.ServeHTTP(rec, loopbackRequest("http://vite.feat-x.myapp.localhost/x?token=secret"))

	if got := rec.Header().Values("Set-Cookie"); !slices.Equal(got, []string{"s=v; Domain=feat-x.myapp.localhost; Path=/"}) {
		t.Errorf("Set-Cookie through the proxy %q, want the domain replaced with the berth's host", got)
	}
	want := SetCookie{Name: "s", Domain: ".myapp.localhost", URL: "http://vite.feat-x.myapp.localhost/x", Wide: true, Rewritten: true}
	if len(heard) != 1 || heard[0] != want {
		t.Errorf("the proxy reported %+v, want %+v", heard, want)
	}
}

func TestAWideCookieIsReportedButKeptWithoutRewrite(t *testing.T) {
	raw := "s=v; Domain=localhost; Path=/"
	port := cookieApp(t, raw)
	s := NewServer(80)
	var heard []SetCookie
	s.Cookies = func(_ Berth, c SetCookie) { heard = append(heard, c) }
	s.SetTable(Table{{Name: "feat-x", Host: "feat-x.myapp.localhost", Port: Target{Port: port}}})
	rec := httptest.NewRecorder()

	s.ServeHTTP(rec, loopbackRequest("http://feat-x.myapp.localhost/"))

	if got := rec.Header().Values("Set-Cookie"); !slices.Equal(got, []string{raw}) {
		t.Errorf("Set-Cookie through the proxy %q, want %q untouched", got, raw)
	}
	if len(heard) != 1 || !heard[0].Wide || heard[0].Rewritten {
		t.Errorf("the proxy reported %+v, want one wide cookie, not rewritten", heard)
	}
}

// Browsers store these though Go's cookie parser refuses their name or value, so the
// proxy reads the header itself.
func TestCookiesGoCannotParseAreReportedAndRewrittenAll(t *testing.T) {
	raws := []string{
		"cart[items]=3; Domain=.myapp.localhost; Path=/",
		`prefs={"theme":"dark"}; Domain=.myapp.localhost; Path=/`,
		"name=café; Domain=.myapp.localhost; Path=/",
	}
	port := cookieApp(t, raws...)
	s := NewServer(80)
	var heard []SetCookie
	s.Cookies = func(_ Berth, c SetCookie) { heard = append(heard, c) }
	s.SetTable(Table{{Name: "feat-x", Host: "feat-x.myapp.localhost", RewriteCookies: true, Port: Target{Port: port}}})
	rec := httptest.NewRecorder()

	s.ServeHTTP(rec, loopbackRequest("http://feat-x.myapp.localhost/"))

	var want []string
	for _, raw := range raws {
		want = append(want, strings.Replace(raw, "Domain=.myapp.localhost", "Domain=feat-x.myapp.localhost", 1))
	}
	if got := rec.Header().Values("Set-Cookie"); !slices.Equal(got, want) {
		t.Errorf("Set-Cookie through the proxy:\n%q\nwant only each domain replaced:\n%q", got, want)
	}
	var names []string
	for _, c := range heard {
		if c.Wide && c.Rewritten && c.Domain == ".myapp.localhost" {
			names = append(names, c.Name)
		}
	}
	if !slices.Equal(names, []string{"cart[items]", "prefs", "name"}) {
		t.Errorf("the proxy reported %+v, want cart[items], prefs and name as wide and rewritten", heard)
	}
}

// The hook hears a cookie scoped to the berth too, so what doctor recorded for its name
// can be forgotten once the app sets it within the berth.
func TestTheCookieHookHearsACookieScopedToTheBerthAsNotWide(t *testing.T) {
	port := cookieApp(t, "wide_session=v; Domain=feat-x.myapp.localhost; Path=/")
	s := NewServer(80)
	var heard []SetCookie
	s.Cookies = func(_ Berth, c SetCookie) { heard = append(heard, c) }
	s.SetTable(Table{{Name: "feat-x", Host: "feat-x.myapp.localhost", Port: Target{Port: port}}})

	s.ServeHTTP(httptest.NewRecorder(), loopbackRequest("http://feat-x.myapp.localhost/"))

	want := SetCookie{Name: "wide_session", Domain: "feat-x.myapp.localhost", URL: "http://feat-x.myapp.localhost/"}
	if len(heard) != 1 || heard[0] != want {
		t.Errorf("the hook heard %+v, want %+v", heard, want)
	}
}
