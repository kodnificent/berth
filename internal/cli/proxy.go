package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/authority"
	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/ports"
	"github.com/kodnificent/berth/internal/privilege"
	"github.com/kodnificent/berth/internal/proxy"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/truststore"
	"github.com/kodnificent/berth/internal/worktree"
)

const proxyUsage = "Usage: berth proxy [trust | untrust]\n\n" +
	"Serves every berth's host, and every name under it, on the loopback address: over HTTP on\n" +
	"port 80, or [proxy] http in the user config, and over HTTPS on port 443, or [proxy] https.\n" +
	"Runs in the foreground until interrupted; it starts nothing, so a berth whose app isn't\n" +
	"running gets a 502 page. HTTPS certificates come from a certificate authority berth makes\n" +
	"in its state directory, which can vouch only for names under .localhost.\n\n" +
	"Commands:\n" +
	"  trust      add the authority to the system's trust store, through the system's own prompt\n" +
	"  untrust    remove the authority berth added from the system's trust store\n"

// proxyRefresh is how often the proxy reads the registry again, so a berth added is
// served well within a second.
const proxyRefresh = 200 * time.Millisecond

// mainPortsTTL bounds how long the proxy trusts what it read of a main's published ports.
const mainPortsTTL = 5 * time.Second

// leafCache bounds the certificates the proxy keeps signed, one per name asked for.
const leafCache = 1024

func init() {
	commands = append(commands, Command{Name: "proxy", Summary: "serve every berth on port-free URLs", Usage: proxyUsage, Run: runProxy})
}

func runProxy(ctx *Context, args []string) error {
	if len(args) == 1 && args[0] == "trust" {
		return runProxyTrust(ctx)
	}
	if len(args) == 1 && args[0] == "untrust" {
		return runProxyUntrust(ctx)
	}
	if len(args) > 0 {
		return apperr.New(apperr.Usage, "berth proxy takes no arguments but trust or untrust; run berth proxy --help for usage")
	}
	userPath, err := config.UserPath()
	if err != nil {
		return err
	}
	httpPort, httpsPort, err := config.ProxyListenPorts(userPath)
	if err != nil {
		return err
	}
	state, err := registry.StateDir()
	if err != nil {
		return err
	}
	// The authority is in place before the https port accepts anything. It is made only
	// once both ports are free, so a start that fails on a port has written nothing. As
	// root it waits for the drop below, since nothing of the user's is touched as root.
	asRoot := privilege.IsRoot()
	var ca *authority.CA
	if !asRoot && ports.Free(httpPort) && ports.Free(httpsPort) {
		if ca, err = ensureAuthority(ctx, state); err != nil {
			return err
		}
	}
	listeners, err := proxy.Listen(httpPort, userPath)
	if err != nil {
		return err
	}
	secure, err := proxy.ListenFor("https", httpsPort, userPath)
	if err != nil {
		closeAll(listeners)
		return err
	}
	if asRoot {
		if err := dropRoot(state); err != nil {
			closeAll(append(listeners, secure...))
			return err
		}
	}
	if ca == nil {
		if ca, err = ensureAuthority(ctx, state); err != nil {
			closeAll(append(listeners, secure...))
			return err
		}
	}
	issuer, err := ca.Issuer(leafCache)
	if err != nil {
		closeAll(append(listeners, secure...))
		return err
	}

	// Compose warnings about main's files, read again every few seconds, would repeat for
	// as long as the proxy runs; berth doctor is where they are reported.
	logrus.SetOutput(io.Discard)
	srv := proxy.NewServer(httpPort)
	srv.HTTPSPort = httpsPort
	watch := newCookieWatch(ctx.Stderr)
	srv.Cookies = watch.hear
	go watch.record()
	routes := newProxyRoutes(ctx.Stderr, httpPort)
	srv.SetTable(routes.refresh())

	var plain, tlsAddrs []string
	for _, l := range listeners {
		plain = append(plain, "http://"+l.Addr().String())
	}
	for _, l := range secure {
		tlsAddrs = append(tlsAddrs, "https://"+l.Addr().String())
	}
	if ctx.JSON {
		if err := writeJSON(ctx.Stdout, map[string]any{"http": plain, "https": tlsAddrs, "authority": ca.CertPath}); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(ctx.Stdout, "berth proxy: serving every berth on %s, and on %s; stop it with Ctrl-C\n", strings.Join(plain, " and "), strings.Join(tlsAddrs, " and "))
		if trusted, err := truststore.FromEnv().Trusts(state, trustCert(ca)); err == nil && !trusted {
			fmt.Fprintf(ctx.Stdout, "berth proxy: browsers warn about its HTTPS until you run berth proxy trust\n")
		}
	}

	httpSrv := &http.Server{Handler: srv, ReadHeaderTimeout: 30 * time.Second}
	httpsSrv := &http.Server{
		Handler:           srv,
		ReadHeaderTimeout: 30 * time.Second,
		// HTTP/2 lets a browser load a dev server's many modules over one connection. Go's
		// server doesn't offer WebSockets over HTTP/2, so browsers open them on HTTP/1.1,
		// which ReverseProxy passes through.
		TLSConfig: &tls.Config{GetCertificate: issuer.GetCertificate, MinVersion: tls.VersionTLS12},
		// A browser that doesn't trust the authority yet, or a name outside .localhost,
		// fails its handshake; saying so for every connection would bury what matters.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	done := make(chan error, len(listeners)+len(secure))
	for _, l := range listeners {
		go func(l net.Listener) { done <- httpSrv.Serve(l) }(l)
	}
	for _, l := range secure {
		go func(l net.Listener) { done <- httpsSrv.ServeTLS(proxy.LoopbackOnly(l), "", "") }(l)
	}
	closeServers := func() error {
		return errors.Join(httpSrv.Close(), httpsSrv.Close())
	}

	stop, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	tick := time.NewTicker(proxyRefresh)
	defer tick.Stop()
	for {
		select {
		case <-stop.Done():
			return closeServers()
		case err := <-done:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			closeServers()
			return fmt.Errorf("berth proxy stopped serving: %w", err)
		case <-tick.C:
			srv.SetTable(routes.refresh())
		}
	}
}

// dropRoot gives up root for good, becoming the user who ran sudo, once the ports root
// was needed for are bound and before the registry, any config but the user config's
// ports, the authority or the cookie record is read or written.
func dropRoot(state string) error {
	if _, err := privilege.Drop(); err != nil {
		return err
	}
	return privilege.StateOwned(state, os.Getuid())
}

func closeAll(ls []net.Listener) {
	for _, l := range ls {
		l.Close()
	}
}

// ensureAuthority loads the proxy's certificate authority, making it on first use and
// saying where.
func ensureAuthority(ctx *Context, state string) (*authority.CA, error) {
	ca, created, err := authority.Ensure(state)
	if err == nil && created && !ctx.JSON {
		fmt.Fprintf(ctx.Stderr, "berth proxy: made its certificate authority, %s; it can vouch only for names under .localhost\n", ca.CertPath)
	}
	return ca, err
}

func trustCert(ca *authority.CA) truststore.Cert {
	return truststore.Cert{DER: ca.Cert.Raw}
}

func firefoxHint(certPath string) string {
	return fmt.Sprintf("Firefox keeps its own store: in Firefox, open Settings, Privacy & Security, Certificates, View Certificates, Authorities, choose Import, pick %s and tick \"Trust this CA to identify websites\"", certPath)
}

// otherStoreHints are the browsers that keep a store of their own on this system, and
// how to add the authority to each.
func otherStoreHints(certPath string) []string {
	hints := []string{firefoxHint(certPath)}
	if runtime.GOOS == "linux" {
		hints = append(hints, fmt.Sprintf("Chrome and Chromium on Linux read their own store too: certutil -d sql:$HOME/.pki/nssdb -A -t C,, -n \"berth proxy\" -i %s, with certutil from your distribution's NSS tools", shellQuoted(certPath)))
	}
	return hints
}

func shellQuoted(s string) string {
	if strings.ContainsAny(s, " '\"$\\`") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}

// refuseRootTrust stops trust and untrust run as root, whatever the store: they work on
// the user's state directory, and call sudo themselves where the system needs it.
func refuseRootTrust(cmd string) error {
	if privilege.IsRoot() {
		return apperr.New(apperr.Generic, "berth proxy %s runs as you, not as root: it works on your own state directory, and asks the system for what needs an administrator itself", cmd).
			WithNext("run berth proxy " + cmd + " without sudo")
	}
	return nil
}

func runProxyTrust(ctx *Context) error {
	store := truststore.FromEnv()
	if err := refuseRootTrust("trust"); err != nil {
		return err
	}
	state, err := registry.StateDir()
	if err != nil {
		return err
	}
	ca, err := ensureAuthority(ctx, state)
	if err != nil {
		return err
	}
	added, err := store.Trust(state, trustCert(ca))
	if err != nil {
		return apperr.New(apperr.Generic, "berth proxy trust could not add the authority %s to %s: %v", ca.CertPath, store, err)
	}
	hints := otherStoreHints(ca.CertPath)
	if ctx.JSON {
		return writeJSON(ctx.Stdout, map[string]any{"authority": ca.CertPath, "store": store.String(), "added": added, "next": hints})
	}
	if added {
		fmt.Fprintf(ctx.Stdout, "berth proxy: added its certificate authority, %s, to %s; browsers that read it now trust the proxy's HTTPS for names under .localhost\n", ca.CertPath, store)
	} else {
		fmt.Fprintf(ctx.Stdout, "berth proxy: its certificate authority, %s, is already in %s\n", ca.CertPath, store)
	}
	for _, h := range hints {
		fmt.Fprintln(ctx.Stdout, h)
	}
	return nil
}

func runProxyUntrust(ctx *Context) error {
	store := truststore.FromEnv()
	if err := refuseRootTrust("untrust"); err != nil {
		return err
	}
	state, err := registry.StateDir()
	if err != nil {
		return err
	}
	removed, gone, err := store.Untrust(state)
	if err != nil {
		return apperr.New(apperr.Generic, "berth proxy untrust could not remove the authority from %s: %v", store, err)
	}
	if ctx.JSON {
		return writeJSON(ctx.Stdout, map[string]any{"store": store.String(), "removed": removed, "not_found": gone})
	}
	switch {
	case removed == 0 && gone == 0:
		fmt.Fprintf(ctx.Stdout, "berth proxy: berth has not added its certificate authority to %s, so there is nothing to remove\n", store)
		return nil
	case removed == 0:
		fmt.Fprintf(ctx.Stdout, "berth proxy: %s no longer holds the certificate authority berth added, so it was removed elsewhere; berth has forgotten it\n", store)
	default:
		fmt.Fprintf(ctx.Stdout, "berth proxy: removed its certificate authority from %s\n", store)
	}
	fmt.Fprintln(ctx.Stdout, "If you added it to Firefox, or another browser's own store, remove it there too")
	return nil
}

// proxyRoutes builds the proxy's table from the registry, and says on log what changes
// from one table to the next.
type proxyRoutes struct {
	log  io.Writer
	port int
	last map[string]string
	// said holds the last error said on log for each source, so one that persists is said
	// once rather than on every refresh.
	said map[string]string
	// byID is the last table's berths by id, kept for a berth whose record can't be read
	// for a moment.
	byID  map[string]proxy.Berth
	table proxy.Table
	// published holds, by repository, the last good read of the ports its main's compose
	// project publishes, and the error of the latest read when it failed.
	published map[string]publishedRead
}

type publishedRead struct {
	ports []int
	good  bool
	err   error
	at    time.Time
}

func newProxyRoutes(log io.Writer, port int) *proxyRoutes {
	return &proxyRoutes{log: log, port: port, last: map[string]string{}, said: map[string]string{}, byID: map[string]proxy.Berth{}, published: map[string]publishedRead{}}
}

// say writes what source reports on log when it differs from what source said last.
func (p *proxyRoutes) say(source string, err error, format string, args ...any) {
	msg := ""
	if err != nil {
		msg = fmt.Sprintf(format, args...) + ": " + err.Error()
	}
	if msg != p.said[source] && msg != "" {
		fmt.Fprintf(p.log, "berth proxy: %s\n", msg)
	}
	p.said[source] = msg
}

// refresh reads the registry again. When it can't be read the last table is kept: an
// empty one would answer every berth with the 404.
func (p *proxyRoutes) refresh() proxy.Table {
	live, unreadable, err := registry.Peek()
	p.say("registry", err, "reading the registry")
	if err != nil {
		return p.table
	}
	published, unread := p.publishedByEveryMain(live)
	var table proxy.Table
	byID := map[string]proxy.Berth{}
	now := map[string]string{}
	add := func(id string, b proxy.Berth, why string) {
		table = append(table, b)
		byID[id] = b
		now[b.Name+" at "+proxy.URL(b.Host, p.port)] = why
	}
	for _, l := range live {
		if l.Host == "" {
			continue
		}
		var b proxy.Berth
		switch {
		case l.Orphaned:
			add(l.ID, proxy.Berth{Name: l.Name, Host: l.Host, Gone: true}, "its worktree is gone")
			continue
		case unread[l.Repository] != nil:
			b = proxy.Berth{Name: l.Name, Host: l.Host, Port: proxy.Target{Refused: "main's compose project could not be read, so the proxy can't tell which ports main publishes: " + unread[l.Repository].Error()}}
		default:
			w := &worktree.Worktree{Path: l.Worktree, AdminDir: l.AdminDir, CommonDir: l.CommonDir, Repository: l.Repository}
			if cfg, err := config.Load(w); err != nil {
				b = proxy.Berth{Name: l.Name, Host: l.Host, Port: proxy.Target{Refused: "its config is invalid: " + err.Error()}}
			} else {
				b = proxyBerth(l.Record, cfg, mainPortsFor(published, l.Repository, cfg))
			}
		}
		add(l.ID, b, b.Port.Refused)
	}
	for id, err := range unreadable {
		p.say("record "+id, err, "reading berth record %s", id)
		// The berth still exists, so its host stays taken, as it was.
		if prev, ok := p.byID[id]; ok {
			add(id, prev, p.last[prev.Name+" at "+proxy.URL(prev.Host, p.port)])
		}
	}
	for source := range p.said {
		if id, ok := strings.CutPrefix(source, "record "); ok && unreadable[id] == nil {
			delete(p.said, source)
		}
	}
	for key, refused := range now {
		if was, seen := p.last[key]; !seen || was != refused {
			if refused == "" {
				fmt.Fprintf(p.log, "berth proxy: serving %s\n", key)
			} else {
				fmt.Fprintf(p.log, "berth proxy: not serving %s: %s\n", key, refused)
			}
		}
	}
	for key := range p.last {
		if _, kept := now[key]; !kept {
			fmt.Fprintf(p.log, "berth proxy: dropped %s\n", key)
		}
	}
	p.last, p.byID, p.table = now, byID, table
	return table
}

// publishedByEveryMain is every port the main of any repository with a berth publishes,
// as add reserves them, and the repositories whose main has never been read. Each main is
// read again once older than mainPortsTTL, since loading a compose project on every
// refresh would keep the proxy busy; a read that fails keeps the last good one.
func (p *proxyRoutes) publishedByEveryMain(live []registry.Live) (map[int]bool, map[string]error) {
	set := map[int]bool{}
	unread := map[string]error{}
	for _, l := range live {
		read, ok := p.published[l.Repository]
		if !ok || time.Since(read.at) >= mainPortsTTL {
			ports, err := mainPublished(l.Repository)
			read.at, read.err = time.Now(), err
			if err == nil {
				read.ports, read.good = ports, true
			}
			p.published[l.Repository] = read
			p.say("compose "+l.Repository, err, "reading main's compose project in %s", l.Repository)
		}
		if !read.good {
			unread[l.Repository] = read.err
		}
		for _, port := range read.ports {
			set[port] = true
		}
	}
	return set, unread
}

// mainPortsFor is every port a berth of repo must not be sent to: those every main
// publishes, and those its own main's .env sets in the keys the berth's own config names.
func mainPortsFor(published map[int]bool, repo string, cfg *config.Config) map[int]bool {
	set := map[int]bool{}
	for port := range published {
		set[port] = true
	}
	_, envPorts := mainEnvAddresses(repo, cfg)
	for _, port := range envPorts {
		set[port] = true
	}
	return set
}

// proxyBerth is where the proxy sends berth rec's names: only to ports berth assigned it,
// and never to one main uses, whatever the config names.
func proxyBerth(rec registry.Record, cfg *config.Config, mainPorts map[int]bool) proxy.Berth {
	assigned := map[string]int{}
	for _, p := range rec.Ports {
		if p.Policy == ports.Assign && p.HostPort > 0 {
			assigned[p.Name] = p.HostPort
		}
	}
	values := cfg.PortValues(assigned)
	target := func(name string) proxy.Target {
		port, ok := values[name]
		switch {
		case name == "":
			return proxy.Target{Refused: "its config sets no [proxy] port; set it to the name of the port the app serves on, such as \"web\""}
		case !ok:
			return proxy.Target{Refused: fmt.Sprintf("berth assigned it no port named %s; run berth add in its worktree", name)}
		case mainPorts[port]:
			return proxy.Target{Refused: fmt.Sprintf("its port %s, %d, is one main uses, and the proxy never sends a berth's requests to main", name, port)}
		}
		return proxy.Target{Port: port}
	}
	b := proxy.Berth{ID: rec.ID, Name: rec.Name, Host: rec.Host, RewriteCookies: cfg.RewritesCookies(), Port: target(cfg.Proxy.Port), Routes: map[string]proxy.Target{}}
	for name, port := range cfg.ProxyRoutes(rec.Name, rec.Host) {
		b.Routes[name] = target(port)
	}
	return b
}
