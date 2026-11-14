package proxy

import (
	"context"
	"errors"
	"html/template"
	"net"
	"net/http"
	"net/http/httputil"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kodnificent/berth/internal/cookie"
)

// Server answers for every berth in its table, which can be replaced while it serves.
type Server struct {
	// HTTPPort and HTTPSPort are the ports the proxy serves on, for the URLs its pages
	// list.
	HTTPPort  int
	HTTPSPort int
	// Cookies, when set, hears of every cookie a berth's app sets through the proxy. It
	// runs before the response is sent, so it must not wait on anything.
	Cookies   func(Berth, SetCookie)
	table     atomic.Pointer[Table]
	transport *http.Transport
}

// SetCookie is one Set-Cookie a berth's app sent, as the proxy judged it. It carries the
// cookie's name and domain, never its value.
type SetCookie struct {
	Name   string
	Domain string
	// URL is the page that set it, without its query, which may hold a secret.
	URL string
	// Wide is a domain wider than the berth's host, so the cookie reaches other berths.
	Wide bool
	// Rewritten is a wide domain the proxy replaced with the berth's host.
	Rewritten bool
}

func NewServer(httpPort int) *Server {
	s := &Server{HTTPPort: httpPort, transport: &http.Transport{
		DialContext:         dialLoopback,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     30 * time.Second,
		// The app sees the Accept-Encoding the browser sent, not one the transport adds.
		DisableCompression: true,
	}}
	s.table.Store(&Table{})
	return s
}

func (s *Server) SetTable(t Table) { s.table.Store(&t) }

// dialLoopback dials the port of addr on 127.0.0.1, then on ::1 when nothing answers
// there, since dev servers that bind "localhost" may take only the IPv6 loopback.
func dialLoopback(ctx context.Context, _, addr string) (net.Conn, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	c, err := d.DialContext(ctx, "tcp4", net.JoinHostPort("127.0.0.1", port))
	if err == nil {
		return c, nil
	}
	if c6, err6 := d.DialContext(ctx, "tcp6", net.JoinHostPort("::1", port)); err6 == nil {
		return c6, nil
	}
	return nil, err
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !fromLoopback(r) {
		refuse(w)
		return
	}
	table := *s.table.Load()
	b, target, ok := table.Lookup(r.Host)
	switch {
	case !ok, b.Gone:
		s.notFound(w, r, table)
	case target.Refused != "":
		page(w, http.StatusBadGateway, refusedPage, map[string]any{"Berth": b.Name, "Host": Hostname(r.Host), "Why": target.Refused})
	default:
		s.forward(w, r, b, target.Port)
	}
}

// fromLoopback backs the loopback-only bind: a system routing other peers to 127.0.0.1,
// as Linux does with route_localnet, still gets nothing from the proxy.
func fromLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// refuse closes the connection without an answer, falling back to a bare 403 where the
// connection can't be taken over.
func refuse(w http.ResponseWriter) {
	if hj, ok := w.(http.Hijacker); ok {
		if conn, _, err := hj.Hijack(); err == nil {
			conn.Close()
			return
		}
	}
	w.WriteHeader(http.StatusForbidden)
}

func (s *Server) forward(w http.ResponseWriter, r *http.Request, b Berth, port int) {
	rp := &httputil.ReverseProxy{
		Transport: s.transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
			pr.Out.Host = pr.In.Host
			dropForwarded(pr.Out.Header)
			pr.SetXForwarded()
		},
		ModifyResponse: func(resp *http.Response) error {
			s.judgeCookies(resp, r, b)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				return
			}
			page(w, http.StatusBadGateway, badGatewayPage, map[string]any{"Berth": b.Name, "Host": Hostname(r.Host), "Port": port, "Err": err.Error()})
		},
	}
	rp.ServeHTTP(w, r)
}

// judgeCookies reports each cookie the app set and, for a berth that asks for it,
// replaces a domain wider than the berth's host with the host. A cookie of a name under
// the host, a routed name's included, is the berth's as much as one of the host itself.
func (s *Server) judgeCookies(resp *http.Response, r *http.Request, b Berth) {
	raws := resp.Header.Values("Set-Cookie")
	if len(raws) == 0 {
		return
	}
	out := make([]string, len(raws))
	for i, raw := range raws {
		out[i] = raw
		name, domain := cookie.NameAndDomain(raw)
		seen := SetCookie{Name: name, Domain: domain, URL: pageURL(r), Wide: cookie.Wider(b.Host, &http.Cookie{Domain: domain})}
		if seen.Wide && b.RewriteCookies {
			out[i], seen.Rewritten = cookie.RewriteDomain(raw, b.Host)
		}
		if s.Cookies != nil {
			s.Cookies(b, seen)
		}
	}
	resp.Header["Set-Cookie"] = out
}

func pageURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + strings.ToLower(r.Host) + r.URL.EscapedPath()
}

// dropForwarded removes every header a client could use to claim another origin, so the
// app sees only what the proxy saw.
func dropForwarded(h http.Header) {
	for k := range h {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), "X-Forwarded-") {
			h.Del(k)
		}
	}
	for _, k := range []string{"Forwarded", "X-Real-Ip", "X-Original-Host"} {
		h.Del(k)
	}
}

type listed struct {
	Name string
	URL  string
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request, table Table) {
	var berths []listed
	for _, b := range table {
		if b.Gone {
			continue
		}
		u := URL(b.Host, s.HTTPPort)
		if r.TLS != nil {
			u = HTTPSURL(b.Host, s.HTTPSPort)
		}
		berths = append(berths, listed{b.Name, u})
	}
	sort.Slice(berths, func(i, j int) bool { return berths[i].URL < berths[j].URL })
	page(w, http.StatusNotFound, notFoundPage, map[string]any{"Host": Hostname(r.Host), "Berths": berths})
}

// URL is where a browser reaches host through a proxy serving HTTP on port.
func URL(host string, port int) string {
	if port == 80 {
		return "http://" + host
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port))
}

// HTTPSURL is where a browser reaches host through a proxy serving HTTPS on port.
func HTTPSURL(host string, port int) string {
	if port == 443 {
		return "https://" + host
	}
	return "https://" + net.JoinHostPort(host, strconv.Itoa(port))
}

func page(w http.ResponseWriter, status int, t *template.Template, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = t.Execute(w, data)
}

const pageHead = `<!doctype html><html><head><meta charset="utf-8"><title>{{block "title" .}}{{end}}</title><style>body{font:16px/1.5 system-ui,sans-serif;max-width:40rem;margin:3rem auto;padding:0 1rem}code{font-size:.95em}</style></head><body>`

var notFoundPage = template.Must(template.New("404").Parse(pageHead + `{{define "title"}}No berth here{{end}}
<h1>No berth has this host</h1>
<p>berth proxy has no berth for <code>{{.Host}}</code>.</p>
{{if .Berths}}<p>The berths it serves:</p>
<ul>{{range .Berths}}<li><a href="{{.URL}}">{{.URL}}</a> ({{.Name}})</li>
{{end}}</ul>{{else}}<p>It serves no berths yet. Run <code>berth add</code> in a worktree to make one.</p>{{end}}
</body></html>
`))

var badGatewayPage = template.Must(template.New("502").Parse(pageHead + `{{define "title"}}{{.Berth}} is not answering{{end}}
<h1>Berth {{.Berth}} is not answering</h1>
<p>Nothing answered on port {{.Port}}, berth {{.Berth}}'s port for <code>{{.Host}}</code>. Start the app in the berth: berth proxy starts nothing.</p>
<p><code>{{.Err}}</code></p>
</body></html>
`))

var refusedPage = template.Must(template.New("refused").Parse(pageHead + `{{define "title"}}{{.Berth}} has no port for this host{{end}}
<h1>Berth {{.Berth}} has no port for this host</h1>
<p>berth proxy sends <code>{{.Host}}</code> nowhere: {{.Why}}.</p>
</body></html>
`))
