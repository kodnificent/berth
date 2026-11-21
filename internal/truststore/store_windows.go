package truststore

import "strings"

const systemName = "your user's Trusted Root Certification Authorities store"

// certutil prints these codes in every language. CRYPT_E_NOT_FOUND is a store that holds
// no certificate matching the hash; NTE_NOT_FOUND is a named store that was never created,
// which holds nothing either.
const (
	cryptNotFound = "0x80092004"
	nteNotFound   = "0x80090011"
)

func storeName(s Store) string {
	if s.Throwaway != "" {
		return s.Throwaway
	}
	return "Root"
}

func notFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, cryptNotFound) || strings.Contains(msg, nteNotFound)
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
