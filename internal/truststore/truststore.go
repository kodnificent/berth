// Package truststore adds berth proxy's certificate authority to the system's trust
// store through the system's own tool, and removes only the certificate berth recorded
// adding, matched by fingerprint.
package truststore

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kodnificent/berth/internal/safefile"
)

// Var names a throwaway store berth uses in place of the system's: a keychain file on
// macOS, a directory standing in for the system's CA directory on Linux, a CurrentUser
// certificate store's name on Windows. With it set, trust and untrust touch only that
// store, raise no prompt and need no administrator.
const Var = "BERTH_TRUST_STORE"

// Store is the trust store trust and untrust use.
type Store struct {
	// Throwaway is Var's value, empty for the system's own store.
	Throwaway string
	// ops replaces the system's tools, for tests.
	ops *ops
}

func FromEnv() Store { return Store{Throwaway: os.Getenv(Var)} }

func (s Store) String() string {
	if s.Throwaway != "" {
		return fmt.Sprintf("the store %s names (%s)", Var, s.Throwaway)
	}
	return systemName
}

// presence is what a store holds where berth records its certificate.
type presence int

const (
	// absent: nothing of berth's there.
	absent presence = iota
	// held: the certificate with the recorded fingerprint.
	held
	// foreign: something else at berth's recorded path, in a directory store.
	foreign
)

// ops is how a system's store is worked. Each step is safe to run again after a failure.
type ops struct {
	// file is where a directory store keeps cert, empty for any other store.
	file func(s Store, cert Cert) (string, error)
	// holds reports what the store holds where e records berth's certificate.
	holds func(s Store, e Entry) (presence, error)
	// add adds the PEM certificate pem, the bytes berth checked; held says the store
	// already holds it, as after a step that failed later. A directory store calls
	// placing once it knows its path is free and before it writes there.
	add func(s Store, e Entry, pem []byte, held bool, placing func() error) error
	// remove takes out what p says is there: the certificate, or, for a directory store,
	// a file berth placed whose content is not it.
	remove func(s Store, e Entry, p presence) error
}

func (s Store) tools() *ops {
	if s.ops != nil {
		return s.ops
	}
	return &platform
}

// Cert is the authority's certificate as trust adds it.
type Cert struct {
	DER []byte
}

func (c Cert) SHA1() string {
	sum := sha1.Sum(c.DER)
	return hex.EncodeToString(sum[:])
}

func (c Cert) SHA256() string {
	sum := sha256.Sum256(c.DER)
	return hex.EncodeToString(sum[:])
}

// Entry is one certificate berth added, or set out to add, to a store. Berth matches it by
// SHA-256; SHA-1 is kept only for the Windows tool, which takes nothing else.
type Entry struct {
	Store  string `json:"store"`
	SHA256 string `json:"sha256"`
	SHA1   string `json:"sha1,omitempty"`
	// File is the file berth writes into the store, where the store is a directory.
	File string `json:"file,omitempty"`
	// Adding is set before berth adds the certificate and cleared once the store is seen
	// to hold it, so a trust that fails part way still leaves a record to finish or undo.
	Adding bool `json:"adding,omitempty"`
	// Placed is set just before berth writes into a directory store, after seeing the
	// path free, so whatever lands there is berth's to remove, even content that isn't
	// berth's certificate. It is cleared once the store is seen to hold the certificate.
	Placed bool `json:"placed,omitempty"`
	// Removing is set before berth removes it, so an untrust that fails part way is
	// finished by the next one.
	Removing bool `json:"removing,omitempty"`
}

func recordPath(state string) string { return filepath.Join(state, "proxy", "trusted.json") }

// Recorded is every certificate berth recorded adding, to any store.
func Recorded(state string) ([]Entry, error) {
	data, err := os.ReadFile(recordPath(state))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Entry
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("reading %s: %w", recordPath(state), err)
	}
	return list, nil
}

func save(state string, list []Entry) error {
	if len(list) == 0 {
		if err := os.Remove(recordPath(state)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return safefile.Write(recordPath(state), append(data, '\n'), 0o644)
}

// Trusts reports whether berth recorded adding cert to s, and saw the store hold it.
func (s Store) Trusts(state string, cert Cert) (bool, error) {
	list, err := Recorded(state)
	if err != nil {
		return false, err
	}
	for _, e := range list {
		if e.Store == s.Throwaway && e.SHA256 == cert.SHA256() && !e.Adding {
			return true, nil
		}
	}
	return false, nil
}

// Trust adds cert to s. It records what it is about to add first, so whatever fails after
// leaves a record untrust can act on, and running it again repairs what was left. It
// then checks the store holds the certificate. added is false when berth had already
// added it and the store still holds it.
func (s Store) Trust(state string, cert Cert) (added bool, err error) {
	list, err := Recorded(state)
	if err != nil {
		return false, err
	}
	i := -1
	for j, e := range list {
		if e.Store == s.Throwaway && e.SHA256 == cert.SHA256() {
			i = j
		}
	}
	tools := s.tools()
	if i < 0 {
		file, err := tools.file(s, cert)
		if err != nil {
			return false, err
		}
		list = append(list, Entry{Store: s.Throwaway, SHA256: cert.SHA256(), SHA1: cert.SHA1(), File: file, Adding: true})
		i = len(list) - 1
		if err := save(state, list); err != nil {
			return false, err
		}
	}
	e := &list[i]
	p, err := tools.holds(s, *e)
	if err != nil {
		return false, err
	}
	if p == held && !e.Adding && !e.Removing {
		return false, nil
	}
	if p == foreign {
		if !e.Placed {
			return false, notBerths(e.File)
		}
		if err := tools.remove(s, *e, foreign); err != nil {
			return false, err
		}
		p = absent
	}
	e.Adding, e.Removing = true, false
	if err := save(state, list); err != nil {
		return false, err
	}
	placing := func() error {
		e.Placed = true
		return save(state, list)
	}
	if err := tools.add(s, *e, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.DER}), p == held, placing); err != nil {
		return false, fmt.Errorf("%w; berth keeps its record of what it set out to add, so berth proxy untrust removes whatever it put there and berth proxy trust again repairs it", err)
	}
	if p, err = tools.holds(s, *e); err != nil {
		return false, err
	}
	if p != held {
		return false, fmt.Errorf("the store does not hold the certificate with SHA-256 %s after berth added it; berth keeps its record, so berth proxy untrust removes whatever it put there and berth proxy trust again repairs it", e.SHA256)
	}
	e.Adding, e.Placed = false, false
	return true, save(state, list)
}

func notBerths(path string) error {
	return fmt.Errorf("%s holds a file that is not berth's certificate and that berth did not write, so berth keeps its record and leaves the file alone; look at it, and delete it if it should not be there", path)
}

// Untrust removes from s every certificate berth recorded adding there, by fingerprint,
// and forgets it. removed counts those it took out; gone those whose recorded place
// holds nothing of berth's, as when removed elsewhere. Neither is an error. A file at
// berth's recorded path holding something berth did not put there is: berth keeps its
// record and names the path.
func (s Store) Untrust(state string) (removed, gone int, err error) {
	list, err := Recorded(state)
	if err != nil {
		return 0, 0, err
	}
	tools := s.tools()
	var kept []Entry
	for i, e := range list {
		if e.Store != s.Throwaway {
			kept = append(kept, e)
			continue
		}
		p, err := tools.holds(s, e)
		if err == nil && p == foreign && !e.Placed {
			err = notBerths(e.File)
		}
		if err != nil {
			return removed, gone, errors.Join(err, save(state, append(kept, list[i:]...)))
		}
		if p == absent && !e.Removing {
			gone++
			continue
		}
		list[i].Removing = true
		if err := save(state, append(append([]Entry{}, kept...), list[i:]...)); err != nil {
			return removed, gone, err
		}
		if err := tools.remove(s, list[i], p); err != nil {
			return removed, gone, errors.Join(err, save(state, append(kept, list[i:]...)))
		}
		removed++
	}
	return removed, gone, save(state, kept)
}

// run runs a store's tool with stdin as its standard input, never berth's own: a prompt
// the system raises comes from the system, on its own window or terminal.
func run(stdin []byte, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(context.Background(), name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}

// pemCerts is the DER of each PEM certificate in data.
func pemCerts(data []byte) [][]byte {
	var out [][]byte
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return out
		}
		if block.Type == "CERTIFICATE" {
			out = append(out, block.Bytes)
		}
	}
}

// fileName is the name berth gives its certificate in a directory store, carrying its
// fingerprint, so it never takes the name of a file already there.
func fileName(cert Cert) string { return "berth-proxy-" + cert.SHA256()[:16] + ".crt" }

var ownName = regexp.MustCompile(`^berth-proxy-[0-9a-f]{16}\.crt$`)

// checkFile refuses a recorded file that is not in one of dirs or not named as berth
// names its files, since berth deletes it, as root on a system store.
func checkFile(path string, dirs ...string) error {
	dir, base := filepath.Dir(path), filepath.Base(path)
	for _, d := range dirs {
		if d != "" && filepath.Clean(d) == dir && ownName.MatchString(base) {
			return nil
		}
	}
	return fmt.Errorf("the record names %s, which is not a file berth writes into the store, so berth won't touch it", path)
}

// ownFile reports whether the file at path holds the certificate e names and nothing
// else, so berth never deletes a file it did not write.
func ownFile(path string, e Entry) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	certs := pemCerts(data)
	if len(certs) != 1 {
		return false, nil
	}
	sum := sha256.Sum256(certs[0])
	return hex.EncodeToString(sum[:]) == e.SHA256, nil
}
