//go:build e2e

package e2e

import (
	"net/http"
	"net/http/cookiejar"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

var wideDomains = []struct{ name, attr, bare string }{
	{"project domain", ".myapp.localhost", "myapp.localhost"},
	{"localhost", "localhost", "localhost"},
}

// namesDomain says whether line names domain itself, not a longer name ending in it,
// so feat-x.myapp.localhost does not count as naming myapp.localhost.
func namesDomain(line, domain string) bool {
	return regexp.MustCompile(`(^|[^A-Za-z0-9.-])\.?` + regexp.QuoteMeta(domain) + `($|[^A-Za-z0-9-])`).MatchString(line)
}

func linesWith(text, s string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, s) {
			out = append(out, line)
		}
	}
	return out
}

func waitForProxyOutput(t *testing.T, p *tlsProxy, s string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(p.out.String(), s) {
		if time.Now().After(deadline) {
			t.Fatalf("the proxy's output does not mention %s within 5s:\n%s", s, p.out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitForCookieFinding runs doctor without --probe until it reports the cookie the
// proxy saw, since the proxy may record it a moment after answering.
func waitForCookieFinding(t *testing.T, repo *Repo, dir, cookie string) probeResult {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		d := probeDoctor(t, repo, dir)
		for _, f := range d.with("cookie_domain") {
			if strings.Contains(f.Message, cookie) {
				return d
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("berth doctor without --probe does not report the cookie %s the proxy saw within 10s:\n%s", cookie, d.Raw)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

type cookieBerths struct {
	p        *tlsProxy
	featX    string
	featXApp *fixtureApp
	shop     string
	shopApp  *fixtureApp
}

func startCookieBerths(t *testing.T, repo *Repo, proxyExtra string, routes map[string]http.HandlerFunc) cookieBerths {
	t.Helper()
	httpPort, httpsPort := proxyTLSUserConfig(t, repo, "")
	proxyRepoConfig(t, repo, proxyExtra)
	featX, fs := addProxiedBerth(t, repo, "feat-x")
	shop, ss := addProxiedBerth(t, repo, "shop")
	b := cookieBerths{featX: featX, shop: shop}
	b.featXApp = serveFixtureApp(t, fs.port(t, "web", 80).HostPort, routes)
	b.shopApp = serveFixtureApp(t, ss.port(t, "web", 80).HostPort, nil)
	b.p = startTLSProxy(t, repo, httpPort, httpsPort)
	return b
}

func TestProxyReportsACookieDomainWiderThanTheBerth(t *testing.T) {
	for _, wide := range wideDomains {
		t.Run(wide.name, func(t *testing.T) {
			repo := NewRepo(t)
			value := "v" + randomHex(t, 8)
			b := startCookieBerths(t, repo, "", map[string]http.HandlerFunc{
				"/login": setCookie("wide_session=" + value + "; Domain=" + wide.attr + "; Path=/"),
			})

			b.p.mustGetTLS(t, b.p.client(nil), "https://feat-x.myapp.localhost/login")
			waitForProxyOutput(t, b.p, "wide_session")
			out := b.p.out.String()
			named := false
			for _, line := range linesWith(out, "wide_session") {
				named = named || namesDomain(line, wide.bare)
			}
			if !named {
				t.Errorf("the proxy's output names wide_session but not its domain %s:\n%s", wide.attr, out)
			}
			if strings.Contains(out, value) {
				t.Errorf("the proxy's output carries the cookie's value:\n%s", out)
			}
		})
	}
}

func TestDoctorReportsTheWideCookieDomainTheProxySaw(t *testing.T) {
	for _, wide := range wideDomains {
		t.Run(wide.name, func(t *testing.T) {
			repo := NewRepo(t)
			value := "v" + randomHex(t, 8)
			b := startCookieBerths(t, repo, "", map[string]http.HandlerFunc{
				"/login": setCookie("wide_session=" + value + "; Domain=" + wide.attr + "; Path=/"),
			})

			b.p.mustGetTLS(t, b.p.client(nil), "https://feat-x.myapp.localhost/login")
			seen := len(b.featXApp.requests())
			d := waitForCookieFinding(t, repo, b.featX, "wide_session")
			named := false
			for _, f := range d.with("cookie_domain") {
				named = named || (strings.Contains(f.Message, "wide_session") && namesDomain(f.Message, wide.bare))
			}
			if !named {
				t.Errorf("doctor --json reports no cookie_domain finding naming wide_session and %s:\n%s", wide.attr, d.Raw)
			}
			if strings.Contains(d.Raw, value) {
				t.Errorf("doctor --json carries the cookie's value:\n%s", d.Raw)
			}

			text := repo.Berth(t, b.featX, "doctor")
			lines := linesWith(text.Stdout, "wide_session")
			named = false
			for _, line := range lines {
				named = named || namesDomain(line, wide.bare)
			}
			if !named {
				t.Errorf("doctor does not report wide_session with its domain %s:\n%s%s", wide.attr, text.Stdout, text.Stderr)
			}
			if strings.Contains(text.Stdout+text.Stderr, value) {
				t.Errorf("doctor's output carries the cookie's value:\n%s%s", text.Stdout, text.Stderr)
			}
			if n := len(b.featXApp.requests()); n != seen {
				t.Errorf("doctor without --probe made %d requests to the app", n-seen)
			}

			if fs := probeDoctor(t, repo, b.shop).with("cookie_domain"); len(fs) != 0 {
				t.Errorf("doctor in shop reports a cookie only feat-x's app set: %+v", fs)
			}
		})
	}
}

func TestProxyRewriteKeepsAWideCookieInsideTheBerth(t *testing.T) {
	for _, wide := range wideDomains {
		t.Run(wide.name, func(t *testing.T) {
			repo := NewRepo(t)
			value := "v" + randomHex(t, 8)
			b := startCookieBerths(t, repo, "cookies = \"rewrite\"\n", map[string]http.HandlerFunc{
				"/login": setCookie("wide_session=" + value + "; Domain=" + wide.attr + "; Path=/"),
			})
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			client := b.p.client(jar)

			b.p.mustGetTLS(t, client, "https://feat-x.myapp.localhost/login")
			for _, host := range []string{"feat-x.myapp.localhost", "app.feat-x.myapp.localhost", "console.feat-x.myapp.localhost"} {
				b.p.mustGetTLS(t, client, "https://"+host+"/check")
				b.featXApp.expectRequest(t, host, "/check")
				for _, r := range b.featXApp.requests() {
					if r.Host == host && r.Path == "/check" && !strings.Contains(r.Cookie, "wide_session="+value) {
						t.Errorf("the browser did not send wide_session to %s after the rewrite; Cookie: %q", host, r.Cookie)
					}
				}
			}

			b.p.mustGetTLS(t, client, "https://shop.myapp.localhost/check")
			b.shopApp.expectRequest(t, "shop.myapp.localhost", "/check")
			for _, r := range b.shopApp.requests() {
				if strings.Contains(r.Cookie, "wide_session") {
					t.Errorf("feat-x's cookie wide_session reached the berth shop after the rewrite; Cookie: %q", r.Cookie)
				}
			}
		})
	}
}

func TestProxyPassesTheBerthsOwnCookieDomainsUntouched(t *testing.T) {
	own := []string{
		"own_session=v1; Domain=feat-x.myapp.localhost; Path=/; HttpOnly; SameSite=Lax",
		"app_pref=v2; domain=app.feat-x.myapp.localhost; path=/; secure",
	}
	for _, mode := range []struct{ name, config string }{{"warn", ""}, {"rewrite", "cookies = \"rewrite\"\n"}} {
		t.Run(mode.name, func(t *testing.T) {
			repo := NewRepo(t)
			b := startCookieBerths(t, repo, mode.config, map[string]http.HandlerFunc{
				"/own": func(w http.ResponseWriter, r *http.Request) {
					for _, c := range own {
						w.Header().Add("Set-Cookie", c)
					}
					w.Write([]byte("ok\n"))
				},
				"/wide": setCookie("wide_marker=x; Domain=.myapp.localhost; Path=/"),
			})
			client := b.p.client(nil)

			resp, _ := b.p.mustGetTLS(t, client, "https://app.feat-x.myapp.localhost/own")
			if got := resp.Header.Values("Set-Cookie"); !reflect.DeepEqual(got, own) {
				t.Errorf("Set-Cookie through the proxy:\n%q\nwant exactly what the app sent:\n%q", got, own)
			}

			b.p.mustGetTLS(t, client, "https://app.feat-x.myapp.localhost/wide")
			waitForProxyOutput(t, b.p, "wide_marker")
			d := waitForCookieFinding(t, repo, b.featX, "wide_marker")
			for _, name := range []string{"own_session", "app_pref"} {
				if strings.Contains(b.p.out.String(), name) {
					t.Errorf("the proxy reports %s, whose domain is the berth's own:\n%s", name, b.p.out.String())
				}
				for _, f := range d.with("cookie_domain") {
					if strings.Contains(f.Message, name) {
						t.Errorf("doctor reports %s, whose domain is the berth's own: %+v", name, f)
					}
				}
			}
		})
	}
}
