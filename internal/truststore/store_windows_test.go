package truststore

import (
	"errors"
	"testing"
)

func TestNotFoundReadsBothWaysCertutilSaysACertificateIsAbsent(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"no error", nil, false},
		{"store without the certificate", errors.New("CertUtil: -store command FAILED: 0x80092004 (-2146885628 CRYPT_E_NOT_FOUND)"), true},
		{"store that was never created", errors.New("CertUtil: -store command FAILED: 0x80090011 (-2146893807 NTE_NOT_FOUND)"), true},
		{"access denied", errors.New("CertUtil: -store command FAILED: 0x80070005 (WIN32: 5 ERROR_ACCESS_DENIED)"), false},
	}
	for _, c := range cases {
		if got := notFound(c.err); got != c.want {
			t.Errorf("%s: notFound = %v, want %v", c.name, got, c.want)
		}
	}
}
