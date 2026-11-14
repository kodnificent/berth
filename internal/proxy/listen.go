package proxy

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/ports"
)

// Listen opens port on the loopback addresses only, so nothing on another interface can
// reach the proxy. The IPv6 loopback is skipped where the system has none. userConfig
// names the file whose [proxy] http picks another port.
func Listen(port int, userConfig string) ([]net.Listener, error) {
	return ListenFor("http", port, userConfig)
}

// ListenFor is Listen for the port [proxy] key sets, http or https.
func ListenFor(key string, port int, userConfig string) ([]net.Listener, error) {
	// macOS and Windows let a loopback bind through while another process holds the port
	// on the wildcard address, and the proxy would then take that process's local traffic.
	free := ports.Free(port)
	ls, err := listenLoopback(key, port, userConfig)
	if err == nil && !free {
		for _, l := range ls {
			l.Close()
		}
		return nil, taken(key, port, userConfig)
	}
	return ls, err
}

func listenLoopback(key string, port int, userConfig string) ([]net.Listener, error) {
	v4, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, listenError(key, "127.0.0.1", port, err, userConfig)
	}
	v6, err := net.Listen("tcp6", net.JoinHostPort("::1", strconv.Itoa(port)))
	switch {
	case err == nil:
		return []net.Listener{v4, v6}, nil
	case refusal(err) != "":
		v4.Close()
		return nil, listenError(key, "::1", port, err, userConfig)
	default:
		return []net.Listener{v4}, nil
	}
}

func taken(key string, port int, userConfig string) error {
	return apperr.New(apperr.Generic, "port %d is taken by another process, so berth proxy can't listen on it", port).
		WithNext(whoHolds(runtime.GOOS, port), fmt.Sprintf("stop it, or set [proxy] %s in %s to a free port", key, userConfig))
}

func listenError(key, addr string, port int, err error, userConfig string) error {
	switch refusal(err) {
	case inUse:
		return taken(key, port, userConfig)
	case denied:
		cause := "the system keeps port %d on %s for administrators"
		if runtime.GOOS == "windows" {
			cause = "Windows refuses port %d on %s"
		}
		return apperr.New(apperr.Generic, cause+", so berth proxy can't listen there", port, addr).
			WithNext(append(allowLowPort(runtime.GOOS, port, executable(), sudoEnv()), fmt.Sprintf("or set [proxy] %s in %s to a port above 1023, which the URLs then carry", key, userConfig))...)
	}
	return apperr.New(apperr.Generic, "berth proxy can't listen on port %d on %s: %v", port, addr, err)
}

const (
	inUse  = "in_use"
	denied = "denied"
)

func whoHolds(goos string, port int) string {
	if goos == "windows" {
		return fmt.Sprintf("netstat -ano -p tcp | findstr :%d names the process holding it", port)
	}
	return fmt.Sprintf("lsof -nP -iTCP:%d -sTCP:LISTEN names the process holding it", port)
}

// allowLowPort is how a person lets berth listen on a low port on goos. Berth never runs
// these itself: each one changes the system, and berth never raises its own privileges.
// env is what a proxy run with sudo must be given to find the same registry and config.
func allowLowPort(goos string, port int, berth string, env []string) []string {
	switch goos {
	case "linux":
		start := "1024"
		if data, err := os.ReadFile("/proc/sys/net/ipv4/ip_unprivileged_port_start"); err == nil {
			start = strings.TrimSpace(string(data))
		}
		return []string{
			fmt.Sprintf("allow ports from %d up until the next boot: sudo sysctl -w net.ipv4.ip_unprivileged_port_start=%d (it is %s here; a file in /etc/sysctl.d keeps it)", port, port, start),
			fmt.Sprintf("or let this berth binary alone bind low ports, until it is next installed: sudo setcap cap_net_bind_service=+ep %s", shellQuote(berth)),
		}
	case "darwin":
		return []string{
			fmt.Sprintf("run it as an administrator, keeping the variables that find your berths: sudo env %s proxy", strings.Join(append(env, shellQuote(berth)), " ")),
		}
	case "windows":
		return []string{
			fmt.Sprintf("Windows refuses port %d when it is in a range the system reserves, often for Hyper-V or WSL, or when another program holds it exclusively; netsh interface ipv4 show excludedportrange protocol=tcp lists the reserved ranges, and %s", port, whoHolds(goos, port)),
		}
	}
	return []string{fmt.Sprintf("run the proxy as an administrator, or allow port %d for your user as the system documents", port)}
}

// sudoEnv is HOME and each variable that moves berth's state or config, as they are now:
// sudo resets the environment, and the proxy must read the registry they point at.
func sudoEnv() []string {
	var out []string
	for _, k := range []string{"HOME", "XDG_STATE_HOME", "XDG_CONFIG_HOME", "BERTH_STATE_DIR"} {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			out = append(out, k+"="+shellQuote(v))
		}
	}
	return out
}

func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-=:+,@%") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func executable() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return "berth"
}

// LoopbackOnly closes every connection from a peer that isn't loopback before anything is
// read from it, so a system routing other peers to 127.0.0.1 gets no TLS handshake, and
// no certificate, from the proxy either.
func LoopbackOnly(l net.Listener) net.Listener { return loopbackOnly{l} }

type loopbackOnly struct{ net.Listener }

func (l loopbackOnly) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if addr, ok := c.RemoteAddr().(*net.TCPAddr); ok && addr.IP.IsLoopback() {
			return c, nil
		}
		c.Close()
	}
}
