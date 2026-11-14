// Package probe requests a berth's running app as a browser would, and reports the
// cookies it sets and the redirects it sends. It only ever connects to the loopback
// address, and it starts nothing: an app that isn't running is reported, never started.
package probe

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kodnificent/berth/internal/names"
)

// Loopback is the one address the probe connects to, whatever host a URL names, so no
// name is ever looked up and no request leaves the machine.
const Loopback = "127.0.0.1"

// MaxRedirects bounds the redirects within the berth followed from one URL.
const MaxRedirects = 10

// DefaultTimeout is how long one request may take, connecting included. Dev servers
// that build a page on its first request can take seconds.
const DefaultTimeout = 10 * time.Second

// Scope says what is the berth's and what is main's.
type Scope struct {
	// Host is the berth's host. It and every name under it are the berth's.
	Host string
	// Ports are the berth's own ports. A URL on any other port is outside the berth.
	Ports []int
	// MainHosts and MainPorts are where main is reached. A redirect to either is reported,
	// and a port in MainPorts is main's even when Ports lists it too.
	MainHosts []string
	MainPorts []int
	// Timeout bounds each request; zero is DefaultTimeout.
	Timeout time.Duration
}

// Page is one response the probe received, at the URL as the user writes it.
type Page struct {
	URL     string
	Cookies []*http.Cookie
}

// Redirect is a redirect to main, which the probe never follows.
type Redirect struct {
	From, To string
	// Why names what of main's the target is on, such as "main's port 8000".
	Why string
}

// Unchecked is a URL the probe could not check, and why.
type Unchecked struct {
	URL, Why string
}

type Report struct {
	Pages     []Page
	ToMain    []Redirect
	Unchecked []Unchecked
}

// Probe requests each URL and the redirects within the berth that follow from it. A URL
// outside the berth is never requested, and neither is any page twice.
func (s Scope) Probe(urls []string) Report {
	var r Report
	c := s.client()
	requested := map[string]bool{}
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil {
			r.Unchecked = append(r.Unchecked, Unchecked{raw, fmt.Sprintf("not requested: %v", err)})
			continue
		}
		clean(u)
		if why := s.outside(u); why != "" {
			r.Unchecked = append(r.Unchecked, Unchecked{u.String(), "not requested: " + why})
			continue
		}
		s.follow(c, u, requested, &r)
	}
	return r
}

func (s Scope) follow(c *http.Client, u *url.URL, requested map[string]bool, r *Report) {
	for hops := 0; ; hops++ {
		at := u.String()
		if requested[page(u)] {
			return
		}
		requested[page(u)] = true
		resp, err := s.get(c, u)
		if err != nil {
			r.Unchecked = append(r.Unchecked, Unchecked{at, s.noAnswer(u, err)})
			return
		}
		resp.Body.Close()
		r.Pages = append(r.Pages, Page{at, resp.Cookies()})
		if !redirect(resp.StatusCode) {
			return
		}
		next, err := resp.Location()
		if err != nil {
			return
		}
		clean(next)
		if refused := s.outside(next); refused != "" {
			switch why := s.main(next); {
			case why != "":
				r.ToMain = append(r.ToMain, Redirect{at, next.String(), why})
			case names.Within(s.Host, next.Hostname()):
				r.Unchecked = append(r.Unchecked, Unchecked{at, fmt.Sprintf("redirects to %s, which was not requested: %s", next, refused)})
			}
			return
		}
		if hops == MaxRedirects {
			r.Unchecked = append(r.Unchecked, Unchecked{at, fmt.Sprintf("stopped after %d redirects within the berth, so %s, where this one leads, was not checked", MaxRedirects, next)})
			return
		}
		u = next
	}
}

func (s Scope) get(c *http.Client, u *url.URL) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "berth-doctor-probe")
	return c.Do(req)
}

// client sends nothing but the request itself: no cookie jar, so a cookie one page sets
// never goes back, no proxy, since one would take the request off the loopback address,
// and no redirect of its own, so every target is judged before it is requested.
func (s Scope) client() *http.Client {
	timeout := s.timeout()
	dialer := &net.Dialer{Timeout: timeout}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				_, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				return dialer.DialContext(ctx, "tcp4", net.JoinHostPort(Loopback, port))
			},
			// The connection only reaches this machine, and the probe reads headers, not
			// the certificate, which a local authority may sign without being trusted yet.
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
			DisableKeepAlives:     true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (s Scope) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return DefaultTimeout
}

// clean drops what a browser wouldn't send, or would send as credentials.
func clean(u *url.URL) {
	u.User = nil
	u.Fragment, u.RawFragment = "", ""
}

// outside says why u is not the berth's, empty when it is.
func (s Scope) outside(u *url.URL) string {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Sprintf("%s is not an http or https URL", u)
	}
	if !names.Within(s.Host, u.Hostname()) {
		return fmt.Sprintf("its host %s is not this berth's host %s or a name under it", u.Hostname(), s.Host)
	}
	// Main's ports are checked before the berth's, so a port listed as both is main's
	// and main's app is never requested.
	if slices.Contains(s.MainPorts, Port(u)) {
		return fmt.Sprintf("port %d is main's, and the probe never requests main's app; use one of this berth's ports in the URL, written as {port.<name>}", Port(u))
	}
	if !slices.Contains(s.Ports, Port(u)) {
		return fmt.Sprintf("port %d is not one of this berth's ports, so another app may answer there; use one of them in the URL, written as {port.<name>}", Port(u))
	}
	return ""
}

// page is u as a browser would tell pages apart, so one page written two ways is
// requested once: host in lowercase without a trailing dot, the scheme's default port
// left out, and an empty path as /.
func page(u *url.URL) string {
	p := *u
	p.Host = strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if port := u.Port(); port != "" && port != strconv.Itoa(defaultPort(u.Scheme)) {
		p.Host = net.JoinHostPort(p.Host, port)
	}
	if p.Path == "" && p.RawPath == "" {
		p.Path = "/"
	}
	return p.String()
}

// main names what of main's u is on, empty when it is on neither main's host nor its
// ports. Only a name on this machine can be on main's port: the berth's own, a loopback
// name or one of main's hosts. On those an implied port counts, since a browser goes to
// 80 or 443 there, while another site's port says nothing about main.
func (s Scope) main(u *url.URL) string {
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	for _, h := range s.MainHosts {
		if strings.EqualFold(h, host) {
			return "main's host " + host
		}
	}
	local := names.Within(s.Host, host) || names.Within("localhost", host)
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		local = true
	}
	if local && slices.Contains(s.MainPorts, Port(u)) {
		return fmt.Sprintf("main's port %d", Port(u))
	}
	return ""
}

func (s Scope) noAnswer(u *url.URL, err error) string {
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return fmt.Sprintf("%s did not answer within %s", u, s.timeout())
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return fmt.Sprintf("nothing answered on port %d: berth never starts the app, so start it and run berth doctor --probe again", Port(u))
	}
	return fmt.Sprintf("%s did not answer: %v", u, err)
}

// Port is the port u is requested on, its scheme's default when it names none.
func Port(u *url.URL) int {
	if p, err := strconv.Atoi(u.Port()); err == nil {
		return p
	}
	return defaultPort(u.Scheme)
}

func defaultPort(scheme string) int {
	if scheme == "https" {
		return 443
	}
	return 80
}

func redirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}
