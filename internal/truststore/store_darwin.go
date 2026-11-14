package truststore

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const systemName = "your login keychain"

var sha256Line = regexp.MustCompile(`(?m)^SHA-256 hash:\s*([0-9A-Fa-f]+)`)

var platform = ops{
	file: func(Store, Cert) (string, error) { return "", nil },
	holds: func(s Store, e Entry) (presence, error) {
		kc, err := keychain(s)
		if err != nil {
			return absent, err
		}
		out, err := run(nil, "security", "find-certificate", "-a", "-Z", kc)
		if err != nil {
			return absent, err
		}
		var fingerprints []string
		for _, m := range sha256Line.FindAllStringSubmatch(out, -1) {
			fingerprints = append(fingerprints, strings.ToLower(m[1]))
		}
		if slices.Contains(fingerprints, e.SHA256) {
			return held, nil
		}
		return absent, nil
	},
	// The throwaway keychain gets the certificate alone, with no trust settings. The login
	// keychain gets it trusted as a root for SSL only (security's ssl policy, which is
	// server authentication), which macOS asks the user to allow in its own dialog.
	// security reads only a file, so it gets a private copy; see withPrivateCopy.
	add: func(s Store, e Entry, pemData []byte, isHeld bool, _ func() error) error {
		if s.Throwaway != "" && isHeld {
			return nil
		}
		kc, err := keychain(s)
		if err != nil {
			return err
		}
		return withPrivateCopy(pemData, func(path string) error {
			if s.Throwaway != "" {
				_, err := run(nil, "security", "add-certificates", "-k", kc, path)
				return err
			}
			_, err := run(nil, "security", "add-trusted-cert", "-r", "trustRoot", "-p", "ssl", "-k", kc, path)
			return err
		})
	},
	// From the login keychain its trust settings go too, which macOS asks the user to
	// allow.
	remove: func(s Store, e Entry, p presence) error {
		if p != held {
			return nil
		}
		kc, err := keychain(s)
		if err != nil {
			return err
		}
		args := []string{"delete-certificate", "-Z", strings.ToUpper(e.SHA256)}
		if s.Throwaway == "" {
			args = append(args, "-t")
		}
		_, err = run(nil, "security", append(args, kc)...)
		return err
	},
}

func keychain(s Store) (string, error) {
	if s.Throwaway != "" {
		return s.Throwaway, nil
	}
	out, err := run(nil, "security", "login-keychain")
	if err != nil {
		return "", err
	}
	if p := strings.Trim(strings.TrimSpace(out), `"`); filepath.IsAbs(p) {
		return p, nil
	}
	return "", fmt.Errorf("security login-keychain names no keychain: %s", strings.TrimSpace(out))
}
