package authority

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	leafLifetime = 30 * 24 * time.Hour
	// leafRenew is how close to expiry a cached leaf is signed again, so a proxy left
	// running for weeks never serves an expired one.
	leafRenew = 24 * time.Hour
)

// Issuer signs a certificate for each name under .localhost it is asked for, keeping at
// most max of them, so a client asking for endless names can't grow the proxy's memory.
// Every leaf shares one key, made in memory and never written anywhere.
type Issuer struct {
	ca  *CA
	key *ecdsa.PrivateKey
	max int
	now func() time.Time

	mu    sync.Mutex
	cache map[string]*tls.Certificate
	order []string
}

func (ca *CA) Issuer(max int) (*Issuer, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Issuer{ca: ca, key: key, max: max, now: time.Now, cache: map[string]*tls.Certificate{}}, nil
}

// GetCertificate is the tls.Config hook. A name outside .localhost, or no name at all,
// gets no certificate and the handshake fails.
func (i *Issuer) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	name := strings.TrimSuffix(strings.ToLower(hello.ServerName), ".")
	if !Covers(name) {
		return nil, fmt.Errorf("berth proxy serves certificates only for names under .localhost, not %q", hello.ServerName)
	}
	return i.leaf(name)
}

func (i *Issuer) leaf(name string) (*tls.Certificate, error) {
	now := i.now()
	i.mu.Lock()
	if c, ok := i.cache[name]; ok && now.Add(leafRenew).Before(c.Leaf.NotAfter) {
		i.mu.Unlock()
		return c, nil
	}
	i.mu.Unlock()

	c, err := i.sign(name, now)
	if err != nil {
		return nil, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, ok := i.cache[name]; !ok {
		i.order = append(i.order, name)
	}
	i.cache[name] = c
	for len(i.order) > i.max {
		delete(i.cache, i.order[0])
		i.order = i.order[1:]
	}
	return c, nil
}

func (i *Issuer) sign(name string, now time.Time) (*tls.Certificate, error) {
	serial, err := serialNumber()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name, Organization: []string{"berth"}},
		DNSNames:     []string{name},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(leafLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, i.ca.Cert, &i.key.PublicKey, i.ca.key)
	if err != nil {
		return nil, fmt.Errorf("signing a certificate for %s: %w", name, err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: i.key, Leaf: leaf}, nil
}
