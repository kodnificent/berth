// Package redis talks to a Redis server on the host over RESP: it finds keys under a
// prefix and deletes them, on every logical database. It never keeps a login, and its
// errors never carry one.
package redis

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kodnificent/berth/internal/hostdb"
)

const (
	DefaultPort    = 6379
	connectTimeout = 10 * time.Second
	commandTimeout = time.Minute
	scanCount      = "1000"
	// maxSelect bounds how far berth selects upward to find the logical databases when the
	// server answers neither CONFIG nor INFO.
	maxSelect = 1 << 16
)

// Location is where a .env sets the server and login: keys under Prefix, such as REDIS_.
type Location struct{ Prefix string }

// Server reads the server in env. missing names the keys a server needs that env
// doesn't set.
func (l Location) Server(env map[string]string) (s hostdb.Server, missing []string, err error) {
	s = hostdb.Server{Host: env[l.Prefix+"HOST"], Port: DefaultPort}
	if s.Host == "" {
		missing = append(missing, l.Prefix+"HOST")
	}
	if p := env[l.Prefix+"PORT"]; p != "" {
		n, convErr := strconv.Atoi(p)
		if convErr != nil || n < 1 || n > 65535 {
			return s, missing, fmt.Errorf("%sPORT is %q, which is not a port", l.Prefix, p)
		}
		s.Port = n
	}
	return s, missing, nil
}

// Login reads the login from the first env that sets a username or a password. A server
// without one needs neither, so no login is not an error.
func (l Location) Login(envs ...map[string]string) hostdb.Login {
	for _, env := range envs {
		user, hasUser := env[l.Prefix+"USERNAME"]
		pw, hasPassword := env[l.Prefix+"PASSWORD"]
		if hasUser || hasPassword {
			return hostdb.Login{User: user, Password: pw}
		}
	}
	return hostdb.Login{}
}

// ServerError is an error reply from the server, such as NOAUTH or an unknown command.
type ServerError string

func (e ServerError) Error() string { return string(e) }

// Conn is an open connection to one server. It is used from one goroutine at a time.
type Conn struct {
	nc       net.Conn
	r        *bufio.Reader
	w        *bufio.Writer
	server   hostdb.Server
	login    hostdb.Login
	selected int
	dbs      []int
}

// Open connects to s and logs in when login has a password.
func Open(s hostdb.Server, login hostdb.Login) (*Conn, error) {
	nc, err := net.DialTimeout("tcp", s.String(), connectTimeout)
	if err != nil {
		return nil, fmt.Errorf("the Redis server at %s can't be reached: %s", s, hostdb.Scrub(err, login))
	}
	c := &Conn{nc: nc, r: bufio.NewReader(nc), w: bufio.NewWriter(nc), server: s, login: login}
	if login.Password != "" {
		args := []string{"AUTH", login.Password}
		if login.User != "" {
			args = []string{"AUTH", login.User, login.Password}
		}
		if _, err := c.do(args...); err != nil && !noPasswordSet(err) {
			nc.Close()
			return nil, fmt.Errorf("the Redis server at %s refused the login: %s", s, hostdb.Scrub(err, login))
		}
	}
	if _, err := c.do("PING"); err != nil {
		nc.Close()
		return nil, c.wrap(err)
	}
	return c, nil
}

// noPasswordSet reports whether the server answered AUTH by saying it has no password,
// which apps tolerate when their .env sets one anyway. The PING after it still has to
// succeed, so a server that does need a login is never passed.
func noPasswordSet(err error) bool {
	var refused ServerError
	if !errors.As(err, &refused) {
		return false
	}
	msg := strings.ToLower(string(refused))
	return strings.Contains(msg, "called without any password configured") || strings.Contains(msg, "but no password is set")
}

func (c *Conn) Close() error { return c.nc.Close() }

func (c *Conn) wrap(err error) error {
	return fmt.Errorf("the Redis server at %s: %s", c.server, hostdb.Scrub(err, c.login))
}

func (c *Conn) do(args ...string) (reply, error) {
	b := make([][]byte, len(args))
	for i, a := range args {
		b[i] = []byte(a)
	}
	return c.doBytes(b...)
}

func (c *Conn) doBytes(args ...[]byte) (reply, error) {
	if err := c.nc.SetDeadline(time.Now().Add(commandTimeout)); err != nil {
		return reply{}, err
	}
	if err := writeCommand(c.w, args...); err != nil {
		return reply{}, err
	}
	rep, err := readReply(c.r)
	if err != nil {
		return reply{}, err
	}
	if rep.Err != "" {
		return rep, ServerError(rep.Err)
	}
	return rep, nil
}

// Databases are the logical databases to look in: every one the server says it has, or,
// when it refuses CONFIG, every one INFO keyspace lists as holding keys, or, when it
// refuses that too, every one it lets berth select.
func (c *Conn) Databases() ([]int, error) {
	if c.dbs != nil {
		return c.dbs, nil
	}
	dbs, err := c.findDatabases()
	if err != nil {
		return nil, c.wrap(err)
	}
	c.dbs = dbs
	return dbs, nil
}

func (c *Conn) findDatabases() ([]int, error) {
	var refused ServerError
	rep, err := c.do("CONFIG", "GET", "databases")
	if err == nil && len(rep.Array) == 2 {
		if n, convErr := strconv.Atoi(string(rep.Array[1].Str)); convErr == nil && n > 0 {
			out := make([]int, n)
			for i := range out {
				out[i] = i
			}
			return out, nil
		}
	} else if err != nil && !errors.As(err, &refused) {
		return nil, err
	}
	rep, err = c.do("INFO", "keyspace")
	if err == nil {
		return keyspace(rep.Str), nil
	}
	if !errors.As(err, &refused) {
		return nil, err
	}
	var out []int
	for n := 0; n < maxSelect; n++ {
		if err := c.selectDB(n); err != nil {
			if errors.As(err, &refused) {
				break
			}
			return nil, err
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("it refused CONFIG GET databases, INFO keyspace and SELECT 0, so berth can't tell which logical databases it has")
	}
	return out, nil
}

// keyspace reads the databases INFO keyspace lists, as lines such as db0:keys=3,....
func keyspace(info []byte) []int {
	out := []int{}
	for _, line := range strings.Split(string(info), "\n") {
		name, _, ok := strings.Cut(strings.TrimSpace(line), ":")
		if n, err := strconv.Atoi(strings.TrimPrefix(name, "db")); ok && strings.HasPrefix(name, "db") && err == nil && n >= 0 {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func (c *Conn) selectDB(n int) error {
	if c.selected == n {
		return nil
	}
	if _, err := c.do("SELECT", strconv.Itoa(n)); err != nil {
		return err
	}
	c.selected = n
	return nil
}

// Pattern is the SCAN MATCH pattern for keys starting with prefix: every glob character
// in it escaped, so the server reads it as text, and a star after it.
func Pattern(prefix []byte) []byte {
	out := make([]byte, 0, len(prefix)*2+1)
	for _, b := range prefix {
		if bytes.IndexByte([]byte(`*?[]\`), b) >= 0 {
			out = append(out, '\\')
		}
		out = append(out, b)
	}
	return append(out, '*')
}

var errNoPrefix = errors.New("berth looks for keys only under a prefix, and this one is empty")

// scan calls fn with the keys on the selected database that begin with prefix, compared
// byte for byte whatever the server's MATCH returned, until fn says to stop.
func (c *Conn) scan(prefix []byte, fn func(keys [][]byte) (stop bool, err error)) error {
	pattern := Pattern(prefix)
	cursor := []byte("0")
	for {
		rep, err := c.doBytes([]byte("SCAN"), cursor, []byte("MATCH"), pattern, []byte("COUNT"), []byte(scanCount))
		if err != nil {
			return err
		}
		if len(rep.Array) != 2 {
			return fmt.Errorf("SCAN sent a reply berth can't read")
		}
		var own [][]byte
		for _, k := range rep.Array[1].Array {
			if len(prefix) > 0 && bytes.HasPrefix(k.Str, prefix) {
				own = append(own, k.Str)
			}
		}
		if len(own) > 0 {
			if stop, err := fn(own); err != nil || stop {
				return err
			}
		}
		cursor = rep.Array[0].Str
		if string(cursor) == "0" {
			return nil
		}
	}
}

// AnyUnder reports whether any logical database holds a key that begins with prefix.
func (c *Conn) AnyUnder(prefix []byte) (bool, error) {
	if len(prefix) == 0 {
		return false, errNoPrefix
	}
	dbs, err := c.Databases()
	if err != nil {
		return false, err
	}
	found := false
	for _, db := range dbs {
		if err := c.selectDB(db); err != nil {
			return false, c.wrap(err)
		}
		err := c.scan(prefix, func([][]byte) (bool, error) {
			found = true
			return true, nil
		})
		if err != nil {
			return false, c.wrap(err)
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

// DeleteUnder deletes every key that begins with prefix, byte for byte, on every logical
// database, and says how many it deleted.
func (c *Conn) DeleteUnder(prefix []byte) (int64, error) {
	if len(prefix) == 0 {
		return 0, errNoPrefix
	}
	dbs, err := c.Databases()
	if err != nil {
		return 0, err
	}
	var deleted int64
	unlink := true
	for _, db := range dbs {
		if err := c.selectDB(db); err != nil {
			return deleted, c.wrap(err)
		}
		err := c.scan(prefix, func(keys [][]byte) (bool, error) {
			n, err := c.delete(keys, &unlink)
			deleted += n
			return false, err
		})
		if err != nil {
			return deleted, c.wrap(err)
		}
	}
	return deleted, nil
}

// delete removes keys with UNLINK, which frees their memory in the background, and with
// DEL on a server older than Redis 4 that has no UNLINK.
func (c *Conn) delete(keys [][]byte, unlink *bool) (int64, error) {
	for {
		cmd := []byte("DEL")
		if *unlink {
			cmd = []byte("UNLINK")
		}
		rep, err := c.doBytes(append([][]byte{cmd}, keys...)...)
		var refused ServerError
		if *unlink && errors.As(err, &refused) && strings.Contains(strings.ToLower(string(refused)), "unknown command") {
			*unlink = false
			continue
		}
		return rep.Int, err
	}
}
