//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// trustStoreVar names the throwaway store berth proxy trust and untrust must use in
// place of the system's: a keychain file on macOS, a directory standing in for the
// system's CA directory on Linux, a CurrentUser certificate store's name on Windows.
const trustStoreVar = "BERTH_TRUST_STORE"

type throwawayStore struct {
	value string
}

func runQuiet(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

func mustRunQuiet(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := runQuiet(name, args...)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return out
}

func unrelatedCertPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "berth e2e unrelated " + randomHex(t, 4)},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// newThrowawayStore makes a store only this test uses, already holding a certificate
// that isn't berth's, so untrust is seen to remove only its own. It never touches the
// machine's real trust store.
func newThrowawayStore(t *testing.T) throwawayStore {
	t.Helper()
	dir := t.TempDir()
	other := filepath.Join(dir, "unrelated.pem")
	if err := os.WriteFile(other, unrelatedCertPEM(t), 0o644); err != nil {
		t.Fatal(err)
	}
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("security"); err != nil {
			t.Skip("macOS's security tool is missing, so no throwaway keychain can be made")
		}
		searchList := mustRunQuiet(t, "security", "list-keychains", "-d", "user")
		t.Cleanup(func() {
			if now, _ := runQuiet("security", "list-keychains", "-d", "user"); now != searchList {
				args := []string{"list-keychains", "-d", "user", "-s"}
				for _, m := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(searchList, -1) {
					args = append(args, m[1])
				}
				if out, err := runQuiet("security", args...); err != nil {
					t.Errorf("restoring the keychain search list: %v\n%s", err, out)
				}
			}
		})
		kc := filepath.Join(dir, "berth-e2e.keychain")
		mustRunQuiet(t, "security", "create-keychain", "-p", "berth-e2e", kc)
		t.Cleanup(func() { runQuiet("security", "delete-keychain", kc) })
		mustRunQuiet(t, "security", "add-certificates", "-k", kc, other)
		return throwawayStore{kc}
	case "linux":
		store := filepath.Join(dir, "ca-certificates")
		if err := os.MkdirAll(store, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(other, filepath.Join(store, "unrelated.crt")); err != nil {
			t.Fatal(err)
		}
		return throwawayStore{store}
	case "windows":
		if _, err := exec.LookPath("certutil"); err != nil {
			t.Skip("certutil is missing, so no throwaway certificate store can be made")
		}
		name := "BerthE2E" + randomHex(t, 4)
		t.Cleanup(func() {
			runQuiet("reg", "delete", `HKCU\Software\Microsoft\SystemCertificates\`+name, "/f")
		})
		mustRunQuiet(t, "certutil", "-user", "-f", "-addstore", name, other)
		return throwawayStore{name}
	}
	t.Skipf("no throwaway trust store is known for %s", runtime.GOOS)
	return throwawayStore{}
}

var sha1Line = regexp.MustCompile(`(?i)(?:SHA-1 hash|Cert Hash\(sha1\)):\s*([0-9a-f ]+)`)

func sha1Hashes(out string) []string {
	var hashes []string
	for _, m := range sha1Line.FindAllStringSubmatch(out, -1) {
		hashes = append(hashes, strings.ToLower(strings.ReplaceAll(strings.TrimSpace(m[1]), " ", "")))
	}
	sort.Strings(hashes)
	return hashes
}

func certsInDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var hashes []string
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		found := false
		for rest := data; ; {
			var block *pem.Block
			block, rest = pem.Decode(rest)
			if block == nil {
				break
			}
			if block.Type == "CERTIFICATE" {
				sum := sha1.Sum(block.Bytes)
				hashes = append(hashes, hex.EncodeToString(sum[:]))
				found = true
			}
		}
		if _, err := x509.ParseCertificate(data); !found && err == nil {
			sum := sha1.Sum(data)
			hashes = append(hashes, hex.EncodeToString(sum[:]))
		}
	}
	sort.Strings(hashes)
	return hashes
}

// certs lists the SHA-1 fingerprints of the certificates the store holds.
func (s throwawayStore) certs(t *testing.T) []string {
	t.Helper()
	switch runtime.GOOS {
	case "darwin":
		out, _ := runQuiet("security", "find-certificate", "-a", "-Z", s.value)
		return sha1Hashes(out)
	case "windows":
		out, _ := runQuiet("certutil", "-user", "-store", s.value)
		return sha1Hashes(out)
	}
	return certsInDir(t, s.value)
}

// contents is everything the store holds; on Linux each file's name and bytes too.
func (s throwawayStore) contents(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		return strings.Join(s.certs(t), "\n")
	}
	entries, err := os.ReadDir(s.value)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(s.value, e.Name()))
		sum := sha256.Sum256(data)
		lines = append(lines, e.Name()+" "+e.Type().String()+" "+hex.EncodeToString(sum[:]))
	}
	return strings.Join(lines, "\n")
}

// realTrustState reads, and only reads, the machine's own trust store, so the test sees
// that trust and untrust with a throwaway store left it alone.
func realTrustState() string {
	switch runtime.GOOS {
	case "darwin":
		user, _ := runQuiet("security", "dump-trust-settings")
		admin, _ := runQuiet("security", "dump-trust-settings", "-d")
		return user + "\n" + admin
	case "windows":
		root, _ := runQuiet("certutil", "-user", "-store", "Root")
		return strings.Join(sha1Hashes(root), "\n")
	}
	var lines []string
	for _, dir := range []string{"/usr/local/share/ca-certificates", "/etc/pki/ca-trust/source/anchors", "/etc/ca-certificates/trust-source/anchors"} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			lines = append(lines, filepath.Join(dir, e.Name()))
		}
	}
	return strings.Join(lines, "\n")
}

func (s throwawayStore) berth(t *testing.T, repo *Repo, args ...string) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, berthBin, args...)
	cmd.Dir = repo.Dir
	cmd.Env = append(repo.Env(), trustStoreVar+"="+s.value)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if ctx.Err() != nil {
		t.Fatalf("berth %v did not finish within a minute, perhaps waiting on a prompt it must never raise with %s set:\n%s%s", args, trustStoreVar, res.Stdout, res.Stderr)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.Exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("berth %v: %v", args, err)
	}
	return res
}

func (s throwawayStore) mustBerth(t *testing.T, repo *Repo, args ...string) Result {
	t.Helper()
	res := s.berth(t, repo, args...)
	if res.Exit != 0 {
		t.Fatalf("berth %v with %s=%s: exit %d\nstdout:\n%s\nstderr:\n%s", args, trustStoreVar, s.value, res.Exit, res.Stdout, res.Stderr)
	}
	return res
}

func TestProxyUntrustAfterTrustLeavesTheTrustStoreAsItWas(t *testing.T) {
	repo := NewRepo(t)
	store := newThrowawayStore(t)
	real := realTrustState()
	before := store.contents(t)

	store.mustBerth(t, repo, "proxy", "trust")
	t.Cleanup(func() { store.berth(t, repo, "proxy", "untrust") })
	ca := loadAuthority(t, repo.StateDir)
	sum := sha1.Sum(ca.cert.Raw)
	if held := store.certs(t); !slices.Contains(held, hex.EncodeToString(sum[:])) {
		t.Errorf("after berth proxy trust the throwaway store does not hold the authority %s; it holds %v", ca.certPath, held)
	}

	store.mustBerth(t, repo, "proxy", "untrust")
	if after := store.contents(t); after != before {
		t.Errorf("the throwaway store after trust and untrust differs from before:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if now := realTrustState(); now != real {
		t.Errorf("berth proxy trust or untrust with %s set changed the machine's own trust store:\nbefore:\n%s\nafter:\n%s", trustStoreVar, real, now)
	}
}

func TestProxyTrustPrintsHowToAddTheAuthorityToFirefox(t *testing.T) {
	repo := NewRepo(t)
	store := newThrowawayStore(t)

	res := store.mustBerth(t, repo, "proxy", "trust")
	t.Cleanup(func() { store.berth(t, repo, "proxy", "untrust") })
	ca := loadAuthority(t, repo.StateDir)
	out := res.Stdout + res.Stderr
	if !strings.Contains(out, "Firefox") || !strings.Contains(out, ca.certPath) {
		t.Errorf("berth proxy trust does not say how to add the authority %s to Firefox:\n%s", ca.certPath, out)
	}
}
