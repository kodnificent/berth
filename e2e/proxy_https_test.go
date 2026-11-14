//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

type authority struct {
	cert     *x509.Certificate
	certPath string
	key      crypto.Signer
	keyPath  string
}

func (a authority) pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(a.cert)
	return pool
}

func parsePrivateKey(der []byte) crypto.Signer {
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		if s, ok := k.(crypto.Signer); ok {
			return s
		}
	}
	if k, err := x509.ParseECPrivateKey(der); err == nil {
		return k
	}
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k
	}
	return nil
}

// loadAuthority finds the certificate authority in berth's state directory by what the
// PEM files hold rather than by their names, which the design does not fix.
func loadAuthority(t *testing.T, stateDir string) authority {
	t.Helper()
	var cas []authority
	seen := map[[32]byte]bool{}
	type keyFile struct {
		key  crypto.Signer
		path string
	}
	var keys []keyFile
	err := filepath.WalkDir(stateDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for {
			var block *pem.Block
			block, data = pem.Decode(data)
			if block == nil {
				break
			}
			switch {
			case block.Type == "CERTIFICATE":
				c, err := x509.ParseCertificate(block.Bytes)
				if err != nil || !c.BasicConstraintsValid || !c.IsCA || seen[sha256.Sum256(c.Raw)] {
					continue
				}
				seen[sha256.Sum256(c.Raw)] = true
				cas = append(cas, authority{cert: c, certPath: path})
			case strings.HasSuffix(block.Type, "PRIVATE KEY"):
				if k := parsePrivateKey(block.Bytes); k != nil {
					keys = append(keys, keyFile{k, path})
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reading berth's state directory %s: %v", stateDir, err)
	}
	if len(cas) != 1 {
		t.Fatalf("berth's state directory %s holds %d certificate authorities in PEM, want 1", stateDir, len(cas))
	}
	a := cas[0]
	for _, k := range keys {
		if pub, ok := k.key.Public().(interface{ Equal(crypto.PublicKey) bool }); ok && pub.Equal(a.cert.PublicKey) {
			a.key, a.keyPath = k.key, k.path
			return a
		}
	}
	t.Fatalf("berth's state directory %s holds no PEM private key for the authority in %s", stateDir, a.certPath)
	return a
}

func proxyTLSUserConfig(t *testing.T, repo *Repo, extra string) (int, int) {
	t.Helper()
	httpPort, httpsPort := loopbackFreePort(t), loopbackFreePort(t)
	writeFile(t, repo.userConfig(), fmt.Sprintf("[proxy]\nhttp = %d\nhttps = %d\n\n%s", httpPort, httpsPort, extra))
	return httpPort, httpsPort
}

type tlsProxy struct {
	*runningProxy
	httpsPort int
	ca        authority
}

// startTLSProxy starts the proxy and waits for its https port, by which time the
// authority must be in the state directory.
func startTLSProxy(t *testing.T, repo *Repo, httpPort, httpsPort int) *tlsProxy {
	t.Helper()
	p := startProxy(t, repo, httpPort)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(httpsPort))
	deadline := time.Now().Add(15 * time.Second)
	for {
		if c, err := net.DialTimeout("tcp4", addr, 200*time.Millisecond); err == nil {
			c.Close()
			break
		}
		if !p.running() || time.Now().After(deadline) {
			t.Fatalf("berth proxy does not accept on its https port %s:\n%s", addr, p.out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	return &tlsProxy{runningProxy: p, httpsPort: httpsPort, ca: loadAuthority(t, repo.StateDir)}
}

// client trusts only the proxy's authority and dials the proxy's https port for every
// host, so a name needs no DNS and its certificate is checked against that host.
func (p *tlsProxy) client(jar http.CookieJar) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(p.httpsPort))
	return &http.Client{
		Jar:           jar,
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			Proxy:             nil,
			DisableKeepAlives: true,
			TLSClientConfig:   &tls.Config{RootCAs: p.ca.pool()},
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "tcp4", addr)
			},
		},
	}
}

func (p *tlsProxy) mustGetTLS(t *testing.T, client *http.Client, url string) (*http.Response, string) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s through the proxy, trusting only its authority: %v\nproxy output:\n%s", url, err, p.out.String())
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

func (p *tlsProxy) mustEchoTLS(t *testing.T, url string) echoed {
	t.Helper()
	resp, body := p.mustGetTLS(t, p.client(nil), url)
	var e echoed
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &e) != nil || e.Backend == "" {
		t.Fatalf("GET %s through the proxy: status %d, want the app's answer:\n%s", url, resp.StatusCode, body)
	}
	return e
}

func signLeaf(t *testing.T, a authority, dnsNames []string, ips []net.IP) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "berth e2e leaf"},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.cert, &key.PublicKey, a.key)
	if err != nil {
		t.Fatalf("signing a leaf with the authority's key: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

func verifyAgainst(a authority, leaf *x509.Certificate) error {
	_, err := leaf.Verify(x509.VerifyOptions{Roots: a.pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err
}

func startBareTLSProxy(t *testing.T) (*Repo, *tlsProxy) {
	t.Helper()
	repo := NewRepo(t)
	httpPort, httpsPort := proxyTLSUserConfig(t, repo, "")
	return repo, startTLSProxy(t, repo, httpPort, httpsPort)
}

func TestProxyServesHTTPSThatVerifiesAgainstItsAuthority(t *testing.T) {
	repo := NewRepo(t)
	httpPort, httpsPort := proxyTLSUserConfig(t, repo, "")
	proxyRepoConfig(t, repo, "")
	_, s := addProxiedBerth(t, repo, "feat-x")
	serveEcho(t, s.port(t, "web", 80).HostPort, "web")
	p := startTLSProxy(t, repo, httpPort, httpsPort)

	for _, host := range []string{"feat-x.myapp.localhost", "app.feat-x.myapp.localhost"} {
		e := p.mustEchoTLS(t, "https://"+host+"/dashboard")
		if e.Backend != "web" || e.Host != host || e.Path != "/dashboard" {
			t.Errorf("GET https://%s/dashboard reached %s with Host %q and path %q, want web with Host %q", host, e.Backend, e.Host, e.Path, host)
		}
	}
}

func TestProxyServesTheSameNameOverHTTPSAgain(t *testing.T) {
	repo := NewRepo(t)
	httpPort, httpsPort := proxyTLSUserConfig(t, repo, "")
	proxyRepoConfig(t, repo, "")
	_, s := addProxiedBerth(t, repo, "feat-x")
	serveEcho(t, s.port(t, "web", 80).HostPort, "web")
	p := startTLSProxy(t, repo, httpPort, httpsPort)

	for i := 0; i < 2; i++ {
		if e := p.mustEchoTLS(t, "https://app.feat-x.myapp.localhost/"); e.Host != "app.feat-x.myapp.localhost" {
			t.Errorf("request %d for app.feat-x.myapp.localhost reached the app with Host %q", i+1, e.Host)
		}
	}
}

func TestProxySetsXForwardedProtoToHTTPSOverTLS(t *testing.T) {
	repo := NewRepo(t)
	httpPort, httpsPort := proxyTLSUserConfig(t, repo, "")
	proxyRepoConfig(t, repo, "")
	_, s := addProxiedBerth(t, repo, "feat-x")
	serveEcho(t, s.port(t, "web", 80).HostPort, "web")
	p := startTLSProxy(t, repo, httpPort, httpsPort)

	if e := p.mustEchoTLS(t, "https://app.feat-x.myapp.localhost/"); e.ForwardedProto != "https" {
		t.Errorf("X-Forwarded-Proto over TLS = %q, want https", e.ForwardedProto)
	}
}

func TestProxyCarriesAWebSocketBothWaysOverTLS(t *testing.T) {
	repo := NewRepo(t)
	httpPort, httpsPort := proxyTLSUserConfig(t, repo, "")
	proxyRepoConfig(t, repo, "")
	_, s := addProxiedBerth(t, repo, "feat-x")
	serveEcho(t, s.port(t, "web", 80).HostPort, "web")
	p := startTLSProxy(t, repo, httpPort, httpsPort)

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(httpsPort)), &tls.Config{ServerName: "app.feat-x.myapp.localhost", RootCAs: p.ca.pool()})
	if err != nil {
		t.Fatalf("TLS to app.feat-x.myapp.localhost, trusting only the authority: %v\nproxy output:\n%s", err, p.out.String())
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	key := base64.StdEncoding.EncodeToString([]byte(randomHex(t, 8)))
	fmt.Fprintf(conn, "GET /socket HTTP/1.1\r\nHost: app.feat-x.myapp.localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", key)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("reading the upgrade response over TLS: %v\nproxy output:\n%s", err, p.out.String())
	}
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Sec-WebSocket-Accept") != webSocketAccept(key) {
		t.Fatalf("upgrade over TLS: status %d, Sec-WebSocket-Accept %q, want 101 with the app's accept key", resp.StatusCode, resp.Header.Get("Sec-WebSocket-Accept"))
	}
	if msg, err := readTextFrame(br); err != nil || msg != "hello from app.feat-x.myapp.localhost" {
		t.Fatalf("message from the app over TLS: %q, %v", msg, err)
	}
	if err := writeTextFrame(conn, true, "ping"); err != nil {
		t.Fatal(err)
	}
	if msg, err := readTextFrame(br); err != nil || msg != "echo: ping" {
		t.Errorf("the app's answer to a message sent over TLS: %q, %v, want \"echo: ping\"", msg, err)
	}
}

func TestProxyAuthorityCannotVouchForANameOutsideLocalhost(t *testing.T) {
	_, p := startBareTLSProxy(t)

	if err := verifyAgainst(p.ca, signLeaf(t, p.ca, []string{"feat-x.myapp.localhost"}, nil)); err != nil {
		t.Fatalf("a leaf the test signs with the authority for feat-x.myapp.localhost does not verify, so the check below proves nothing: %v", err)
	}
	if err := verifyAgainst(p.ca, signLeaf(t, p.ca, []string{"example.com"}, nil)); err == nil {
		t.Errorf("a certificate the authority signs for example.com verifies against it as the only root")
	}
}

func TestProxyAuthorityCannotVouchForAnIPAddress(t *testing.T) {
	_, p := startBareTLSProxy(t)

	if err := verifyAgainst(p.ca, signLeaf(t, p.ca, nil, []net.IP{net.ParseIP("203.0.113.7")})); err == nil {
		t.Errorf("a certificate the authority signs for the address 203.0.113.7 verifies against it as the only root")
	}
}

func TestProxyRefusesATLSHandshakeForANameOutsideLocalhost(t *testing.T) {
	_, p := startBareTLSProxy(t)

	for _, name := range []string{"example.com", "feat-x.myapp.localhost.example.com"} {
		dialer := &net.Dialer{Timeout: 5 * time.Second}
		conn, err := tls.DialWithDialer(dialer, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(p.httpsPort)), &tls.Config{
			ServerName:         name,
			InsecureSkipVerify: true, // whatever the proxy serves for this name must be seen, verified or not
		})
		if err != nil {
			continue
		}
		served := conn.ConnectionState().PeerCertificates
		conn.Close()
		if len(served) > 0 && served[0].VerifyHostname(name) == nil {
			t.Errorf("the proxy completed a TLS handshake for %s with a certificate for that name", name)
		}
	}
}

func TestProxyAuthorityKeyIsReadableOnlyByTheUser(t *testing.T) {
	_, p := startBareTLSProxy(t)

	if runtime.GOOS == "windows" {
		t.Skip("Windows keeps no permission bits; the ACL of the authority's key is not checked here")
	}
	info, err := os.Stat(p.ca.keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("the authority's key %s has mode %#o, want 0600", p.ca.keyPath, mode)
	}
}
