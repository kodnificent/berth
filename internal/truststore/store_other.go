//go:build !darwin && !linux && !windows

package truststore

import "errors"

const systemName = "the system's trust store"

var errUnknown = errors.New("berth doesn't know this system's trust store; add the authority to it as the system's documentation says")

var platform = ops{
	file:   func(Store, Cert) (string, error) { return "", errUnknown },
	holds:  func(Store, Entry) (presence, error) { return absent, errUnknown },
	add:    func(Store, Entry, []byte, bool, func() error) error { return errUnknown },
	remove: func(Store, Entry, presence) error { return errUnknown },
}
