package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/kodnificent/berth/internal/authority"
)

// tlsThrough serves TLS for every name under .localhost on l, wrapped by LoopbackOnly as
// the proxy's https port is, and dials it trusting the authority.
func tlsThrough(t *testing.T, wrap func(net.Listener) net.Listener) (tls.ConnectionState, error) {
	t.Helper()
	ca, _, err := authority.Ensure(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := ca.Issuer(8)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.NotFoundHandler(), TLSConfig: &tls.Config{GetCertificate: issuer.GetCertificate}}
	go srv.ServeTLS(LoopbackOnly(wrap(l)), "", "")
	t.Cleanup(func() { srv.Close() })
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp4", l.Addr().String(), &tls.Config{ServerName: "feat-x.myapp.localhost", RootCAs: pool})
	if err != nil {
		return tls.ConnectionState{}, err
	}
	defer conn.Close()
	return conn.ConnectionState(), nil
}

func TestANonLoopbackPeerGetsNoTLSHandshakeAndNoCertificate(t *testing.T) {
	state, err := tlsThrough(t, func(l net.Listener) net.Listener { return peerListener{l} })

	if err == nil || len(state.PeerCertificates) > 0 {
		t.Errorf("a non-loopback peer completed a handshake (%v) and got %d certificates", err, len(state.PeerCertificates))
	}
}

func TestALoopbackPeerCompletesTheTLSHandshake(t *testing.T) {
	state, err := tlsThrough(t, func(l net.Listener) net.Listener { return l })

	if err != nil || len(state.PeerCertificates) == 0 {
		t.Errorf("a loopback peer's handshake: %v, %d certificates; want it completed", err, len(state.PeerCertificates))
	}
}
