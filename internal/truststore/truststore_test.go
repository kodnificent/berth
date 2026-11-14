package truststore

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

func testCert(t *testing.T) Cert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test authority"}, IsCA: true, BasicConstraintsValid: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return Cert{DER: der}
}

// memoryStore stands in for a system's store, holding fingerprints, and can fail its
// next add after taking the certificate, as a cancelled dialog or a failed rebuild does.
type memoryStore struct {
	held    map[string]bool
	failAdd bool
	dropAdd bool
}

func (m *memoryStore) store() Store { return m.named("memory") }

func (m *memoryStore) named(name string) Store {
	m.held = map[string]bool{}
	return Store{Throwaway: name, ops: &ops{
		file: func(Store, Cert) (string, error) { return "", nil },
		holds: func(_ Store, e Entry) (presence, error) {
			if m.held[e.SHA256] {
				return held, nil
			}
			return absent, nil
		},
		add: func(_ Store, e Entry, _ []byte, _ bool, _ func() error) error {
			if !m.dropAdd {
				m.held[e.SHA256] = true
			}
			if m.failAdd {
				m.failAdd = false
				return errors.New("the update step failed")
			}
			return nil
		},
		remove: func(_ Store, e Entry, _ presence) error { delete(m.held, e.SHA256); return nil },
	}}
}

func TestUntrustWithNothingTrustedRemovesNothing(t *testing.T) {
	m := &memoryStore{}

	removed, gone, err := m.store().Untrust(t.TempDir())

	if removed != 0 || gone != 0 || err != nil {
		t.Errorf("untrust with nothing recorded: removed %d, gone %d, %v; want nothing and no error", removed, gone, err)
	}
}

func TestUntrustRemovesWhatATrustThatFailedAfterAddingLeftInTheStore(t *testing.T) {
	state, m, cert := t.TempDir(), &memoryStore{failAdd: true}, testCert(t)
	s := m.store()
	if _, err := s.Trust(state, cert); err == nil {
		t.Fatal("trust whose add fails succeeded")
	}

	removed, _, err := s.Untrust(state)

	if err != nil || removed != 1 || len(m.held) != 0 {
		t.Errorf("untrust after a failed trust: removed %d, %v, store holds %v; want the certificate removed", removed, err, m.held)
	}
}

func TestTrustAgainFinishesATrustThatFailedAfterAdding(t *testing.T) {
	state, m, cert := t.TempDir(), &memoryStore{failAdd: true}, testCert(t)
	s := m.store()
	if _, err := s.Trust(state, cert); err == nil {
		t.Fatal("trust whose add fails succeeded")
	}

	added, err := s.Trust(state, cert)

	if trusted, _ := s.Trusts(state, cert); err != nil || !added || !trusted {
		t.Errorf("trust again: added %v, %v, trusted %v; want it finished", added, err, trusted)
	}
}

func TestTrustFailsWhenTheStoreDoesNotHoldTheCertificateAfterAdding(t *testing.T) {
	state, m, cert := t.TempDir(), &memoryStore{dropAdd: true}, testCert(t)

	if _, err := m.store().Trust(state, cert); err == nil || !strings.Contains(err.Error(), cert.SHA256()) {
		t.Errorf("trust into a store that kept nothing: %v, want a failure naming the fingerprint", err)
	}
}

func TestUntrustOfACertificateRemovedElsewhereSaysItWasGone(t *testing.T) {
	state, m, cert := t.TempDir(), &memoryStore{}, testCert(t)
	s := m.store()
	if _, err := s.Trust(state, cert); err != nil {
		t.Fatal(err)
	}
	delete(m.held, cert.SHA256())

	removed, gone, err := s.Untrust(state)

	if list, _ := Recorded(state); err != nil || removed != 0 || gone != 1 || len(list) != 0 {
		t.Errorf("untrust of a certificate gone from the store: removed %d, gone %d, %v, record %v; want it said gone and forgotten", removed, gone, err, list)
	}
}

func TestUntrustInOneStoreLeavesTheOtherStoresCertificateAndRecord(t *testing.T) {
	state, cert := t.TempDir(), testCert(t)
	ma, mb := &memoryStore{}, &memoryStore{}
	a, b := ma.named("store-a"), mb.named("store-b")
	for _, s := range []Store{a, b} {
		if _, err := s.Trust(state, cert); err != nil {
			t.Fatal(err)
		}
	}

	removed, _, err := a.Untrust(state)

	if err != nil || removed != 1 || ma.held[cert.SHA256()] {
		t.Errorf("untrust in store-a: removed %d, %v, store-a holds it %v; want it removed", removed, err, ma.held[cert.SHA256()])
	}
	if !mb.held[cert.SHA256()] {
		t.Errorf("untrust in store-a took the certificate out of store-b")
	}
	if list, _ := Recorded(state); len(list) != 1 || list[0].Store != "store-b" {
		t.Errorf("the record after untrust in store-a is %+v, want store-b's entry alone", list)
	}
	if removed, _, err := b.Untrust(state); err != nil || removed != 1 || mb.held[cert.SHA256()] {
		t.Errorf("a later untrust in store-b: removed %d, %v; want store-b's certificate removed", removed, err)
	}
}
