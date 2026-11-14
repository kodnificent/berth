package truststore

import "strings"

const systemName = "your user's Trusted Root Certification Authorities store"

// cryptNotFound is CRYPT_E_NOT_FOUND, which certutil prints in every language when the
// store holds no certificate matching the hash.
const cryptNotFound = "0x80092004"

func storeName(s Store) string {
	if s.Throwaway != "" {
		return s.Throwaway
	}
	return "Root"
}

func notFound(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), cryptNotFound)
}

// certutil finds a certificate by its SHA-1 hash, the only fingerprint it takes.
var platform = ops{
	file: func(Store, Cert) (string, error) { return "", nil },
	holds: func(s Store, e Entry) (presence, error) {
		_, err := run(nil, "certutil", "-user", "-store", storeName(s), e.SHA1)
		if notFound(err) {
			return absent, nil
		}
		if err != nil {
			return absent, err
		}
		return held, nil
	},
	// Root, for the system's store, has Windows ask the user to allow it in its own
	// dialog. A Root certificate is trusted for every purpose its own extended key usage
	// allows, so the authority's server-only usage is what narrows it.
	// certutil reads only a file, so it gets a private copy; see withPrivateCopy.
	add: func(s Store, e Entry, pemData []byte, _ bool, _ func() error) error {
		return withPrivateCopy(pemData, func(path string) error {
			_, err := run(nil, "certutil", "-user", "-addstore", storeName(s), path)
			return err
		})
	},
	remove: func(s Store, e Entry, p presence) error {
		if p != held {
			return nil
		}
		_, err := run(nil, "certutil", "-user", "-delstore", storeName(s), e.SHA1)
		if notFound(err) {
			return nil
		}
		return err
	},
}
