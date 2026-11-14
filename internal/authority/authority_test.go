package authority

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newCA(t *testing.T) (*CA, string) {
	t.Helper()
	state := t.TempDir()
	ca, created, err := Ensure(state)
	if err != nil || !created {
		t.Fatalf("Ensure in an empty state directory: created %v, %v", created, err)
	}
	return ca, state
}

func roots(ca *CA) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	return pool
}

func signWithAuthority(t *testing.T, ca *CA, dns []string, ips []net.IP) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(7),
		Subject:      pkix.Name{CommonName: "test"},
		DNSNames:     dns,
		IPAddresses:  ips,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

func verify(ca *CA, leaf *x509.Certificate, name string) error {
	_, err := leaf.Verify(x509.VerifyOptions{DNSName: name, Roots: roots(ca), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err
}

func TestLeavesForABerthsNamesVerifyAgainstTheAuthority(t *testing.T) {
	ca, _ := newCA(t)
	issuer, err := ca.Issuer(8)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"feat-x.myapp.localhost", "app.feat-x.myapp.localhost", "localhost"} {
		c, err := issuer.GetCertificate(&tls.ClientHelloInfo{ServerName: name})
		if err != nil {
			t.Fatalf("no certificate for %s: %v", name, err)
		}
		if err := verify(ca, c.Leaf, name); err != nil {
			t.Errorf("the leaf for %s does not verify against the authority: %v", name, err)
		}
	}
}

func TestTheAuthorityCannotVouchForANameOutsideLocalhost(t *testing.T) {
	ca, _ := newCA(t)

	for _, name := range []string{"example.com", "localhost.example.com", "notlocalhost"} {
		if err := verify(ca, signWithAuthority(t, ca, []string{name}, nil), name); err == nil {
			t.Errorf("a certificate the authority signs for %s verifies", name)
		}
	}
}

func TestTheAuthorityCannotVouchForAnIPAddress(t *testing.T) {
	ca, _ := newCA(t)

	for _, ip := range []string{"127.0.0.1", "203.0.113.7", "::1"} {
		leaf := signWithAuthority(t, ca, nil, []net.IP{net.ParseIP(ip)})
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots(ca), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err == nil {
			t.Errorf("a certificate the authority signs for %s verifies", ip)
		}
	}
}

func TestTheIssuerSignsNothingOutsideLocalhost(t *testing.T) {
	ca, _ := newCA(t)
	issuer, err := ca.Issuer(8)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"", "example.com", "feat-x.myapp.localhost.example.com", "127.0.0.1", "bad_name.localhost"} {
		if c, err := issuer.GetCertificate(&tls.ClientHelloInfo{ServerName: name}); err == nil {
			t.Errorf("the issuer gave %q a certificate for %v", name, c.Leaf.DNSNames)
		}
	}
}

func TestTheIssuerKeepsAtMostItsBoundOfLeaves(t *testing.T) {
	ca, _ := newCA(t)
	issuer, err := ca.Issuer(4)
	if err != nil {
		t.Fatal(err)
	}

	for n := range 20 {
		if _, err := issuer.GetCertificate(&tls.ClientHelloInfo{ServerName: fmt.Sprintf("n%d.localhost", n)}); err != nil {
			t.Fatal(err)
		}
	}

	if len(issuer.cache) > 4 || len(issuer.order) > 4 {
		t.Errorf("after 20 names the issuer holds %d leaves, want at most 4", len(issuer.cache))
	}
}

func TestALeafCloseToExpiryIsSignedAgain(t *testing.T) {
	ca, _ := newCA(t)
	issuer, err := ca.Issuer(4)
	if err != nil {
		t.Fatal(err)
	}
	hello := &tls.ClientHelloInfo{ServerName: "feat-x.myapp.localhost"}
	first, err := issuer.GetCertificate(hello)
	if err != nil {
		t.Fatal(err)
	}

	issuer.now = func() time.Time { return first.Leaf.NotAfter.Add(-time.Hour) }
	again, err := issuer.GetCertificate(hello)
	if err != nil {
		t.Fatal(err)
	}

	if !again.Leaf.NotAfter.After(first.Leaf.NotAfter) {
		t.Errorf("a leaf an hour from expiry was served again, expiring %s", again.Leaf.NotAfter)
	}
}

func TestTheAuthorityKeyIsReadableOnlyByTheUser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows keeps no permission bits")
	}
	_, state := newCA(t)

	info, err := os.Stat(filepath.Join(Dir(state), keyFile))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("the key has mode %#o, want 0600", mode)
	}
}

func TestEnsureLoadsTheAuthorityItMadeBefore(t *testing.T) {
	ca, state := newCA(t)

	again, created, err := Ensure(state)

	if err != nil || created || again.SHA256() != ca.SHA256() {
		t.Errorf("Ensure a second time: created %v, %v; want the same authority loaded", created, err)
	}
}

func TestAnAuthorityWhoseKeyOthersCanReadIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows keeps no permission bits")
	}
	_, state := newCA(t)
	key := filepath.Join(Dir(state), keyFile)
	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := Ensure(state)

	if err == nil || !strings.Contains(err.Error(), key) {
		t.Errorf("Ensure with the key at 0644: %v, want a refusal naming %s", err, key)
	}
	if info, _ := os.Stat(key); info.Mode().Perm() != 0o644 {
		t.Errorf("the key's mode changed to %#o; berth must not quietly narrow a key that may have been copied", info.Mode().Perm())
	}
}

func writePEM(t *testing.T, path, kind string, der []byte, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), perm); err != nil {
		t.Fatal(err)
	}
}
