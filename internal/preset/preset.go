// Package preset loads the framework presets shipped in the binary and decides which of
// them apply to a repository. All framework knowledge lives in its data files.
package preset

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/initconf"
)

//go:embed *.toml
var data embed.FS

type term struct {
	File         string   `toml:"file"`
	Matches      string   `toml:"matches"`
	JSONObjects  []string `toml:"json_objects"`
	JSONKey      string   `toml:"json_key"`
	Env          string   `toml:"env"`
	Default      string   `toml:"default"`
	Values       []string `toml:"values"`
	NotPublished bool     `toml:"not_published"`
	Any          []term   `toml:"any"`

	re *regexp.Regexp
}

type env struct {
	Key     string `toml:"key"`
	Value   string `toml:"value"`
	Confirm []term `toml:"confirm"`
	Reason  string `toml:"reason"`
}

type port struct {
	Name    string `toml:"name"`
	Env     string `toml:"env"`
	Default int    `toml:"default"`
	Confirm []term `toml:"confirm"`
	Reason  string `toml:"reason"`
}

type mysql struct {
	From          string `toml:"from"`
	Default       string `toml:"default"`
	Strategy      string `toml:"strategy"`
	DatabaseEnv   string `toml:"database_env"`
	Confirm       []term `toml:"confirm"`
	Reason        string `toml:"reason"`
	Prefix        string `toml:"prefix"`
	PrefixConfirm []term `toml:"prefix_confirm"`
	PrefixReason  string `toml:"prefix_reason"`
}

type preset struct {
	Name    string `toml:"name"`
	About   string `toml:"about"`
	Applies []term `toml:"applies"`
	Env     []env  `toml:"env"`
	Ports   []port `toml:"ports"`
	MySQL   *mysql `toml:"mysql"`
}

type Repo struct {
	Root        string
	Env         map[string]string
	ComposeVars map[string]bool
	Services    []string
	Published   []int
}

func Apply(name string, repo Repo) ([]initconf.Preset, error) {
	all, err := load()
	if err != nil {
		return nil, err
	}
	var picked []preset
	for _, p := range all {
		if name == p.Name || name == "" && repo.all(p.Applies) {
			picked = append(picked, p)
		}
	}
	if name != "" && len(picked) == 0 {
		var names []string
		for _, p := range all {
			names = append(names, p.Name)
		}
		return nil, apperr.New(apperr.Usage, "unknown preset %q; berth ships %s", name, strings.Join(names, ", "))
	}
	return suggest(picked, repo), nil
}

func suggest(presets []preset, repo Repo) []initconf.Preset {
	var out []initconf.Preset
	seen := map[string]bool{}
	first := func(key string) bool {
		if seen[key] {
			return false
		}
		seen[key] = true
		return true
	}
	var table *initconf.PresetMySQL
	// Init doesn't connect to the server, so of the databases a table copies, only the
	// one main's .env names can be known here.
	copied := ""
	for _, p := range presets {
		if p.MySQL != nil {
			if table = repo.mysql(p.MySQL); table.Active {
				copied = repo.Env[p.MySQL.DatabaseEnv]
			}
			break
		}
	}
	for _, p := range presets {
		s := initconf.Preset{Name: p.Name, About: p.About}
		for _, e := range p.Env {
			if first(e.Key) {
				s.Env = append(s.Env, repo.env(e, copied))
			}
		}
		for _, pt := range p.Ports {
			if first(pt.Env) {
				s.Ports = append(s.Ports, repo.port(pt, copied))
			}
		}
		if p.MySQL != nil && table != nil {
			s.MySQL, table = table, nil
		}
		out = append(out, s)
	}
	return out
}

const (
	composeReads   = "Main's compose files read this key, and berth never changes a key they read."
	copiedDatabase = "Main's .env gives this key the name of a database berth copies, so berth writes the berth's own name to it and refuses a second writer."
)

func (r Repo) env(e env, copied string) initconf.PresetEnv {
	s := initconf.PresetEnv{Key: e.Key, Value: e.Value, Active: r.all(e.Confirm), Reason: e.Reason}
	switch {
	case r.ComposeVars[e.Key]:
		s.Active, s.Reason = false, composeReads
	case copied != "" && r.Env[e.Key] == copied:
		s.Active, s.Reason = false, copiedDatabase
	}
	return s
}

func (r Repo) port(p port, copied string) initconf.PresetPort {
	s := initconf.PresetPort{Name: p.Name, Env: p.Env, Main: p.Default, Active: r.all(p.Confirm), Reason: p.Reason}
	if n, err := strconv.Atoi(r.Env[p.Env]); err == nil {
		s.Main = n
	}
	switch {
	case r.ComposeVars[p.Env]:
		s.Active, s.Reason = false, composeReads
	case copied != "" && r.Env[p.Env] == copied:
		s.Active, s.Reason = false, copiedDatabase
	case slices.Contains(r.Services, p.Name):
		s.Active, s.Reason = false, fmt.Sprintf("A compose service is also named %s, so [compose.ports] covers it, and berth refuses a port with a service's name.", p.Name)
	}
	return s
}

var databaseName = regexp.MustCompile(`^[A-Za-z0-9_$-]+$`)

func (r Repo) mysql(m *mysql) *initconf.PresetMySQL {
	s := &initconf.PresetMySQL{
		Active: r.all(m.Confirm), Reason: m.Reason,
		From: m.From, Default: m.Default,
		Prefix: m.Prefix, PrefixActive: r.all(m.PrefixConfirm), PrefixReason: m.PrefixReason,
		Databases: map[string]string{},
	}
	name := r.Env[m.DatabaseEnv]
	if s.PrefixActive {
		s.Databases["*"] = m.Strategy
		prefixKey := strings.TrimPrefix(m.Prefix, "env:")
		if s.Active && name != "" && !strings.HasPrefix(name, r.Env[prefixKey]) {
			s.Active = false
			s.Reason = fmt.Sprintf("%s names a database outside %s, so a berth would keep using main's and a migration there would change main's data. Move it under the prefix, or drop the prefix and list the databases by name, then uncomment this.", m.DatabaseEnv, prefixKey)
		}
	} else if name != "" && databaseName.MatchString(name) {
		s.Databases[name] = m.Strategy
	}
	return s
}

func (r Repo) all(terms []term) bool {
	if len(terms) == 0 {
		return false
	}
	for _, t := range terms {
		if !r.holds(t) {
			return false
		}
	}
	return true
}

func (r Repo) holds(t term) bool {
	switch {
	case t.File != "":
		return r.fileHolds(t)
	case t.Env != "":
		v := r.Env[t.Env]
		if v == "" {
			v = t.Default
		}
		switch {
		case t.NotPublished:
			n, err := strconv.Atoi(v)
			return err != nil || !slices.Contains(r.Published, n)
		case len(t.Values) > 0:
			return slices.Contains(t.Values, v)
		default:
			return v != ""
		}
	default:
		return slices.ContainsFunc(t.Any, r.holds)
	}
}

// fileHolds checks files by pattern under Root; a file that can't be read holds nothing.
func (r Repo) fileHolds(t term) bool {
	root := os.DirFS(r.Root)
	matches, err := fs.Glob(root, t.File)
	if err != nil {
		return false
	}
	for _, name := range matches {
		info, err := fs.Stat(root, name)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if t.re == nil && t.JSONKey == "" {
			return true
		}
		content, err := fs.ReadFile(root, name)
		if err != nil {
			continue
		}
		if t.re != nil && t.re.Match(content) || t.JSONKey != "" && hasJSONKey(content, t.JSONObjects, t.JSONKey) {
			return true
		}
	}
	return false
}

func hasJSONKey(content []byte, objects []string, key string) bool {
	var top map[string]json.RawMessage
	if json.Unmarshal(content, &top) != nil {
		return false
	}
	for _, name := range objects {
		var object map[string]json.RawMessage
		if json.Unmarshal(top[name], &object) == nil {
			if _, ok := object[key]; ok {
				return true
			}
		}
	}
	return false
}

func load() ([]preset, error) {
	files, err := fs.Glob(data, "*.toml")
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	out := make([]preset, 0, len(files))
	for _, f := range files {
		p, err := parse(f)
		if err != nil {
			return nil, fmt.Errorf("the %s preset shipped with berth is invalid, which is a bug in berth: %w", f, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func parse(name string) (preset, error) {
	var p preset
	raw, err := data.ReadFile(name)
	if err != nil {
		return p, err
	}
	md, err := toml.Decode(string(raw), &p)
	if err != nil {
		return p, err
	}
	if extra := md.Undecoded(); len(extra) > 0 {
		return p, fmt.Errorf("unknown key %s", extra[0])
	}
	if p.Name == "" || p.Name+".toml" != name {
		return p, fmt.Errorf("name %q doesn't match the file", p.Name)
	}
	if len(p.Applies) == 0 {
		return p, fmt.Errorf("applies is empty")
	}
	for _, e := range p.Env {
		if e.Key == "" || e.Value == "" || e.Reason == "" {
			return p, fmt.Errorf("env %q needs key, value and reason", e.Key)
		}
	}
	for _, pt := range p.Ports {
		if pt.Name == "" || pt.Env == "" || pt.Reason == "" {
			return p, fmt.Errorf("port %q needs name, env and reason", pt.Name)
		}
	}
	if m := p.MySQL; m != nil && (m.From == "" || m.Strategy == "" || m.Reason == "" || m.Prefix != "" && m.PrefixReason == "") {
		return p, fmt.Errorf("mysql needs from, strategy, reason, and a prefix_reason with a prefix")
	}
	for _, list := range termLists(&p) {
		if err := compile(list); err != nil {
			return p, err
		}
	}
	return p, nil
}

// termLists is every condition list of p, sharing its storage, so compile can fill in
// their regular expressions.
func termLists(p *preset) [][]term {
	lists := [][]term{p.Applies}
	for _, e := range p.Env {
		lists = append(lists, e.Confirm)
	}
	for _, port := range p.Ports {
		lists = append(lists, port.Confirm)
	}
	if p.MySQL != nil {
		lists = append(lists, p.MySQL.Confirm, p.MySQL.PrefixConfirm)
	}
	return lists
}

func compile(terms []term) error {
	for i := range terms {
		t := &terms[i]
		set := 0
		for _, ok := range []bool{t.File != "", t.Env != "", len(t.Any) > 0} {
			if ok {
				set++
			}
		}
		if set != 1 {
			return fmt.Errorf("a condition needs exactly one of file, env and any: %+v", *t)
		}
		if (t.JSONKey != "") != (len(t.JSONObjects) > 0) || t.JSONKey != "" && t.Matches != "" {
			return fmt.Errorf("json_key and json_objects go together, without matches: %+v", *t)
		}
		if t.Matches != "" {
			re, err := regexp.Compile(t.Matches)
			if err != nil {
				return err
			}
			t.re = re
		}
		if err := compile(t.Any); err != nil {
			return err
		}
	}
	return nil
}
