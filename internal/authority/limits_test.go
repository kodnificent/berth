package authority

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func signTemplate(t *testing.T, ca *CA, tmpl *x509.Certificate) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl.SerialNumber = big.NewInt(9)
	tmpl.NotBefore, tmpl.NotAfter = time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
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

func verifiesFor(ca *CA, leaf *x509.Certificate, usage x509.ExtKeyUsage) bool {
	_, err := leaf.Verify(x509.VerifyOptions{Roots: roots(ca), KeyUsages: []x509.ExtKeyUsage{usage}})
	return err == nil
}

func TestTheAuthorityCannotVouchForAMailbox(t *testing.T) {
	ca, _ := newCA(t)
	leaf := signTemplate(t, ca, &x509.Certificate{Subject: pkix.Name{CommonName: "mail"}, EmailAddresses: []string{"someone@example.com"},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}})

	for _, usage := range []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection, x509.ExtKeyUsageAny} {
		if verifiesFor(ca, leaf, usage) {
			t.Errorf("a leaf for someone@example.com verifies for usage %v", usage)
		}
	}
}

func TestTheAuthorityCannotVouchForCodeSigning(t *testing.T) {
	ca, _ := newCA(t)
	leaf := signTemplate(t, ca, &x509.Certificate{Subject: pkix.Name{CommonName: "code"},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning}})

	if verifiesFor(ca, leaf, x509.ExtKeyUsageCodeSigning) {
		t.Errorf("a code signing leaf verifies for code signing")
	}
}

func TestTheAuthorityCannotVouchForAClientURI(t *testing.T) {
	ca, _ := newCA(t)
	uri, _ := url.Parse("spiffe://example.com/client")
	leaf := signTemplate(t, ca, &x509.Certificate{Subject: pkix.Name{CommonName: "client"}, URIs: []*url.URL{uri},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})

	for _, usage := range []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageAny} {
		if verifiesFor(ca, leaf, usage) {
			t.Errorf("a leaf for %s verifies for usage %v", uri, usage)
		}
	}
}

func TestLoadRefusesAnAuthorityMissingAnyOfItsLimits(t *testing.T) {
	weaken := map[string]func(*x509.Certificate){
		"a DNS name besides localhost": func(c *x509.Certificate) { c.PermittedDNSDomains = []string{"localhost", "example.com"} },
		"constraints not critical":     func(c *x509.Certificate) { c.PermittedDNSDomainsCritical = false },
		"IPv6 not excluded":            func(c *x509.Certificate) { c.ExcludedIPRanges = c.ExcludedIPRanges[:1] },
		"only part of IPv4 excluded": func(c *x509.Certificate) {
			c.ExcludedIPRanges[0] = &net.IPNet{IP: net.IPv4(10, 0, 0, 0).To4(), Mask: net.CIDRMask(8, 32)}
		},
		"no email constraint":        func(c *x509.Certificate) { c.PermittedEmailAddresses = nil },
		"no URI constraint":          func(c *x509.Certificate) { c.PermittedURIDomains = nil },
		"no extended key usage":      func(c *x509.Certificate) { c.ExtKeyUsage = nil },
		"client auth besides server": func(c *x509.Certificate) { c.ExtKeyUsage = append(c.ExtKeyUsage, x509.ExtKeyUsageClientAuth) },
		"any path length":            func(c *x509.Certificate) { c.MaxPathLenZero = false; c.MaxPathLen = -1 },
		"a path length of one":       func(c *x509.Certificate) { c.MaxPathLenZero = false; c.MaxPathLen = 1 },
		"not an authority at all":    func(c *x509.Certificate) { c.IsCA = false; c.MaxPathLenZero = false },
	}
	for name, weaker := range weaken {
		t.Run(name, func(t *testing.T) {
			state := t.TempDir()
			key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			tmpl := template(big.NewInt(3), time.Now())
			weaker(tmpl)
			der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
			if err != nil {
				t.Fatal(err)
			}
			keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
			if err := os.MkdirAll(Dir(state), 0o700); err != nil {
				t.Fatal(err)
			}
			writePEM(t, filepath.Join(Dir(state), certFile), "CERTIFICATE", der, 0o644)
			writePEM(t, filepath.Join(Dir(state), keyFile), "PRIVATE KEY", keyDER, 0o600)

			if _, _, err := Load(state); err == nil {
				t.Errorf("an authority with %s was loaded", name)
			}
		})
	}
}
