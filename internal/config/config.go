// Package config loads berth's layered configuration, validates it and expands its
// templates. It only reports values; acting on them belongs to the commands.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/bytesize"
	"github.com/kodnificent/berth/internal/names"
	"github.com/kodnificent/berth/internal/worktree"
)

const (
	DefaultHost = "{name}.localhost"
	DefaultURL  = "http://{host}"
	// DefaultPool sits below 32768, where Linux and Docker hand out ephemeral ports, so an
	// assigned port never races a port Docker picks for an ephemeral mapping.
	DefaultPool = "20000-29999"
	// DefaultMinFree is the free disk below which add warns before a clone.
	DefaultMinFree = "10GB"
)

type Config struct {
	Berth   Berth           `toml:"berth"`
	Compose Compose         `toml:"compose"`
	Files   Files           `toml:"files"`
	Env     Env             `toml:"env"`
	Ports   map[string]Port `toml:"ports"`
	Hooks   Hooks           `toml:"hooks"`
	Pool    Pool            `toml:"pool"`
	Clone   Clone           `toml:"clone"`
	State   State           `toml:"state"`
	Doctor  Doctor          `toml:"doctor"`
	Proxy   Proxy           `toml:"proxy"`

	// origin maps each key, as TOML writes it, to the file whose value won.
	origin map[string]string
}

type Berth struct {
	Host string `toml:"host"`
	URL  string `toml:"url"`
}

type Compose struct {
	Files   []string          `toml:"files"`
	Exclude []string          `toml:"exclude"`
	Ports   map[string]string `toml:"ports"`
	Volumes map[string]string `toml:"volumes"`
}

type Files struct {
	Clone []string `toml:"clone"`
}

type Env struct {
	URL  []string          `toml:"url"`
	Host []string          `toml:"host"`
	Set  map[string]string `toml:"set"`
}

type Port struct {
	Env string `toml:"env"`
}

type Pool struct {
	Ports string `toml:"ports"`
}

type Clone struct {
	MinFree string `toml:"min_free"`
}

type State struct {
	MySQL    *HostDB `toml:"mysql"`
	Postgres *HostDB `toml:"postgres"`
	SQLite   *SQLite `toml:"sqlite"`
	Redis    *Redis  `toml:"redis"`
}

// HostDB is the host database server the config names, with its table name, nil when it
// names none. Validation refuses a config that names two.
func (c *Config) HostDB() (*HostDB, string) {
	if c.State.Postgres != nil {
		return c.State.Postgres, "postgres"
	}
	if c.State.MySQL != nil {
		return c.State.MySQL, "mysql"
	}
	return nil, ""
}

// SQLite lists database files in the worktree that a berth gets its own copy of.
type SQLite struct {
	Files []string `toml:"files"`
}

// SQLiteFiles is [state.sqlite] files, cleaned, each once, in the order listed. They
// keep the config's forward slashes on Windows too, so berth names them as written.
func (c *Config) SQLiteFiles() []string {
	if c.State.SQLite == nil {
		return nil
	}
	var out []string
	for _, p := range c.State.SQLite.Files {
		if p = filepath.ToSlash(filepath.Clean(p)); !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// HostDB is a host database server a berth gets its own databases on, next to main's.
type HostDB struct {
	From      string         `toml:"from"`
	Prefix    string         `toml:"prefix"`
	Default   string         `toml:"default"`
	Databases map[string]any `toml:"databases"`
	// Disconnect lets a Postgres template copy end main's sessions on the database it
	// copies. A pointer, so validation can tell it was set under [state.mysql].
	Disconnect *bool `toml:"disconnect"`
}

func (m *HostDB) Disconnects() bool { return m.Disconnect != nil && *m.Disconnect }

// Database is what the config says about one database: a strategy, and the seed file or
// the command it loads with.
type Database struct {
	Strategy string
	File     string
	Command  string
}

// Database strategies. File and Command are set with a table entry naming the file or
// the script, never as a plain strategy name.
const (
	Dump     = "dump"
	Fresh    = "fresh"
	Skip     = "skip"
	None     = "none"
	Snapshot = "snapshot"
	File     = "file"
	Command  = "command"
	Template = "template"
)

var (
	databaseStrategies = []string{Dump, Fresh, Skip, None, Snapshot}
	postgresStrategies = []string{Template, Fresh, Skip, None}
	fromPattern        = regexp.MustCompile(`^env:([A-Za-z_][A-Za-z0-9_]*)\*$`)
	fromURLPattern     = regexp.MustCompile(`^env:([A-Za-z_][A-Za-z0-9_]*)$`)
	prefixPattern      = regexp.MustCompile(`^env:([A-Za-z_][A-Za-z0-9_]*)$`)
	databaseKey        = regexp.MustCompile(`^[A-Za-z0-9_$*-]+$`)
)

// EnvPrefix is the prefix from names, under which main's .env sets the server's keys.
func (m *HostDB) EnvPrefix() string {
	if g := fromPattern.FindStringSubmatch(m.From); g != nil {
		return g[1]
	}
	return ""
}

// URLKey is the env key from names when it is one URL holding the server and login.
func (m *HostDB) URLKey() string {
	if g := fromURLPattern.FindStringSubmatch(m.From); g != nil {
		return g[1]
	}
	return ""
}

// PrefixKey is the env key main's database prefix is read from, empty without prefix.
func (m *HostDB) PrefixKey() string {
	if g := prefixPattern.FindStringSubmatch(m.Prefix); g != nil {
		return g[1]
	}
	return ""
}

// Entry is what the config says about the database key: its own entry, else the matching
// pattern with the most literal characters, else default.
func (m *HostDB) Entry(key string) Database {
	if raw, ok := m.Databases[key]; ok {
		return parseDatabase(raw)
	}
	var best any
	bestLen := -1
	for _, pattern := range sortedKeys(m.Databases) {
		if !strings.Contains(pattern, "*") {
			continue
		}
		if ok, _ := path.Match(pattern, key); ok && len(strings.ReplaceAll(pattern, "*", "")) > bestLen {
			best, bestLen = m.Databases[pattern], len(strings.ReplaceAll(pattern, "*", ""))
		}
	}
	if bestLen >= 0 {
		return parseDatabase(best)
	}
	if m.Default != "" {
		return Database{Strategy: m.Default}
	}
	return Database{Strategy: Skip}
}

// Strategy is the strategy for the database key.
func (m *HostDB) Strategy(key string) string { return m.Entry(key).Strategy }

// parseDatabase reads an entry validate has already accepted.
func parseDatabase(raw any) Database {
	table, ok := raw.(map[string]any)
	if !ok {
		s, _ := raw.(string)
		return Database{Strategy: s}
	}
	if from, ok := table["from"].(string); ok {
		return Database{Strategy: File, File: strings.TrimPrefix(from, "file:")}
	}
	cmd, _ := table["command"].(string)
	return Database{Strategy: Command, Command: cmd}
}

// Listed is every exact database key, sorted.
func (m *HostDB) Listed() []string {
	var out []string
	for _, k := range sortedKeys(m.Databases) {
		if !strings.Contains(k, "*") {
			out = append(out, k)
		}
	}
	return out
}

// Doctor holds what berth doctor --probe requests besides the berth's URL.
type Doctor struct {
	Probe []string `toml:"probe"`
}

type Hooks struct {
	PostAdd   string `toml:"post-add"`
	PreRemove string `toml:"pre-remove"`
}

// LocalName is the visible personal config, at the top of the main worktree.
const LocalName = "berth.local.toml"

// Paths lists the config files for a worktree, highest precedence first.
func Paths(w *worktree.Worktree) ([]string, error) {
	personal, _, err := Personal(w)
	if err != nil {
		return nil, err
	}
	user, err := UserPath()
	if err != nil {
		return nil, err
	}
	return []string{personal, RepoPath(w), user}, nil
}

// Personal is the personal layer's path: main's berth.local.toml or the hidden
// berth.toml in the common git directory, whichever exists, else where it would be
// created. Both are read from main, because git doesn't copy ignored files into new
// worktrees.
func Personal(w *worktree.Worktree) (path string, exists bool, err error) {
	hidden := filepath.Join(w.CommonDir, "berth.toml")
	hasHidden, err := isFile(hidden)
	if err != nil {
		return "", false, err
	}
	// A bare repository's first worktree entry is the git directory itself, so there is
	// no main working tree to hold a visible file.
	if w.Repository == w.CommonDir {
		return hidden, hasHidden, nil
	}
	local := filepath.Join(w.Repository, LocalName)
	hasLocal, err := isFile(local)
	switch {
	case err != nil:
		return "", false, err
	case hasLocal && hasHidden:
		return "", false, invalid(local, "%s is a personal config too; keep one and delete the other", hidden)
	case hasHidden:
		return hidden, true, nil
	default:
		return local, hasLocal, nil
	}
}

// RepoPath is the committed config of the worktree berth acts on, so each branch reads
// its own version.
func RepoPath(w *worktree.Worktree) string {
	return filepath.Join(w.Path, "berth.toml")
}

func isFile(path string) (bool, error) {
	_, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading config %s: %w", path, err)
	}
	return true, nil
}

// UserPath is ~/.config/berth/config.toml on every platform, as the design documents,
// rather than os.UserConfigDir, which is ~/Library/Application Support on macOS.
func UserPath() (string, error) {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" && filepath.IsAbs(d) {
		return filepath.Join(d, "berth", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding the user config: %w", err)
	}
	return filepath.Join(home, ".config", "berth", "config.toml"), nil
}

func Load(w *worktree.Worktree) (*Config, error) {
	paths, err := Paths(w)
	if err != nil {
		return nil, err
	}
	c, err := LoadFiles(paths...)
	if err != nil {
		return nil, err
	}
	if err := c.proxyOnlyInUserConfig(paths[len(paths)-1]); err != nil {
		return nil, err
	}
	return c, nil
}

// LoadFiles merges the given files, highest precedence first. Missing files are
// skipped. Tables merge key by key; any other value, arrays included, replaces the
// lower layer's value whole.
func LoadFiles(paths ...string) (*Config, error) {
	layers := make([]map[string]any, len(paths))
	for i, p := range paths {
		layer, err := readLayer(p)
		if err != nil {
			return nil, err
		}
		layers[i] = layer
	}
	return build(paths, layers)
}

// Parse loads data as if it were the only config file, at path, so a config can be
// checked before it is written.
func Parse(path string, data []byte) (*Config, error) {
	layer, err := decodeLayer(path, data)
	if err != nil {
		return nil, err
	}
	return build([]string{path}, []map[string]any{layer})
}

func build(paths []string, layers []map[string]any) (*Config, error) {
	merged := map[string]any{}
	origin := map[string]string{}
	for i := len(paths) - 1; i >= 0; i-- {
		merge(merged, layers[i], nil, origin, paths[i])
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(merged); err != nil {
		return nil, fmt.Errorf("merging config: %w", err)
	}
	c := &Config{origin: origin}
	if _, err := toml.Decode(buf.String(), c); err != nil {
		return nil, fmt.Errorf("merging config: %w", err)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func readLayer(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	return decodeLayer(path, data)
}

func decodeLayer(path string, data []byte) (map[string]any, error) {
	var generic map[string]any
	if _, err := toml.Decode(string(data), &generic); err != nil {
		return nil, invalid(path, "%s", strings.TrimPrefix(err.Error(), "toml: "))
	}
	md, err := toml.Decode(string(data), &Config{})
	if err != nil {
		return nil, invalid(path, "%s", strings.TrimPrefix(err.Error(), "toml: "))
	}
	if unknown := md.Undecoded(); len(unknown) > 0 {
		var keys []string
		for _, k := range unknown {
			if databaseOption(k) {
				continue
			}
			if n := len(keys); n > 0 && strings.HasPrefix(k.String(), keys[n-1]+".") {
				continue
			}
			keys = append(keys, k.String())
		}
		if len(keys) > 0 {
			return nil, invalid(path, "unknown key %s", strings.Join(keys, ", "))
		}
	}
	return generic, nil
}

func databaseEntry(key toml.Key) bool {
	return len(key) == 4 && key[0] == "state" && (key[1] == "mysql" || key[1] == "postgres") && key[2] == "databases"
}

// databaseOption reports whether key is one of a database entry's own keys, which decode
// through an untyped value and so show as undecoded.
func databaseOption(key toml.Key) bool {
	return len(key) == 5 && databaseEntry(key[:4]) && (key[4] == "from" || key[4] == "command")
}

func invalid(path, format string, args ...any) error {
	return apperr.New(apperr.ConfigInvalid, "%s: %s", path, fmt.Sprintf(format, args...))
}

func merge(dst, src map[string]any, prefix toml.Key, origin map[string]string, file string) {
	for k, v := range src {
		key := append(prefix[:len(prefix):len(prefix)], k)
		if table, ok := v.(map[string]any); ok && !databaseEntry(key) {
			sub, ok := dst[k].(map[string]any)
			if !ok {
				sub = map[string]any{}
				dst[k] = sub
			}
			merge(sub, table, key, origin, file)
			continue
		}
		dst[k] = v
		origin[key.String()] = file
	}
}

// Host is the berth's host template.
func (c *Config) Host() string {
	if c.Berth.Host != "" {
		return c.Berth.Host
	}
	return DefaultHost
}

// URL is the berth's URL template.
func (c *Config) URL() string {
	if c.Berth.URL != "" {
		return c.Berth.URL
	}
	return DefaultURL
}

// MinFree is the free disk, in bytes, that a clone should leave.
func (c *Config) MinFree() int64 {
	spec := c.Clone.MinFree
	if spec == "" {
		spec = DefaultMinFree
	}
	n, _ := bytesize.Parse(spec)
	return n
}

// PoolRange is the port pool every berth on the machine draws assigned ports from.
func (c *Config) PoolRange() (lo, hi int) {
	spec := c.Pool.Ports
	if spec == "" {
		spec = DefaultPool
	}
	lo, hi, _ = parsePool(spec)
	return lo, hi
}

func parsePool(spec string) (lo, hi int, ok bool) {
	a, b, found := strings.Cut(spec, "-")
	lo, errA := strconv.Atoi(strings.TrimSpace(a))
	hi, errB := strconv.Atoi(strings.TrimSpace(b))
	return lo, hi, found && errA == nil && errB == nil && lo >= 1 && lo <= hi && hi <= 65535
}

// NameMax is the longest {name} that keeps every label of the host a valid DNS label.
func (c *Config) NameMax() int {
	max := names.MaxName
	parts, err := parseTemplate(c.Host())
	if err != nil {
		return max
	}
	var fixed, count, labelFixed, labelCount int
	label := func() {
		if labelCount > 0 {
			max = min(max, (names.MaxLabel-labelFixed)/labelCount)
		}
		labelFixed, labelCount = 0, 0
	}
	for _, p := range parts {
		if p.token == "name" {
			count++
			labelCount++
			continue
		}
		for i, seg := range strings.Split(p.literal+p.token, ".") {
			if i > 0 {
				label()
				fixed++
			}
			fixed += len(seg)
			labelFixed += len(seg)
		}
	}
	label()
	if count > 0 {
		max = min(max, (253-fixed)/count)
	}
	return max
}

var (
	envName  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	portName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

var (
	portPolicies = []string{"assign", "ephemeral", "none"}
	volumeRoles  = []string{"clone", "share", "fresh"}
)

func (c *Config) validate() error {
	var problems []string
	bad := func(key toml.Key, format string, args ...any) {
		file := c.fileOf(key)
		problems = append(problems, fmt.Sprintf("%s: %s: %s", file, key, fmt.Sprintf(format, args...)))
	}

	for _, k := range sortedKeys(c.Compose.Ports) {
		key := toml.Key{"compose", "ports", k}
		svc, port, ok := strings.Cut(k, ":")
		if n, err := strconv.Atoi(port); !ok || svc == "" || err != nil || n < 1 || n > 65535 {
			bad(key, "want \"<service>:<container port>\", such as \"web:80\"")
		}
		if p := c.Compose.Ports[k]; !oneOf(p, portPolicies) {
			bad(key, "unknown port policy %q; want one of %s", p, strings.Join(portPolicies, ", "))
		}
	}
	if c.Clone.MinFree != "" {
		if _, err := bytesize.Parse(c.Clone.MinFree); err != nil {
			bad(toml.Key{"clone", "min_free"}, "%v", err)
		}
	}
	if c.Pool.Ports != "" {
		if _, _, ok := parsePool(c.Pool.Ports); !ok {
			bad(toml.Key{"pool", "ports"}, "want a range \"<first>-<last>\" within 1-65535, such as %q", DefaultPool)
		}
	}
	if parts, err := parseTemplate(c.Host()); err == nil && c.Berth.Host != "" {
		sample, _ := expand(parts, Values{Name: "a"})
		switch {
		case !uses(parts, "name"):
			bad(toml.Key{"berth", "host"}, "must contain {name}, or every berth would share one host and its cookies")
		case !names.ValidHost(sample):
			bad(toml.Key{"berth", "host"}, "%q is not a valid hostname once {name} is filled in: use lowercase letters, digits, hyphens and dots", c.Berth.Host)
		}
	}
	for _, k := range sortedKeys(c.Compose.Volumes) {
		if r := c.Compose.Volumes[k]; !oneOf(r, volumeRoles) {
			bad(toml.Key{"compose", "volumes", k}, "unknown volume role %q; want one of %s", r, strings.Join(volumeRoles, ", "))
		}
	}
	for _, list := range []struct {
		key  toml.Key
		vals []string
	}{
		{toml.Key{"compose", "files"}, c.Compose.Files},
		{toml.Key{"compose", "exclude"}, c.Compose.Exclude},
	} {
		for _, v := range list.vals {
			if v == "" {
				bad(list.key, "holds an empty string")
			}
		}
	}
	for _, p := range c.Files.Clone {
		if !inside(p) {
			bad(toml.Key{"files", "clone"}, "%q is not a path inside the worktree", p)
		}
	}
	if s := c.State.SQLite; s != nil {
		c.validateSQLite(s, bad)
	}
	for _, list := range []struct {
		key  toml.Key
		vals []string
	}{
		{toml.Key{"env", "url"}, c.Env.URL},
		{toml.Key{"env", "host"}, c.Env.Host},
	} {
		for _, v := range list.vals {
			if !envName.MatchString(v) {
				bad(list.key, "%q is not a valid environment variable name", v)
			}
		}
	}

	reserved := func(key toml.Key, name string) {
		if name == "COMPOSE_PROJECT_NAME" || name == "COMPOSE_FILE" {
			bad(key, "%s is written by berth to point compose at the berth's own project; remove it", name)
		}
	}
	for _, k := range sortedKeys(c.Env.Set) {
		reserved(toml.Key{"env", "set", k}, k)
	}
	for _, v := range c.Env.URL {
		reserved(toml.Key{"env", "url"}, v)
	}
	for _, v := range c.Env.Host {
		reserved(toml.Key{"env", "host"}, v)
	}
	for _, name := range sortedKeys(c.Ports) {
		reserved(toml.Key{"ports", name, "env"}, c.Ports[name].Env)
	}
	if c.State.MySQL != nil && c.State.Postgres != nil {
		bad(toml.Key{"state", "postgres"}, "a project can name one host database server, and [state.mysql] is set too; remove one of them")
	}
	if m := c.State.MySQL; m != nil {
		c.validateHostDB(m, "mysql", databaseStrategies, bad)
	}
	if m := c.State.Postgres; m != nil {
		c.validateHostDB(m, "postgres", postgresStrategies, bad)
	}
	if r := c.State.Redis; r != nil {
		c.validateRedis(r, bad)
	}

	ports := c.portTable()
	for _, name := range sortedKeys(c.Ports) {
		key := toml.Key{"ports", name}
		if !portName.MatchString(name) {
			bad(key, "%q is not a valid port name", name)
		}
		if ports.assigned[name] > 0 {
			bad(key, "%s is also a compose service with an assigned port, so {port.%s} would be ambiguous", name, name)
		}
		if e := c.Ports[name].Env; e != "" && !envName.MatchString(e) {
			bad(toml.Key{"ports", name, "env"}, "%q is not a valid environment variable name", e)
		}
	}

	templates := []struct {
		key     toml.Key
		value   string
		allowed []string
	}{
		{toml.Key{"berth", "host"}, c.Berth.Host, []string{"name"}},
		{toml.Key{"berth", "url"}, c.Berth.URL, []string{"name", "host", "port"}},
	}
	for _, k := range sortedKeys(c.Env.Set) {
		templates = append(templates, struct {
			key     toml.Key
			value   string
			allowed []string
		}{toml.Key{"env", "set", k}, c.Env.Set[k], []string{"name", "host", "url", "port"}})
		if !envName.MatchString(k) {
			bad(toml.Key{"env", "set", k}, "%q is not a valid environment variable name", k)
		}
	}
	for _, u := range c.Doctor.Probe {
		templates = append(templates, struct {
			key     toml.Key
			value   string
			allowed []string
		}{toml.Key{"doctor", "probe"}, u, []string{"name", "host", "url", "port"}})
		if why := c.probeOutside(u); why != "" {
			bad(toml.Key{"doctor", "probe"}, "%s", why)
		}
	}
	c.validateProxy(ports, bad)
	for _, t := range templates {
		parts, err := parseTemplate(t.value)
		if err != nil {
			bad(t.key, "%v", err)
			continue
		}
		for _, p := range parts {
			if p.token == "" {
				continue
			}
			if name, isPort := strings.CutPrefix(p.token, "port."); isPort && oneOf("port", t.allowed) {
				if why := ports.undefined(name); why != "" {
					bad(t.key, "{%s} names no defined port: %s", p.token, why)
				}
				continue
			}
			if !oneOf(p.token, t.allowed) {
				bad(t.key, "unknown template value {%s}; here you can use %s", p.token, describe(t.allowed))
			}
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return apperr.New(apperr.ConfigInvalid, "%s", strings.Join(problems, "\n"))
}

// inside reports whether p is a relative path that stays inside the worktree. On
// Windows, a path from the root of the current drive, or relative to another drive's
// current directory, isn't absolute but leaves the worktree all the same.
func inside(p string) bool {
	clean := filepath.Clean(p)
	return p != "" && !filepath.IsAbs(p) && filepath.VolumeName(p) == "" && !strings.HasPrefix(clean, string(filepath.Separator)) &&
		clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func (c *Config) validateSQLite(s *SQLite, bad func(toml.Key, string, ...any)) {
	key := toml.Key{"state", "sqlite", "files"}
	for _, p := range s.Files {
		if !inside(p) {
			bad(key, "%q is not a database file inside the worktree", p)
			continue
		}
		clean := filepath.Clean(p)
		for _, dir := range c.Files.Clone {
			d := filepath.Clean(dir)
			if clean == d || strings.HasPrefix(clean, d+string(filepath.Separator)) {
				bad(key, "%q is also cloned by [files] clone %q, which copies files as they are and could catch the database mid-write; take it out of one of them", p, dir)
			}
		}
	}
}

func (c *Config) validateHostDB(m *HostDB, name string, strategies []string, bad func(toml.Key, string, ...any)) {
	table := toml.Key{"state", name}
	switch {
	case m.From == "":
		bad(table, "needs from = \"env:<PREFIX>*\" naming the keys main's .env sets the server in, such as \"env:DB_*\", or from = \"env:<KEY>\" naming one URL, such as \"env:DATABASE_URL\"")
	case m.EnvPrefix() == "" && m.URLKey() == "":
		bad(toml.Key{"state", name, "from"}, "%q is neither \"env:<PREFIX>*\" nor \"env:<KEY>\"; want the prefix of the keys main's .env sets the server in, such as \"env:DB_*\", or the one key holding its URL, such as \"env:DATABASE_URL\"", m.From)
	}
	if m.Disconnect != nil && name != "postgres" {
		bad(toml.Key{"state", name, "disconnect"}, "unknown key: disconnect ends main's sessions before a Postgres template copy, so it belongs under [state.postgres]")
	}
	if m.Prefix != "" && m.PrefixKey() == "" {
		bad(toml.Key{"state", name, "prefix"}, "%q is not \"env:<KEY>\"; want the key main's .env sets its database prefix in, such as \"env:DB_DATABASE_PREFIX\"", m.Prefix)
	}
	if m.Default != "" {
		key := toml.Key{"state", name, "default"}
		if why := postgresRefusal(name, m.Default); why != "" {
			bad(key, "%s", why)
		} else if !oneOf(m.Default, strategies) {
			bad(key, "unknown strategy %q; want one of %s", m.Default, strings.Join(strategies, ", "))
		}
	}
	for _, k := range sortedKeys(m.Databases) {
		key := toml.Key{"state", name, "databases", k}
		switch {
		case !databaseKey.MatchString(k):
			bad(key, "%q is not a database name or pattern: use letters, digits, _, $ and -, with * matching any run of them", k)
		case strings.Contains(k, "*") && m.Prefix == "":
			bad(key, "a pattern matches names after the prefix, so it needs [state.%s] prefix; without one, list each database by its name", name)
		}
		if why := postgresRefusal(name, m.Databases[k]); why != "" {
			bad(key, "%s", why)
			continue
		}
		validateDatabaseEntry(key, m.Databases[k], strategies, bad)
	}
	if pk := m.PrefixKey(); pk != "" {
		for _, w := range c.EnvWriters() {
			if w.Env == pk {
				bad(w.Key, "%s is written by berth with the berth's database prefix ([state.%s] prefix); remove it", pk, name)
			}
		}
	}
}

// postgresRefusal says why a strategy MySQL has can't be used on Postgres in this
// version, empty when it can or it isn't Postgres.
func postgresRefusal(table string, entry any) string {
	if table != "postgres" {
		return ""
	}
	want := fmt.Sprintf("want one of %s or { command = \"<script>\" }", strings.Join(postgresStrategies, ", "))
	strategy, _ := entry.(string)
	if t, ok := entry.(map[string]any); ok {
		if _, isFile := t["from"]; isFile {
			strategy = File
		}
	}
	switch strategy {
	case Dump, Snapshot:
		return fmt.Sprintf("the %s strategy is MySQL's in this version of berth, so a Postgres database can't use it; %s", strategy, want)
	case File:
		return fmt.Sprintf("a seed file is MySQL's in this version of berth, so a Postgres database can't use one; %s", want)
	}
	return ""
}

func validateDatabaseEntry(key toml.Key, raw any, strategies []string, bad func(toml.Key, string, ...any)) {
	want := fmt.Sprintf("want one of %s, { from = \"file:<path>\" } or { command = \"<script>\" }", strings.Join(strategies, ", "))
	switch v := raw.(type) {
	case string:
		if !oneOf(v, strategies) {
			bad(key, "unknown strategy %q; %s", v, want)
		}
	case map[string]any:
		from, hasFrom := v["from"]
		command, hasCommand := v["command"]
		switch {
		case hasFrom == hasCommand || len(v) != 1:
			bad(key, "a table entry holds exactly one of from or command; %s", want)
		case hasFrom:
			f, ok := from.(string)
			if !ok || !strings.HasPrefix(f, "file:") || f == "file:" {
				bad(key, "from must be \"file:<path>\", a path resolved in the main worktree")
			}
		default:
			c, ok := command.(string)
			if !ok || strings.TrimSpace(c) == "" {
				bad(key, "command must be the script to run, as a non-empty string")
			}
		}
	default:
		bad(key, "%s", want)
	}
}

// EnvWriter is a config entry that sets an env key in the managed block.
type EnvWriter struct {
	Key toml.Key
	Env string
}

// EnvWriters lists every entry under [env] and [ports] that writes an env key.
func (c *Config) EnvWriters() []EnvWriter {
	var out []EnvWriter
	for _, k := range sortedKeys(c.Env.Set) {
		out = append(out, EnvWriter{toml.Key{"env", "set", k}, k})
	}
	for _, v := range c.Env.URL {
		out = append(out, EnvWriter{toml.Key{"env", "url"}, v})
	}
	for _, v := range c.Env.Host {
		out = append(out, EnvWriter{toml.Key{"env", "host"}, v})
	}
	for _, name := range sortedKeys(c.Ports) {
		if e := c.Ports[name].Env; e != "" {
			out = append(out, EnvWriter{toml.Key{"ports", name, "env"}, e})
		}
	}
	return out
}

// Invalid is a config error about key, naming the file that set it, for problems only a
// command can see, such as a key main's .env doesn't set.
func (c *Config) Invalid(key []string, format string, args ...any) error {
	return apperr.New(apperr.ConfigInvalid, "%s: %s: %s", c.fileOf(key), toml.Key(key), fmt.Sprintf(format, args...))
}

// fileOf names the file that set key, or for a table, the file that set any key in it.
func (c *Config) fileOf(key toml.Key) string {
	k := key.String()
	if f, ok := c.origin[k]; ok {
		return f
	}
	for _, o := range sortedKeys(c.origin) {
		if strings.HasPrefix(o, k+".") {
			return c.origin[o]
		}
	}
	return "config"
}

type portTable struct {
	defined  map[string]bool
	assigned map[string]int
	policy   map[string]string
}

// portTable lists the names {port.<name>} can use. Only assign ports and [ports]
// entries get a stable value; a service with one assigned port is reachable by its own
// name, and every assigned port by "<service>:<container port>".
func (c *Config) portTable() portTable {
	t := portTable{defined: map[string]bool{}, assigned: map[string]int{}, policy: map[string]string{}}
	for k, p := range c.Compose.Ports {
		svc, _, _ := strings.Cut(k, ":")
		if p == "assign" {
			t.defined[k] = true
			t.assigned[svc]++
		} else {
			t.policy[k] = p
			if _, seen := t.policy[svc]; !seen {
				t.policy[svc] = p
			}
		}
	}
	for svc, n := range t.assigned {
		if n == 1 {
			t.defined[svc] = true
		}
	}
	for name := range c.Ports {
		t.defined[name] = true
	}
	return t
}

func (t portTable) undefined(name string) string {
	switch {
	case t.defined[name]:
		return ""
	case t.assigned[name] > 1:
		return fmt.Sprintf("service %s has more than one assigned port; name one as {port.%s:<container port>}", name, name)
	case t.policy[name] != "":
		return fmt.Sprintf("port %s has policy %q, and only \"assign\" ports have a stable value", name, t.policy[name])
	default:
		return fmt.Sprintf("define %s under [compose.ports] with \"assign\" or under [ports]", name)
	}
}

// PortValues adds to assigned, keyed "<service>:<container port>" or by [ports] name,
// the {port.<service>} alias of every service with exactly one assign entry.
func (c *Config) PortValues(assigned map[string]int) map[string]int {
	t := c.portTable()
	out := make(map[string]int, len(assigned))
	for k, v := range assigned {
		out[k] = v
	}
	for k, p := range c.Compose.Ports {
		svc, _, _ := strings.Cut(k, ":")
		if v, ok := assigned[k]; ok && p == "assign" && t.assigned[svc] == 1 {
			out[svc] = v
		}
	}
	return out
}

func describe(allowed []string) string {
	out := make([]string, len(allowed))
	for i, a := range allowed {
		if a == "port" {
			out[i] = "{port.<name>}"
		} else {
			out[i] = "{" + a + "}"
		}
	}
	return strings.Join(out, ", ")
}

// Effective is the configuration's templated values for one berth. Unresolved holds
// the templates that name a port with no value yet, keyed "url" or "env.<KEY>".
type Effective struct {
	Name       string
	Host       string
	URL        string
	Env        map[string]string
	Unresolved map[string]string
}

// Resolve expands every templated value for the berth named name.
func (c *Config) Resolve(name string, ports map[string]int) Effective {
	e := Effective{Name: name, Env: map[string]string{}, Unresolved: map[string]string{}}
	v := Values{Name: name, Ports: ports}
	hostParts, _ := parseTemplate(c.Host())
	e.Host, _ = expand(hostParts, v)
	v.Host = e.Host

	urlParts, _ := parseTemplate(c.URL())
	url, urlOK := expand(urlParts, v)
	if urlOK {
		e.URL, v.URL = url, url
	} else {
		e.Unresolved["url"] = c.URL()
	}

	for _, k := range c.Env.Host {
		e.Env[k] = e.Host
	}
	for _, k := range c.Env.URL {
		if urlOK {
			e.Env[k] = e.URL
		} else {
			e.Unresolved["env."+k] = "{url}"
		}
	}
	// env.set comes last so a key templated there wins over the same key listed under
	// env.url or env.host, whichever layer each came from.
	for k, tmpl := range c.Env.Set {
		delete(e.Unresolved, "env."+k)
		delete(e.Env, k)
		parts, _ := parseTemplate(tmpl)
		out, ok := expand(parts, v)
		if ok && (urlOK || !uses(parts, "url")) {
			e.Env[k] = out
		} else {
			e.Unresolved["env."+k] = tmpl
		}
	}
	return e
}

func uses(parts []part, token string) bool {
	for _, p := range parts {
		if p.token == token {
			return true
		}
	}
	return false
}

func oneOf(s string, set []string) bool {
	for _, v := range set {
		if s == v {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
