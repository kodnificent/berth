// Package authority is berth proxy's local certificate authority: made once in berth's
// state directory, able to vouch only for names under .localhost, and signing a
// certificate for each name the proxy is asked for.
package authority

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/names"
)

const (
	certFile = "cert.pem"
	keyFile  = "key.pem"
	lifetime = 10 * 365 * 24 * time.Hour
)

// CA is the authority as loaded from the state directory. Its key never leaves the
// process: nothing prints, logs or sends it.
type CA struct {
	Cert     *x509.Certificate
	CertPath string
	key      crypto.Signer
}

// Dir is where the authority lives in the state directory state.
func Dir(state string) string { return filepath.Join(state, "proxy", "ca") }

// Covers reports whether the authority can vouch for name: a hostname that is localhost
// or under it.
func Covers(name string) bool {
	return names.ValidHost(name) && names.Within("localhost", name)
}

// SHA256 is the certificate's fingerprint, as berth records it.
func (ca *CA) SHA256() string {
	sum := sha256.Sum256(ca.Cert.Raw)
	return hex.EncodeToString(sum[:])
}

// Ensure loads the authority in state, making it first when there is none; created says
// it was made now.
func Ensure(state string) (ca *CA, created bool, err error) {
	ca, ok, err := Load(state)
	if err != nil || ok {
		return ca, false, err
	}
	if err := create(state); err != nil {
		return nil, false, fmt.Errorf("making berth proxy's certificate authority in %s: %w", Dir(state), err)
	}
	ca, ok, err = Load(state)
	if err == nil && !ok {
		err = fmt.Errorf("berth proxy's certificate authority in %s vanished as it was made", Dir(state))
	}
	return ca, err == nil, err
}

// Load reads the authority in state; ok is false when there is none yet.
func Load(state string) (ca *CA, ok bool, err error) {
	dir := Dir(state)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	certPath, keyPath := filepath.Join(dir, certFile), filepath.Join(dir, keyFile)
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, false, broken(dir, err)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		return nil, false, broken(dir, err)
	}
	// Windows keeps no permission bits; the key's ACL is its directory's, the user's own.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, false, apperr.New(apperr.Generic, "the key of berth proxy's certificate authority, %s, gives its group or others some permission (mode %#o), so it may have been copied or changed and berth no longer uses it", keyPath, info.Mode().Perm()).
			WithNext("run berth proxy untrust, delete " + dir + ", and berth makes a new authority the next time it needs one")
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, false, broken(dir, err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, false, broken(dir, errors.New(certFile+" holds no PEM certificate"))
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, false, broken(dir, err)
	}
	if !constrained(cert) {
		return nil, false, apperr.New(apperr.Generic, "berth proxy's certificate authority in %s does not carry every limit berth gives one, so berth no longer uses it: it was made by an earlier berth, or put there by something else", dir).
			WithNext("run berth proxy untrust, delete " + dir + ", and berth makes a new authority the next time it needs one; run berth proxy trust again after")
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, false, broken(dir, errors.New(keyFile+" holds no PEM key"))
	}
	parsed, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
	if err != nil {
		return nil, false, broken(dir, errors.New(keyFile+" holds no key berth can read"))
	}
	key, isSigner := parsed.(crypto.Signer)
	pub, comparable := cert.PublicKey.(interface{ Equal(crypto.PublicKey) bool })
	if !isSigner || !comparable || !pub.Equal(key.Public()) {
		return nil, false, broken(dir, errors.New(keyFile+" is not the key of "+certFile))
	}
	if time.Now().After(cert.NotAfter) {
		return nil, false, apperr.New(apperr.Generic, "berth proxy's certificate authority in %s expired on %s", dir, cert.NotAfter.Format("2006-01-02")).
			WithNext("run berth proxy untrust, delete " + dir + ", and berth makes a new authority the next time it needs one")
	}
	return &CA{Cert: cert, CertPath: certPath, key: key}, true, nil
}

func broken(dir string, err error) error {
	return apperr.New(apperr.Generic, "berth proxy's certificate authority in %s can't be read: %v", dir, err).
		WithNext("run berth proxy untrust, delete " + dir + ", and berth makes a new authority the next time it needs one")
}

// constrained reports whether cert carries exactly the limits template gives the
// authority, so an authority put in its place that could vouch for more is never used.
func constrained(cert *x509.Certificate) bool {
	localhost := []string{"localhost"}
	return cert.IsCA && cert.BasicConstraintsValid && cert.MaxPathLen == 0 && cert.MaxPathLenZero &&
		slices.Equal(cert.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}) && len(cert.UnknownExtKeyUsage) == 0 &&
		cert.PermittedDNSDomainsCritical &&
		slices.Equal(cert.PermittedDNSDomains, localhost) && len(cert.ExcludedDNSDomains) == 0 &&
		slices.Equal(cert.PermittedEmailAddresses, localhost) && slices.Equal(cert.PermittedURIDomains, localhost) &&
		len(cert.PermittedIPRanges) == 0 && excludesEveryAddress(cert.ExcludedIPRanges)
}

// excludesEveryAddress reports whether ranges hold all of IPv4 and all of IPv6.
func excludesEveryAddress(ranges []*net.IPNet) bool {
	var v4, v6 bool
	for _, r := range ranges {
		ones, bits := r.Mask.Size()
		v4 = v4 || (ones == 0 && bits == 32)
		v6 = v6 || (ones == 0 && bits == 128)
	}
	return v4 && v6
}

// create makes the authority in a directory of its own and moves it into place whole, so
// a reader never sees a certificate without its key, and of two berth processes making
// one at once, the first to move its directory wins and the other's is discarded.
func create(state string) error {
	parent := filepath.Dir(Dir(state))
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".tmp-ca-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	certPEM, keyPEM, err := newAuthority()
	if err != nil {
		return err
	}
	if err := writeNew(filepath.Join(tmp, keyFile), keyPEM, 0o600); err != nil {
		return err
	}
	if err := writeNew(filepath.Join(tmp, certFile), certPEM, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, Dir(state)); err != nil {
		if _, statErr := os.Stat(Dir(state)); statErr == nil {
			return nil
		}
		return err
	}
	return nil
}

// writeNew creates path with perm from the start: the key is never readable by anyone
// else, not even for a moment before a chmod.
func writeNew(path string, data []byte, perm fs.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func newAuthority() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := serialNumber()
	if err != nil {
		return nil, nil, err
	}
	tmpl := template(serial, time.Now())
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

// template is the authority's certificate. Each limit narrows what trusting it allows:
// server certificates only, for names under localhost only. The email and URI subtrees
// permit only localhost, so a leaf naming any other mailbox or URI fails too.
func template(serial *big.Int, now time.Time) *x509.Certificate {
	return &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "berth proxy local authority " + fmt.Sprintf("%016x", serial)[:8], Organization: []string{"berth"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(lifetime),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		MaxPathLen:            0,
		// Critical, so a client that can't read the limits refuses the authority rather
		// than trust it for every site.
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         []string{"localhost"},
		ExcludedIPRanges: []*net.IPNet{
			{IP: net.IPv4zero.To4(), Mask: net.CIDRMask(0, 32)},
			{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)},
		},
		PermittedEmailAddresses: []string{"localhost"},
		PermittedURIDomains:     []string{"localhost"},
		// Go checks a chain's extended key usages at every certificate, the root
		// included, and so do the system verifiers, so the authority vouches for TLS
		// servers and nothing else.
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
}

func serialNumber() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}
