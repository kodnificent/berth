// Package hostdb is what berth needs from a database server on the host, whichever
// engine it is: where the server is, the login, and a connection that lists, creates,
// drops and measures databases. The engine packages implement it.
package hostdb

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Server is where a database server listens.
type Server struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

func (s Server) String() string { return net.JoinHostPort(s.Host, strconv.Itoa(s.Port)) }

// Same reports whether s and o name one server, counting every loopback spelling as one.
func (s Server) Same(o Server) bool {
	return s.Port == o.Port && canonical(s.Host) == canonical(o.Host)
}

// Loopback reports whether s listens on this machine's loopback interface.
func (s Server) Loopback() bool { return canonical(s.Host) == "127.0.0.1" }

func canonical(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "localhost" {
		return "127.0.0.1"
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		return "127.0.0.1"
	}
	return h
}

// Login is the user and password berth connects as. It is read when needed and never
// written anywhere.
type Login struct {
	User     string
	Password string
}

// Conn is an open connection to one server.
type Conn interface {
	// Databases lists every database on the server apart from the engine's own.
	Databases() ([]string, error)
	// Create makes the empty database name, leaving one that exists as it is.
	Create(name string) error
	// Drop deletes the database name and everything in it.
	Drop(name string) error
	// Size is how much the database name holds, as it stands now.
	Size(name string) (int64, error)
	Close() error
}

// Engine is one kind of host database server.
type Engine struct {
	// Name is how messages call it, such as MySQL.
	Name string
	// Table is its table under [state], such as mysql, and its record's key.
	Table       string
	MaxName     int
	DefaultPort int
	// URLRouting are the query parameters of a URL that its drivers follow to another
	// host, port or database than the URL's own.
	URLRouting []string
	Open       func(Server, Login) (Conn, error)
}

// Location reads the keys of a .env a server and login are set in, under prefix or in
// one URL.
func (e Engine) Location(prefix, urlKey string) Location {
	return Location{Prefix: prefix, URLKey: urlKey, DefaultPort: e.DefaultPort, Routing: e.URLRouting}
}

// Location is where a .env sets a server and login: keys under Prefix, or one URL in
// URLKey.
type Location struct {
	Prefix      string
	URLKey      string
	DefaultPort int
	// Routing are the query parameters a URL is refused for, as its engine's URLRouting.
	Routing []string
}

var (
	hostKeys     = []string{"HOST"}
	portKeys     = []string{"PORT"}
	userKeys     = []string{"USERNAME", "USER"}
	passwordKeys = []string{"PASSWORD"}
)

// ServerKeys are every key that holds the server or the login, which berth never
// rewrites as a database name.
func (l Location) ServerKeys() []string {
	if l.URLKey != "" {
		return []string{l.URLKey}
	}
	var out []string
	for _, group := range [][]string{hostKeys, portKeys, userKeys, passwordKeys} {
		for _, k := range group {
			out = append(out, l.Prefix+k)
		}
	}
	return out
}

// LoginKeys are the keys to set the login in, as a message names them.
func (l Location) LoginKeys() []string {
	if l.URLKey != "" {
		return []string{l.URLKey}
	}
	return []string{l.Prefix + userKeys[0], l.Prefix + passwordKeys[0]}
}

// Server reads the server in env. missing names the keys a server needs that env
// doesn't set.
func (l Location) Server(env map[string]string) (s Server, missing []string, err error) {
	if l.URLKey != "" {
		return l.urlServer(env)
	}
	host, ok := lookup(env, l.Prefix, hostKeys)
	if !ok || host == "" {
		missing = append(missing, l.Prefix+hostKeys[0])
	}
	if _, ok := lookup(env, l.Prefix, userKeys); !ok {
		missing = append(missing, l.Prefix+userKeys[0])
	}
	s = Server{Host: host, Port: l.DefaultPort}
	if p, ok := lookup(env, l.Prefix, portKeys); ok && p != "" {
		n, convErr := strconv.Atoi(p)
		if convErr != nil || n < 1 || n > 65535 {
			return s, missing, fmt.Errorf("%s%s is %q, which is not a port", l.Prefix, portKeys[0], p)
		}
		s.Port = n
	}
	return s, missing, nil
}

// Login reads the login from the first env that sets its user.
func (l Location) Login(envs ...map[string]string) (Login, bool) {
	for _, env := range envs {
		if l.URLKey != "" {
			if u, err := parseURL(env[l.URLKey]); err == nil && u.User != nil && u.User.Username() != "" {
				pw, _ := u.User.Password()
				return Login{User: u.User.Username(), Password: pw}, true
			}
			continue
		}
		if user, ok := lookup(env, l.Prefix, userKeys); ok {
			pw, _ := lookup(env, l.Prefix, passwordKeys)
			return Login{User: user, Password: pw}, true
		}
	}
	return Login{}, false
}

func lookup(env map[string]string, prefix string, keys []string) (string, bool) {
	for _, k := range keys {
		if v, ok := env[prefix+k]; ok {
			return v, true
		}
	}
	return "", false
}

func (l Location) urlServer(env map[string]string) (s Server, missing []string, err error) {
	raw, ok := env[l.URLKey]
	if !ok || raw == "" {
		return s, []string{l.URLKey}, nil
	}
	u, err := parseURL(raw)
	if err != nil {
		return s, nil, fmt.Errorf("%s %v", l.URLKey, err)
	}
	if u.Hostname() == "" {
		return s, nil, fmt.Errorf("%s names no host", l.URLKey)
	}
	if strings.Contains(u.Host, ",") {
		return s, nil, fmt.Errorf("%s names more than one host; berth needs the one server main uses", l.URLKey)
	}
	// The app's driver would follow these past the path berth rewrites, so the berth's URL
	// would still reach main's database.
	for param := range u.Query() {
		if slices.ContainsFunc(l.Routing, func(r string) bool { return strings.EqualFold(r, param) }) {
			return s, nil, fmt.Errorf("%s sets %s in its query, which would point the app past the database berth names in its path; set the server and database in the URL itself and remove %s", l.URLKey, param, param)
		}
	}
	if u.User == nil || u.User.Username() == "" {
		return s, nil, fmt.Errorf("%s names no user", l.URLKey)
	}
	s = Server{Host: u.Hostname(), Port: l.DefaultPort}
	if p := u.Port(); p != "" {
		n, convErr := strconv.Atoi(p)
		if convErr != nil || n < 1 || n > 65535 {
			return s, nil, fmt.Errorf("%s has a port that is not a port", l.URLKey)
		}
		s.Port = n
	}
	return s, nil, nil
}

// parseURL reads raw, and its errors never quote it: net/url's would carry the password.
func parseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Opaque != "" {
		return nil, fmt.Errorf("is not a URL berth can read; want scheme://user:password@host:port/database, with special characters percent-encoded")
	}
	return u, nil
}

// URLDatabase is the database the URL in env names.
func (l Location) URLDatabase(env map[string]string) (string, error) {
	u, err := parseURL(env[l.URLKey])
	if err != nil {
		return "", fmt.Errorf("%s %v", l.URLKey, err)
	}
	name := strings.TrimPrefix(u.Path, "/")
	if name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("%s names no single database in its path", l.URLKey)
	}
	return name, nil
}

// WithDatabase is the URL raw with its path set to database and every other byte as it
// was, so a login's percent-encoding and the query string survive. raw has already been
// read with Location.Server.
func WithDatabase(raw, database string) string {
	start, end := pathBounds(raw)
	return raw[:start] + "/" + url.PathEscape(database) + raw[end:]
}

// pathBounds finds the URL path raw[start:end], empty right after the authority when
// raw has none.
func pathBounds(raw string) (start, end int) {
	start = strings.Index(raw, "://") + len("://")
	if n := strings.IndexAny(raw[start:], "/?#"); n >= 0 {
		start += n
	} else {
		start = len(raw)
	}
	end = len(raw)
	if n := strings.IndexAny(raw[start:], "?#"); n >= 0 {
		end = start + n
	}
	return start, end
}

// Redacted is the URL in raw with the login and the query string, which may hold a
// password, taken out, for anything berth shows. A URL berth can't read is hidden whole.
func Redacted(raw string) string {
	u, err := parseURL(raw)
	if err != nil {
		return "<redacted>"
	}
	out := u.Scheme + "://"
	if u.User != nil {
		out += "<redacted>@"
	}
	out += u.Host + u.EscapedPath()
	if u.RawQuery != "" {
		out += "?<redacted>"
	}
	return out
}

// Scrub takes the login out of a driver error, which names the user on access denied.
func Scrub(err error, login Login) string {
	msg := err.Error()
	for _, secret := range []string{login.Password, login.User} {
		if secret != "" {
			msg = strings.ReplaceAll(msg, secret, "<redacted>")
		}
	}
	return msg
}
