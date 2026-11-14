package truststore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
)

const systemName = "the system's CA certificates"

// anchor is a distribution's directory of local CA certificates and the command that
// rebuilds the system's bundle from it. Each bundle trusts its anchors for every purpose:
// none of these directories takes a narrower trust, so the authority's own limits are
// what narrow it.
type anchor struct {
	dir    string
	update []string
}

var anchors = []anchor{
	{"/usr/local/share/ca-certificates", []string{"update-ca-certificates"}},           // Debian, Ubuntu, Alpine
	{"/etc/pki/ca-trust/source/anchors", []string{"update-ca-trust", "extract"}},       // Fedora, RHEL
	{"/etc/ca-certificates/trust-source/anchors", []string{"trust", "extract-compat"}}, // Arch
	{"/usr/share/pki/trust/anchors", []string{"update-ca-certificates"}},               // openSUSE
}

func systemAnchor() (anchor, error) {
	for _, a := range anchors {
		if info, err := os.Stat(a.dir); err != nil || !info.IsDir() {
			continue
		}
		if _, err := exec.LookPath(a.update[0]); err == nil {
			return a, nil
		}
	}
	return anchor{}, errors.New("berth found no CA certificate directory it knows, with its update command, on this system; add the authority to the system's trust store as its documentation says")
}

func anchorOf(path string) (anchor, bool) {
	for _, a := range anchors {
		if filepath.Dir(path) == a.dir {
			return a, true
		}
	}
	return anchor{}, false
}

// storeDirs are the only directories berth writes into, or deletes from, for s.
func storeDirs(s Store) []string {
	if s.Throwaway != "" {
		return []string{s.Throwaway}
	}
	var dirs []string
	for _, a := range anchors {
		dirs = append(dirs, a.dir)
	}
	return dirs
}

// asRoot runs a command as root through sudo, whose own prompt asks for the password on
// the terminal, unless berth already is root. stdin, when set, is the command's input.
var asRoot = func(stdin []byte, args ...string) error {
	if os.Geteuid() != 0 {
		args = append([]string{"sudo"}, args...)
	}
	_, err := run(stdin, args[0], args[1:]...)
	return err
}

var platform = ops{
	file: func(s Store, cert Cert) (string, error) {
		if s.Throwaway != "" {
			return filepath.Join(s.Throwaway, fileName(cert)), nil
		}
		a, err := systemAnchor()
		if err != nil {
			return "", err
		}
		return filepath.Join(a.dir, fileName(cert)), nil
	},
	holds: func(s Store, e Entry) (presence, error) {
		if err := checkFile(e.File, storeDirs(s)...); err != nil {
			return absent, err
		}
		if _, err := os.Lstat(e.File); errors.Is(err, fs.ErrNotExist) {
			return absent, nil
		} else if err != nil {
			return absent, err
		}
		own, err := ownFile(e.File, e)
		if err != nil {
			return absent, err
		}
		if own {
			return held, nil
		}
		return foreign, nil
	},
	// The certificate reaches root's install on its standard input, so no file the user
	// can write stands between berth's check and the store. What install wrote is checked
	// before the bundle is rebuilt, and removed unrebuilt when it isn't berth's
	// certificate; the path was free just before, so whatever is there is berth's doing.
	// The bundle is rebuilt on every add, so a rebuild that failed after the copy is done
	// by the next trust.
	add: func(s Store, e Entry, pemData []byte, held bool, placing func() error) error {
		if err := checkFile(e.File, storeDirs(s)...); err != nil {
			return err
		}
		if !held {
			if _, err := os.Lstat(e.File); !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("%s is there and does not hold berth's certificate, and berth never writes over a file it did not write", e.File)
			}
			if err := placing(); err != nil {
				return err
			}
		}
		if s.Throwaway != "" {
			if held {
				return nil
			}
			return writeNew(e.File, pemData)
		}
		a, _ := anchorOf(e.File)
		if !held {
			if err := asRoot(pemData, "install", "-m", "0644", "/dev/stdin", e.File); err != nil {
				return err
			}
			if own, err := ownFile(e.File, e); err != nil || !own {
				rmErr := asRoot(nil, "rm", "-f", e.File)
				return errors.Join(err, rmErr, fmt.Errorf("%s did not hold berth's certificate after install, so berth removed it and did not rebuild the system's bundle", e.File))
			}
		}
		return asRoot(nil, a.update...)
	},
	// The certificate goes only from a store directory, named as berth names its file and
	// holding the recorded certificate; a foreign file goes only when berth placed it,
	// which Untrust and Trust check. The system's bundle is rebuilt every time, so a
	// rebuild that failed after the delete is done by the next untrust.
	remove: func(s Store, e Entry, p presence) error {
		if err := checkFile(e.File, storeDirs(s)...); err != nil {
			return err
		}
		if p == held {
			if own, err := ownFile(e.File, e); err != nil || !own {
				return errors.Join(err, fmt.Errorf("%s no longer holds berth's certificate, so berth won't delete it", e.File))
			}
		}
		if s.Throwaway != "" {
			if p == absent {
				return nil
			}
			return os.Remove(e.File)
		}
		a, _ := anchorOf(e.File)
		if p != absent {
			if err := asRoot(nil, "rm", "-f", e.File); err != nil {
				return err
			}
		}
		return asRoot(nil, a.update...)
	},
}

func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}
