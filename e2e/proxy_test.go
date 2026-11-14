//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type runningProxy struct {
	port   int
	out    *syncBuffer
	exited chan struct{}
}

func loopbackFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// proxyUserConfig moves the proxy off ports 80 and 443, which a test may not take.
func proxyUserConfig(t *testing.T, repo *Repo, extra string) int {
	t.Helper()
	httpPort, httpsPort := loopbackFreePort(t), loopbackFreePort(t)
	writeFile(t, repo.userConfig(), fmt.Sprintf("[proxy]\nhttp = %d\nhttps = %d\n\n%s", httpPort, httpsPort, extra))
	return httpPort
}

func proxyRepoConfig(t *testing.T, repo *Repo, extra string) {
	t.Helper()
	writeFile(t, repo.privateConfig(), "[berth]\nhost = \"{name}.myapp.localhost\"\n\n[proxy]\nport = \"web\"\n\n"+extra+"\n"+onlyWeb(t, repo))
}

func addProxiedBerth(t *testing.T, repo *Repo, branch string) (string, portsStatus) {
	t.Helper()
	wt := repo.AddWorktree(t, branch)
	mustBerth(t, repo, wt, "add")
	s := portsStatusOf(t, repo, wt)
	if want := branch + ".myapp.localhost"; s.Host != want {
		t.Fatalf("berth %s has host %q, want %q", branch, s.Host, want)
	}
	return wt, s
}

func startProxy(t *testing.T, repo *Repo, port int) *runningProxy {
	t.Helper()
	p := &runningProxy{port: port, out: &syncBuffer{}, exited: make(chan struct{})}
	cmd := exec.Command(berthBin, "proxy")
	cmd.Dir = repo.Dir
	cmd.Env = repo.Env()
	cmd.Stdout, cmd.Stderr = p.out, p.out
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting berth proxy: %v", err)
	}
	go func() {
		_ = cmd.Wait()
		close(p.exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-p.exited
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		select {
		case <-p.exited:
			t.Fatalf("berth proxy exited before accepting on 127.0.0.1:%d:\n%s", port, p.out.String())
		default:
		}
		if c, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond); err == nil {
			c.Close()
			return p
		}
		if time.Now().After(deadline) {
			t.Fatalf("berth proxy does not accept on 127.0.0.1:%d after 15s:\n%s", port, p.out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (p *runningProxy) running() bool {
	select {
	case <-p.exited:
		return false
	default:
		return true
	}
}

// get asks the proxy for host at the loopback address, so no name needs to resolve.
func (p *runningProxy) get(host, path string, timeout time.Duration) (int, string, error) {
	client := &http.Client{
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(p.port)+path, nil)
	if err != nil {
		return 0, "", err
	}
	req.Host = host
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), err
}

func (p *runningProxy) mustGet(t *testing.T, host, path string) (int, string) {
	t.Helper()
	code, body, err := p.get(host, path, 5*time.Second)
	if err != nil {
		t.Fatalf("GET %s%s through the proxy: %v\nproxy output:\n%s", host, path, err, p.out.String())
	}
	return code, body
}

type echoed struct {
	Backend        string `json:"backend"`
	Host           string `json:"host"`
	Path           string `json:"path"`
	ForwardedProto string `json:"x_forwarded_proto"`
	ForwardedHost  string `json:"x_forwarded_host"`
	ForwardedFor   string `json:"x_forwarded_for"`
}

func (p *runningProxy) mustEcho(t *testing.T, host, path string) echoed {
	t.Helper()
	code, body := p.mustGet(t, host, path)
	var e echoed
	if code != http.StatusOK || json.Unmarshal([]byte(body), &e) != nil || e.Backend == "" {
		t.Fatalf("GET %s%s through the proxy: status %d, want the app's answer:\n%s", host, path, code, body)
	}
	return e
}

// serveEcho stands in for the user's app on one of the berth's ports, answering with what
// reached it so the test sees the request as the app does.
func serveEcho(t *testing.T, port int, backend string) {
	t.Helper()
	l, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("listening on 127.0.0.1:%d: %v", port, err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			serveWebSocket(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(echoed{
			Backend:        backend,
			Host:           r.Host,
			Path:           r.URL.Path,
			ForwardedProto: r.Header.Get("X-Forwarded-Proto"),
			ForwardedHost:  r.Header.Get("X-Forwarded-Host"),
			ForwardedFor:   r.Header.Get("X-Forwarded-For"),
		})
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
}

func webSocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func serveWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, rw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", webSocketAccept(r.Header.Get("Sec-WebSocket-Key")))
	writeTextFrame(rw, false, "hello from "+r.Host)
	rw.Flush()
	msg, err := readTextFrame(rw.Reader)
	if err != nil {
		return
	}
	writeTextFrame(rw, false, "echo: "+msg)
	rw.Flush()
}

// writeTextFrame writes one short text frame; a client must mask what it sends.
func writeTextFrame(w io.Writer, masked bool, payload string) error {
	frame := []byte{0x81}
	if !masked {
		frame = append(frame, byte(len(payload)))
		frame = append(frame, payload...)
	} else {
		key := []byte{0x1b, 0x2c, 0x3d, 0x4e}
		frame = append(frame, 0x80|byte(len(payload)))
		frame = append(frame, key...)
		for i := 0; i < len(payload); i++ {
			frame = append(frame, payload[i]^key[i%4])
		}
	}
	_, err := w.Write(frame)
	return err
}

func readTextFrame(r *bufio.Reader) (string, error) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(r, head); err != nil {
		return "", err
	}
	if head[0]&0x0f != 0x1 {
		return "", fmt.Errorf("frame opcode %#x, want a text frame", head[0]&0x0f)
	}
	n := int(head[1] & 0x7f)
	if n >= 126 {
		return "", fmt.Errorf("frame of length code %d, want a short frame", n)
	}
	var key []byte
	if head[1]&0x80 != 0 {
		key = make([]byte, 4)
		if _, err := io.ReadFull(r, key); err != nil {
			return "", err
		}
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return "", err
	}
	for i := range payload {
		if key != nil {
			payload[i] ^= key[i%4]
		}
	}
	return string(payload), nil
}

func TestProxyRoutesTheBerthsHostAndNamesUnderItToItsWebPort(t *testing.T) {
	repo := NewRepo(t)
	port := proxyUserConfig(t, repo, "")
	proxyRepoConfig(t, repo, "")
	_, s := addProxiedBerth(t, repo, "feat-x")
	serveEcho(t, s.port(t, "web", 80).HostPort, "web")
	p := startProxy(t, repo, port)

	for _, host := range []string{"feat-x.myapp.localhost", "app.feat-x.myapp.localhost"} {
		e := p.mustEcho(t, host, "/dashboard")
		if e.Backend != "web" || e.Host != host || e.Path != "/dashboard" {
			t.Errorf("GET http://%s/dashboard reached %s with Host %q and path %q, want web with Host %q and path /dashboard", host, e.Backend, e.Host, e.Path, host)
		}
	}
}

func TestProxySetsTheForwardedHeaders(t *testing.T) {
	repo := NewRepo(t)
	port := proxyUserConfig(t, repo, "")
	proxyRepoConfig(t, repo, "")
	_, s := addProxiedBerth(t, repo, "feat-x")
	serveEcho(t, s.port(t, "web", 80).HostPort, "web")
	p := startProxy(t, repo, port)

	e := p.mustEcho(t, "app.feat-x.myapp.localhost", "/")
	if e.ForwardedProto != "http" {
		t.Errorf("X-Forwarded-Proto = %q, want http", e.ForwardedProto)
	}
	if e.ForwardedHost != "app.feat-x.myapp.localhost" {
		t.Errorf("X-Forwarded-Host = %q, want app.feat-x.myapp.localhost", e.ForwardedHost)
	}
	if first, _, _ := strings.Cut(e.ForwardedFor, ","); strings.TrimSpace(first) != "127.0.0.1" {
		t.Errorf("X-Forwarded-For = %q, want the client's address 127.0.0.1", e.ForwardedFor)
	}
}

func TestProxySendsARoutedNameToAnotherOfTheBerthsPorts(t *testing.T) {
	repo := NewRepo(t)
	port := proxyUserConfig(t, repo, "")
	proxyRepoConfig(t, repo, "[proxy.routes]\n\"vite.{host}\" = \"vite\"\n\n[ports]\nvite = { env = \"VITE_PORT\" }\n")
	_, s := addProxiedBerth(t, repo, "feat-x")
	serveEcho(t, s.port(t, "web", 80).HostPort, "web")
	serveEcho(t, s.named(t, "vite").HostPort, "vite")
	p := startProxy(t, repo, port)

	if e := p.mustEcho(t, "vite.feat-x.myapp.localhost", "/@vite/client"); e.Backend != "vite" || e.Host != "vite.feat-x.myapp.localhost" {
		t.Errorf("vite.feat-x.myapp.localhost reached %s with Host %q, want the vite port with its own Host", e.Backend, e.Host)
	}
	for _, host := range []string{"feat-x.myapp.localhost", "app.feat-x.myapp.localhost"} {
		if e := p.mustEcho(t, host, "/"); e.Backend != "web" {
			t.Errorf("%s reached %s, want the web port named by [proxy] port", host, e.Backend)
		}
	}
}

// A single-port pool fixes the berth's port before it exists, so the app can answer the
// moment add returns and the second is the proxy's alone.
func TestProxyServesABerthAddedWhileItRuns(t *testing.T) {
	repo := NewRepo(t)
	pool := freePortBlock(t, 1)
	port := proxyUserConfig(t, repo, poolConfig(pool))
	proxyRepoConfig(t, repo, "")
	p := startProxy(t, repo, port)

	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add")
	added := time.Now()
	serveEcho(t, pool[0], "web")

	var code int
	var body string
	var err error
	for time.Since(added) < time.Second {
		code, body, err = p.get("feat-x.myapp.localhost", "/", 500*time.Millisecond)
		if err == nil && code == http.StatusOK && strings.Contains(body, `"backend":"web"`) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("feat-x.myapp.localhost is not served within a second of berth add: status %d, err %v\n%s\nproxy output:\n%s", code, err, body, p.out.String())
}

func TestProxyAnswersARemovedBerthWithTheNotFoundPage(t *testing.T) {
	repo := NewRepo(t)
	port := proxyUserConfig(t, repo, "")
	proxyRepoConfig(t, repo, "")
	wt, s := addProxiedBerth(t, repo, "feat-x")
	serveEcho(t, s.port(t, "web", 80).HostPort, "web")
	p := startProxy(t, repo, port)
	p.mustEcho(t, "feat-x.myapp.localhost", "/")

	mustBerth(t, repo, wt, "remove")
	removed := time.Now()

	var code int
	var body string
	for time.Since(removed) < 5*time.Second {
		code, body, _ = p.get("feat-x.myapp.localhost", "/", 500*time.Millisecond)
		if code == http.StatusNotFound {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code != http.StatusNotFound {
		t.Fatalf("feat-x.myapp.localhost after berth remove: status %d, want 404 while the proxy keeps running\n%s", code, body)
	}
	if strings.Contains(body, "http://feat-x.myapp.localhost") {
		t.Errorf("the 404 page still lists the removed berth's URL:\n%s", body)
	}
}

func TestProxyNotFoundPageListsTheBerthsURLs(t *testing.T) {
	repo := NewRepo(t)
	port := proxyUserConfig(t, repo, "")
	proxyRepoConfig(t, repo, "")
	_, featX := addProxiedBerth(t, repo, "feat-x")
	_, shop := addProxiedBerth(t, repo, "shop")
	serveEcho(t, featX.port(t, "web", 80).HostPort, "web")
	serveEcho(t, shop.port(t, "web", 80).HostPort, "web")
	p := startProxy(t, repo, port)

	for _, host := range []string{"nope.myapp.localhost", "myfeat-x.myapp.localhost"} {
		code, body := p.mustGet(t, host, "/")
		if code != http.StatusNotFound {
			t.Errorf("%s, which no berth has, got status %d, want 404:\n%s", host, code, body)
			continue
		}
		for _, url := range []string{"http://feat-x.myapp.localhost", "http://shop.myapp.localhost"} {
			if !strings.Contains(body, url) {
				t.Errorf("the 404 page for %s does not list %s:\n%s", host, url, body)
			}
		}
	}
}

func TestProxyCarriesAWebSocketBothWays(t *testing.T) {
	repo := NewRepo(t)
	port := proxyUserConfig(t, repo, "")
	proxyRepoConfig(t, repo, "")
	_, s := addProxiedBerth(t, repo, "feat-x")
	serveEcho(t, s.port(t, "web", 80).HostPort, "web")
	p := startProxy(t, repo, port)

	conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(p.port)), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	key := base64.StdEncoding.EncodeToString([]byte(randomHex(t, 8)))
	fmt.Fprintf(conn, "GET /socket HTTP/1.1\r\nHost: app.feat-x.myapp.localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", key)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("reading the upgrade response: %v\nproxy output:\n%s", err, p.out.String())
	}
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Sec-WebSocket-Accept") != webSocketAccept(key) {
		t.Fatalf("upgrade through the proxy: status %d, Sec-WebSocket-Accept %q, want 101 with the app's accept key", resp.StatusCode, resp.Header.Get("Sec-WebSocket-Accept"))
	}

	if msg, err := readTextFrame(br); err != nil || msg != "hello from app.feat-x.myapp.localhost" {
		t.Fatalf("message from the app through the proxy: %q, %v", msg, err)
	}
	if err := writeTextFrame(conn, true, "ping"); err != nil {
		t.Fatal(err)
	}
	if msg, err := readTextFrame(br); err != nil || msg != "echo: ping" {
		t.Errorf("the app's answer to a message sent through the proxy: %q, %v, want \"echo: ping\"", msg, err)
	}
}

func nonLoopbackAddress(t *testing.T) net.IP {
	t.Helper()
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	var v6 net.IP
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() || !ipnet.IP.IsGlobalUnicast() {
				continue
			}
			if ip4 := ipnet.IP.To4(); ip4 != nil {
				return ip4
			}
			if v6 == nil {
				v6 = ipnet.IP
			}
		}
	}
	if v6 == nil {
		t.Skip("this machine has no non-loopback address to connect to the proxy from")
	}
	return v6
}

func TestProxyAnswersABerthWithNothingListeningWithABadGatewayNamingIt(t *testing.T) {
	repo := NewRepo(t)
	port := proxyUserConfig(t, repo, "")
	proxyRepoConfig(t, repo, "")
	_, featX := addProxiedBerth(t, repo, "feat-x")
	_, shop := addProxiedBerth(t, repo, "shop")
	serveEcho(t, shop.port(t, "web", 80).HostPort, "web")
	p := startProxy(t, repo, port)
	silent := featX.port(t, "web", 80).HostPort

	code, body := p.mustGet(t, "feat-x.myapp.localhost", "/")
	if code != http.StatusBadGateway {
		t.Errorf("feat-x.myapp.localhost with nothing on port %d: status %d, want 502:\n%s", silent, code, body)
	}
	if !strings.Contains(body, "feat-x") || !regexp.MustCompile(`\b`+strconv.Itoa(silent)+`\b`).MatchString(body) {
		t.Errorf("the 502 page does not name the berth feat-x and its port %d:\n%s", silent, body)
	}
	if !portFree(silent) {
		t.Errorf("something listens on feat-x's port %d after the proxy answered for it; the proxy starts nothing", silent)
	}

	if e := p.mustEcho(t, "shop.myapp.localhost", "/"); e.Host != "shop.myapp.localhost" {
		t.Errorf("shop.myapp.localhost reached the app with Host %q after feat-x's 502", e.Host)
	}
	if !p.running() {
		t.Errorf("the proxy exited after a 502:\n%s", p.out.String())
	}
}

func stateContents(t *testing.T, repo *Repo) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(repo.StateDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.IsDir() && d.Name() != "lock" {
			rel, _ := filepath.Rel(repo.StateDir, path)
			out[rel] = readString(t, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func expectSameState(t *testing.T, before, after map[string]string) {
	t.Helper()
	for name, data := range before {
		if got, ok := after[name]; !ok || got != data {
			t.Errorf("the failed proxy changed %s in berth's state directory", name)
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			t.Errorf("the failed proxy left %s in berth's state directory", name)
		}
	}
}

// runFailingProxy runs berth proxy expecting it to give up, and fails the test if it is
// still serving after the timeout rather than wait on it.
func runFailingProxy(t *testing.T, repo *Repo) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, berthBin, "proxy")
	cmd.Dir = repo.Dir
	cmd.Env = repo.Env()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if ctx.Err() != nil {
		t.Fatalf("berth proxy kept running for 30s although it cannot listen:\n%s%s", res.Stdout, res.Stderr)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.Exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("berth proxy: %v", err)
	}
	return res
}

func TestProxyExitsWhenItsHTTPPortIsTaken(t *testing.T) {
	repo := NewRepo(t)
	port := proxyUserConfig(t, repo, "")
	proxyRepoConfig(t, repo, "")
	addProxiedBerth(t, repo, "feat-x")
	held, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	state := stateContents(t, repo)
	userConfig := readString(t, repo.userConfig())

	res := runFailingProxy(t, repo)
	out := res.Stdout + res.Stderr
	if res.Exit != 1 {
		t.Errorf("berth proxy with port %d taken exited %d, want 1:\n%s", port, res.Exit, out)
	}
	if !regexp.MustCompile(`\b` + strconv.Itoa(port) + `\b`).MatchString(out) {
		t.Errorf("berth proxy does not name the taken port %d:\n%s", port, out)
	}
	expectSameState(t, state, stateContents(t, repo))
	if got := readString(t, repo.userConfig()); got != userConfig {
		t.Errorf("berth proxy changed the user config:\n%s", got)
	}
	c, err := net.DialTimeout("tcp4", held.Addr().String(), 2*time.Second)
	if err != nil {
		t.Errorf("the test's own listener on port %d no longer accepts after berth proxy: %v", port, err)
	} else {
		c.Close()
	}
}

// Linux keeps ports below ip_unprivileged_port_start for root, so only there can the test
// see port 80 reserved without binding it itself.
func TestProxyExitsNamingHowToAllowAReservedPort(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() == 0 {
		t.Skip("port 80 is reserved for administrators only on Linux, for a user other than root")
	}
	const sysctl = "/proc/sys/net/ipv4/ip_unprivileged_port_start"
	before, err := os.ReadFile(sysctl)
	if err != nil {
		t.Skipf("cannot read %s: %v", sysctl, err)
	}
	if start, err := strconv.Atoi(strings.TrimSpace(string(before))); err != nil || start <= 80 {
		t.Skipf("port 80 is not reserved here: %s is %s", sysctl, strings.TrimSpace(string(before)))
	}
	repo := NewRepo(t)
	writeFile(t, repo.userConfig(), fmt.Sprintf("[proxy]\nhttp = 80\nhttps = %d\n", loopbackFreePort(t)))
	proxyRepoConfig(t, repo, "")
	addProxiedBerth(t, repo, "feat-x")
	state := stateContents(t, repo)

	res := runFailingProxy(t, repo)
	out := res.Stdout + res.Stderr
	if res.Exit != 1 {
		t.Errorf("berth proxy on reserved port 80 exited %d, want 1:\n%s", res.Exit, out)
	}
	if !regexp.MustCompile(`\b80\b`).MatchString(out) {
		t.Errorf("berth proxy does not name port 80:\n%s", out)
	}
	if !regexp.MustCompile(`ip_unprivileged_port_start|setcap|cap_net_bind_service`).MatchString(out) {
		t.Errorf("berth proxy does not print the command that allows port 80 on Linux:\n%s", out)
	}
	if after, _ := os.ReadFile(sysctl); !bytes.Equal(after, before) {
		t.Errorf("berth proxy changed %s from %s to %s", sysctl, before, after)
	}
	expectSameState(t, state, stateContents(t, repo))
}

func TestProxyAnswersABerthWhoseConfigSetsNoProxyPortWithABadGatewaySayingSo(t *testing.T) {
	repo := NewRepo(t)
	port := proxyUserConfig(t, repo, "")
	writeFile(t, repo.privateConfig(), "[berth]\nhost = \"{name}.myapp.localhost\"\n\n"+onlyWeb(t, repo))
	_, s := addProxiedBerth(t, repo, "feat-x")
	serveEcho(t, s.port(t, "web", 80).HostPort, "web")
	p := startProxy(t, repo, port)

	for _, host := range []string{"feat-x.myapp.localhost", "app.feat-x.myapp.localhost"} {
		code, body := p.mustGet(t, host, "/")
		if code != http.StatusBadGateway || !strings.Contains(body, "feat-x") || !strings.Contains(body, "[proxy] port") {
			t.Errorf("%s with no [proxy] port: status %d, want a 502 naming feat-x and [proxy] port:\n%s", host, code, body)
		}
	}
}

func TestProxyCannotBeConnectedToOnANonLoopbackAddress(t *testing.T) {
	repo := NewRepo(t)
	ip := nonLoopbackAddress(t)
	port := proxyUserConfig(t, repo, "")
	proxyRepoConfig(t, repo, "")
	startProxy(t, repo, port)

	addr := net.JoinHostPort(ip.String(), strconv.Itoa(port))
	if conn, err := net.DialTimeout("tcp", addr, 3*time.Second); err == nil {
		conn.Close()
		t.Errorf("a TCP connection to the proxy's port on %s succeeded; the proxy must listen on loopback only", addr)
	}
}

func TestProxyListenPortsInAProjectConfigAreInvalid(t *testing.T) {
	repo := NewRepo(t)
	proxyRepoConfig(t, repo, "")
	wt := repo.AddWorktree(t, "feat-x")
	writeFile(t, repoConfig(wt), "[proxy]\nhttp = 8080\n")

	res := repo.Berth(t, wt, "add")

	named := filepath.Join(filepath.Base(wt), "berth.toml") + ": proxy.http"
	if out := res.Stdout + res.Stderr; res.Exit != 5 || !strings.Contains(out, named) {
		t.Errorf("berth add with [proxy] http in %s exited %d, want 5 naming the file and proxy.http:\n%s", repoConfig(wt), res.Exit, out)
	}
}
