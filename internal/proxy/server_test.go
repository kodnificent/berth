package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func backend(t *testing.T, l net.Listener) int {
	t.Helper()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"host": r.Host, "header": r.Header})
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return l.Addr().(*net.TCPAddr).Port
}

type seen struct {
	Host   string      `json:"host"`
	Header http.Header `json:"header"`
}

func loopbackRequest(url string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.RemoteAddr = "127.0.0.1:50000"
	return req
}

func through(t *testing.T, port int, req *http.Request) (int, seen) {
	t.Helper()
	s := NewServer(80)
	s.SetTable(Table{{Name: "feat-x", Host: "feat-x.myapp.localhost", Port: Target{Port: port}}})
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	var got seen
	body, _ := io.ReadAll(rec.Body)
	_ = json.Unmarshal(body, &got)
	return rec.Code, got
}

func TestTheAppSeesNoForwardedHeaderTheClientSent(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := backend(t, l)
	req := loopbackRequest("http://app.feat-x.myapp.localhost/")
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.Header.Set("X-Forwarded-Host", "bank.example")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Forwarded", "for=203.0.113.9;proto=https")
	req.Header.Set("X-Real-Ip", "203.0.113.9")
	req.Header.Set("X-Original-Host", "bank.example")
	for _, k := range []string{"X-Forwarded-Port", "X-Forwarded-Prefix", "X-Forwarded-Ssl", "X-Forwarded-Scheme", "X-Forwarded-Server"} {
		req.Header.Set(k, "spoofed")
	}

	code, got := through(t, port, req)

	if code != http.StatusOK {
		t.Fatalf("status %d, want the app's 200", code)
	}
	want := map[string]string{"X-Forwarded-For": "127.0.0.1", "X-Forwarded-Host": "app.feat-x.myapp.localhost", "X-Forwarded-Proto": "http",
		"Forwarded": "", "X-Real-Ip": "", "X-Original-Host": "", "X-Forwarded-Port": "", "X-Forwarded-Prefix": "", "X-Forwarded-Ssl": "", "X-Forwarded-Scheme": "", "X-Forwarded-Server": ""}
	for k, v := range want {
		if g := strings.Join(got.Header.Values(k), ", "); g != v {
			t.Errorf("the app saw %s %q, want %q", k, g, v)
		}
	}
	if got.Host != "app.feat-x.myapp.localhost" {
		t.Errorf("the app saw Host %q, want the one requested", got.Host)
	}
}

func TestAnAppListeningOnlyOnTheIPv6LoopbackIsReached(t *testing.T) {
	l, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback here: %v", err)
	}
	port := backend(t, l)

	code, got := through(t, port, loopbackRequest("http://feat-x.myapp.localhost/"))

	if code != http.StatusOK || got.Host != "feat-x.myapp.localhost" {
		t.Errorf("status %d, Host %q; want the app on ::1 to answer", code, got.Host)
	}
}

func TestARequestFromANonLoopbackPeerNeverReachesTheApp(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := backend(t, l)
	req := httptest.NewRequest(http.MethodGet, "http://feat-x.myapp.localhost/", nil)
	req.RemoteAddr = "198.51.100.7:40000"

	code, got := through(t, port, req)

	if code != http.StatusForbidden || got.Host != "" {
		t.Errorf("status %d, app saw Host %q; want 403 and nothing reaching the app", code, got.Host)
	}
}

func TestANameUnderAGoneBerthsHostGetsTheNotFoundPage(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := backend(t, l)
	s := NewServer(80)
	s.SetTable(Table{
		{Name: "feat-x", Host: "feat-x.myapp.localhost", Port: Target{Port: port}},
		{Name: "app", Host: "app.feat-x.myapp.localhost", Gone: true},
	})
	req := loopbackRequest("http://api.app.feat-x.myapp.localhost/")
	rec := httptest.NewRecorder()

	s.ServeHTTP(rec, req)

	if body := rec.Body.String(); rec.Code != http.StatusNotFound || strings.Contains(body, "http://app.feat-x.myapp.localhost") {
		t.Errorf("status %d, want 404 not listing the gone berth:\n%s", rec.Code, body)
	}
}

// countingBackend counts the requests that reach it.
func countingBackend(t *testing.T) (int, *atomic.Int32) {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) })}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return l.Addr().(*net.TCPAddr).Port, &hits
}

func TestARefusedBerthGetsTheBadGatewayPageAndNothingReachesItsPort(t *testing.T) {
	port, hits := countingBackend(t)
	s := NewServer(80)
	s.SetTable(Table{{Name: "feat-x", Host: "feat-x.myapp.localhost", Port: Target{Port: port, Refused: "its config sets no [proxy] port"}}})

	for _, host := range []string{"feat-x.myapp.localhost", "app.feat-x.myapp.localhost"} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, loopbackRequest("http://"+host+"/"))
		if body := rec.Body.String(); rec.Code != http.StatusBadGateway || !strings.Contains(body, "feat-x") || !strings.Contains(body, "its config sets no [proxy] port") {
			t.Errorf("%s: status %d, want 502 naming feat-x and the reason:\n%s", host, rec.Code, body)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the port in the refused target got %d requests, want none", n)
	}
}

// peerListener hands out connections that claim to come from another host, as one
// routed to the loopback address from outside would.
type peerListener struct{ net.Listener }

type peerConn struct{ net.Conn }

func (c peerConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("198.51.100.7"), Port: 40000}
}

func (l peerListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return peerConn{c}, nil
}

func TestANonLoopbackPeerGetsAClosedConnectionAndNoAnswer(t *testing.T) {
	port, hits := countingBackend(t)
	s := NewServer(80)
	s.SetTable(Table{{Name: "feat-x", Host: "feat-x.myapp.localhost", Port: Target{Port: port}}})
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: s}
	go srv.Serve(peerListener{l})
	defer srv.Close()

	conn, err := net.DialTimeout("tcp4", l.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: feat-x.myapp.localhost\r\n\r\n")
	got, err := io.ReadAll(conn)

	if len(got) > 0 || err != nil {
		t.Errorf("the client got %q, error %v; want the connection closed with no answer", got, err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the app got %d requests from a non-loopback peer, want none", n)
	}
}
