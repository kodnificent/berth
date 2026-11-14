package truststore

import (
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func dirStore(dir string) Store { return Store{Throwaway: dir} }

func TestUntrustAfterTrustLeavesADirectoryStoreAsItWas(t *testing.T) {
	state, dir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "other.crt"), []byte("not berth's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := dirStore(dir)

	if added, err := s.Trust(state, testCert(t)); err != nil || !added {
		t.Fatalf("trust: added %v, %v", added, err)
	}
	if removed, _, err := s.Untrust(state); err != nil || removed != 1 {
		t.Fatalf("untrust: removed %d, %v; want 1", removed, err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "other.crt" {
		t.Errorf("the store after trust and untrust holds %v, want only other.crt", entries)
	}
}

func TestUntrustKeepsTheRecordOfAFileAtItsPathThatIsNotBerths(t *testing.T) {
	state, dir := t.TempDir(), t.TempDir()
	s := dirStore(dir)
	cert := testCert(t)
	if _, err := s.Trust(state, cert); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fileName(cert))
	if err := os.WriteFile(path, []byte("someone else's now\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, gone, err := s.Untrust(state)

	if err == nil || !strings.Contains(err.Error(), path) || gone != 0 {
		t.Errorf("untrust with %s holding something else: gone %d, %v; want a refusal naming the path", path, gone, err)
	}
	if list, _ := Recorded(state); len(list) != 1 {
		t.Errorf("the record after untrust is %v, want it kept", list)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "someone else's now\n" {
		t.Errorf("untrust deleted or changed %s: %q, %v", path, data, err)
	}
}

// The directory store's add writes the file and then fails, as the system store's
// rebuild can after the copy.
func TestATrustWhoseStepAfterTheCopyFailsIsFinishedByTheNextTrust(t *testing.T) {
	state, dir, cert := t.TempDir(), t.TempDir(), testCert(t)
	failing := platform
	failing.add = func(s Store, e Entry, pemData []byte, held bool, placing func() error) error {
		if err := platform.add(s, e, pemData, held, placing); err != nil {
			return err
		}
		return errors.New("update-ca-certificates failed")
	}
	if _, err := (Store{Throwaway: dir, ops: &failing}).Trust(state, cert); err == nil {
		t.Fatal("trust whose update step fails succeeded")
	}

	added, err := dirStore(dir).Trust(state, cert)

	if trusted, _ := dirStore(dir).Trusts(state, cert); err != nil || !added || !trusted {
		t.Errorf("trust after a failed one: added %v, %v, trusted %v; want it finished, not refused", added, err, trusted)
	}
}

func TestUntrustRefusesARecordedFileOutsideTheStore(t *testing.T) {
	state, dir, elsewhere := t.TempDir(), t.TempDir(), t.TempDir()
	cert := testCert(t)
	s := dirStore(dir)
	if _, err := s.Trust(state, cert); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(elsewhere, fileName(cert))
	data, _ := os.ReadFile(filepath.Join(dir, fileName(cert)))
	if err := os.WriteFile(victim, data, 0o644); err != nil {
		t.Fatal(err)
	}
	list, _ := Recorded(state)
	list[0].File = victim
	raw, _ := json.Marshal(list)
	if err := os.WriteFile(recordPath(state), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.Untrust(state); err == nil {
		t.Errorf("untrust with the record naming %s, outside the store, succeeded", victim)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("untrust touched %s, outside the store: %v", victim, err)
	}
}

// fakeRoot points the system's store at a directory of the test's and runs what berth
// would run as root as this user instead, recording each command. before runs ahead of
// each, as a process of the same user acting while sudo waits for the password would.
type fakeRoot struct {
	dir      string
	calls    []string
	before   func(args []string)
	install  func(dest string, data []byte) error
	failRmOn int
}

func newFakeRoot(t *testing.T) *fakeRoot {
	t.Helper()
	f := &fakeRoot{dir: t.TempDir()}
	f.install = func(dest string, data []byte) error { return os.WriteFile(dest, data, 0o644) }
	savedAnchors, savedRoot := anchors, asRoot
	t.Cleanup(func() { anchors, asRoot = savedAnchors, savedRoot })
	anchors = []anchor{{f.dir, []string{"true"}}}
	asRoot = func(stdin []byte, args ...string) error {
		f.calls = append(f.calls, strings.Join(args, " "))
		if f.before != nil {
			f.before(args)
		}
		switch args[0] {
		case "install":
			src, dest := args[3], args[4]
			data := stdin
			if src != "/dev/stdin" {
				var err error
				if data, err = os.ReadFile(src); err != nil {
					return err
				}
			}
			return f.install(dest, data)
		case "rm":
			if f.failRmOn > 0 {
				f.failRmOn--
				return errors.New("sudo: a password is required")
			}
			return os.Remove(args[2])
		}
		return nil
	}
	return f
}

func (f *fakeRoot) rebuilt() bool { return slices.Contains(f.calls, "true") }

func otherPEM(t *testing.T) []byte {
	t.Helper()
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: testCert(t).DER})
}

func TestAToolStepThatRewritesWhatItIsGivenCannotChangeWhatLandsInTheStore(t *testing.T) {
	f := newFakeRoot(t)
	attacker := otherPEM(t)
	f.before = func(args []string) {
		for _, a := range args {
			if info, err := os.Stat(a); err == nil && info.Mode().IsRegular() && filepath.Dir(a) != f.dir {
				os.WriteFile(a, attacker, 0o600)
			}
		}
	}
	state, cert := t.TempDir(), testCert(t)

	if _, err := (Store{}).Trust(state, cert); err != nil {
		t.Fatalf("trust: %v", err)
	}

	if own, _ := ownFile(filepath.Join(f.dir, fileName(cert)), Entry{SHA256: cert.SHA256()}); !own {
		t.Errorf("the store holds something other than berth's certificate after a rewrite of what the tool was given")
	}
}

func TestAMismatchAfterInstallIsRemovedAndTheBundleNotRebuilt(t *testing.T) {
	f := newFakeRoot(t)
	attacker := otherPEM(t)
	f.install = func(dest string, _ []byte) error { return os.WriteFile(dest, attacker, 0o644) }
	state, cert := t.TempDir(), testCert(t)

	_, err := (Store{}).Trust(state, cert)

	if err == nil {
		t.Fatal("trust that installed other content succeeded")
	}
	if _, statErr := os.Stat(filepath.Join(f.dir, fileName(cert))); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("the mismatched file is still in the store: %v", statErr)
	}
	if f.rebuilt() {
		t.Errorf("the system's bundle was rebuilt with the mismatched file: %v", f.calls)
	}
}

func TestAfterAFailedCheckTheRecordStaysAndUntrustRemovesWhatBerthPlaced(t *testing.T) {
	f := newFakeRoot(t)
	attacker := otherPEM(t)
	f.install = func(dest string, _ []byte) error { return os.WriteFile(dest, attacker, 0o644) }
	f.failRmOn = 1
	state, cert := t.TempDir(), testCert(t)
	if _, err := (Store{}).Trust(state, cert); err == nil {
		t.Fatal("trust that installed other content succeeded")
	}
	if list, _ := Recorded(state); len(list) != 1 {
		t.Fatalf("the record after the failed check is %v, want it kept", list)
	}

	removed, gone, err := (Store{}).Untrust(state)

	path := filepath.Join(f.dir, fileName(cert))
	if _, statErr := os.Stat(path); err != nil || removed != 1 || gone != 0 || !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("untrust after the failed check: removed %d, gone %d, %v, %s %v; want what berth placed removed", removed, gone, err, path, statErr)
	}
	if list, _ := Recorded(state); len(list) != 0 {
		t.Errorf("the record after untrust is %v, want it gone", list)
	}
}

func TestTrustAgainRepairsAfterAFailedCheck(t *testing.T) {
	f := newFakeRoot(t)
	attacker := otherPEM(t)
	honest := f.install
	f.install = func(dest string, _ []byte) error { return os.WriteFile(dest, attacker, 0o644) }
	f.failRmOn = 1
	state, cert := t.TempDir(), testCert(t)
	if _, err := (Store{}).Trust(state, cert); err == nil {
		t.Fatal("trust that installed other content succeeded")
	}
	f.install = honest

	added, err := (Store{}).Trust(state, cert)

	if trusted, _ := (Store{}).Trusts(state, cert); err != nil || !added || !trusted {
		t.Errorf("trust again: added %v, %v, trusted %v; want it repaired", added, err, trusted)
	}
}
