//go:build e2e

package e2e

import (
	"net"
	"strconv"
	"testing"
	"time"
)

func TestProxyCannotBeConnectedToOverHTTPSOnANonLoopbackAddress(t *testing.T) {
	ip := nonLoopbackAddress(t)
	_, p := startBareTLSProxy(t)

	addr := net.JoinHostPort(ip.String(), strconv.Itoa(p.httpsPort))
	if conn, err := net.DialTimeout("tcp", addr, 3*time.Second); err == nil {
		conn.Close()
		t.Errorf("a TCP connection to the proxy's https port on %s succeeded; it must listen on loopback only", addr)
	}
}
