package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/hostdb"
	"github.com/kodnificent/berth/internal/mysql"
	"github.com/kodnificent/berth/internal/names"
	"github.com/kodnificent/berth/internal/postgres"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/volume"
	"github.com/kodnificent/berth/internal/worktree"
)

const (
	// minSegment keeps some of the berth's name in its segment beside the _2 to _1000
	// that maxCandidates allows.
	minSegment = 8
	// maxCandidates bounds the names tried under the registry lock.
	maxCandidates = 1000
	kindDatabase  = "database"
	// dbCreating is a database recorded as the berth's and not yet made.
	dbCreating = "creating"
)

// dbSetup is what add reads from the config and main's .env about the host database
// server, with a connection to it.
type dbSetup struct {
	cfg       *config.Config
	host      *config.HostDB
	engine    hostdb.Engine
	server    hostdb.Server
	conn      hostdb.Conn
	envPrefix string
	urlKey    string
	// urlDatabase is the database main's URL names, empty without a URL key.
	urlDatabase string
	// source is main's database prefix, empty without a prefix key in the config.
	source    string
	prefixKey string
	mainEnv   map[string]string
}

func engineFor(rec registry.Record) hostdb.Engine {
	if rec.Postgres != nil {
		return postgres.Engine
	}
	return mysql.Engine
}

func engineNamed(table string) hostdb.Engine {
	if table == postgres.Engine.Table {
		return postgres.Engine
	}
	return mysql.Engine
}

func setDB(rec *registry.Record, table string, m *registry.HostDB) {
	if table == postgres.Engine.Table {
		rec.Postgres = m
		return
	}
	rec.MySQL = m
}

func locationOf(rec registry.Record, m *registry.HostDB) hostdb.Location {
	return engineFor(rec).Location(m.EnvPrefix, m.URLKey)
}

func recordedServer(m *registry.HostDB) hostdb.Server {
	return hostdb.Server{Host: m.Host, Port: m.Port}
}

// readEnvFile parses the .env at path; a missing file is an empty one. A syntax error
// names the file and line only: the parser quotes the value it stopped in, which may be
// a password.
func readEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	env, err := dotenv.UnmarshalBytesWithLookup(data, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: line %d is not valid .env syntax", path, badLine(data))
	}
	return env, nil
}

// badLine is the line a .env stops parsing at: one past the longest run of whole lines
// from the top that parses, so a quote left open is blamed on the line that opens it.
func badLine(data []byte) int {
	lines := strings.SplitAfter(string(data), "\n")
	for k := len(lines) - 1; k > 0; k-- {
		if _, err := dotenv.UnmarshalWithLookup(strings.Join(lines[:k], ""), nil); err == nil {
			return k + 1
		}
	}
	return 1
}

func readMainEnv(w *worktree.Worktree, engine hostdb.Engine) (map[string]string, error) {
	path := filepath.Join(w.Repository, ".env")
	env, err := readEnvFile(path)
	if err != nil {
		return nil, apperr.New(apperr.Generic, "reading main's %s for the %s server [state.%s] from names: %v", path, engine.Name, engine.Table, err)
	}
	return env, nil
}

// prepareDatabases checks the config's host database server against main's .env and
// connects to the server it names. It runs before the berth is registered, so a config
// that can't work creates nothing. It is nil without a host database server.
func prepareDatabases(w *worktree.Worktree, cfg *config.Config) (*dbSetup, error) {
	m, table := cfg.HostDB()
	if m == nil {
		return nil, nil
	}
	engine := engineNamed(table)
	env, err := readMainEnv(w, engine)
	if err != nil {
		return nil, err
	}
	d := &dbSetup{cfg: cfg, host: m, engine: engine, envPrefix: m.EnvPrefix(), urlKey: m.URLKey(), prefixKey: m.PrefixKey(), mainEnv: env}
	envPath := filepath.Join(w.Repository, ".env")
	server, missing, err := d.location().Server(env)
	if len(missing) > 0 {
		return nil, cfg.Invalid([]string{"state", table, "from"}, "main's .env (%s) sets no %s, so berth can't tell which %s server main uses; set them there or fix from", envPath, strings.Join(missing, ", "), engine.Name)
	}
	if err != nil {
		return nil, cfg.Invalid([]string{"state", table, "from"}, "in main's .env (%s), %v", envPath, err)
	}
	d.server = server
	if d.urlKey != "" {
		if d.urlDatabase, err = d.location().URLDatabase(env); err != nil {
			return nil, cfg.Invalid([]string{"state", table, "from"}, "in main's .env (%s), %v", envPath, err)
		}
	}
	if d.prefixKey != "" {
		d.source = env[d.prefixKey]
		if d.source == "" {
			return nil, cfg.Invalid([]string{"state", table, "prefix"}, "main's .env (%s) sets no %s, or sets it empty, so berth has no prefix of main's to build the berth's from", envPath, d.prefixKey)
		}
	}
	login, _ := d.location().Login(env)
	if d.conn, err = engine.Open(server, login); err != nil {
		return nil, apperr.New(apperr.Generic, "%v; start it, or point main's .env at the server main uses, and run this again", err)
	}
	if err := d.checkWriters(); err != nil {
		d.conn.Close()
		return nil, err
	}
	return d, nil
}

func (d *dbSetup) location() hostdb.Location { return d.engine.Location(d.envPrefix, d.urlKey) }

// connection is the MySQL connection dumps and snapshots read through, nil without one.
func (d *dbSetup) connection() *mysql.Conn {
	if d == nil {
		return nil
	}
	conn, _ := d.conn.(*mysql.Conn)
	return conn
}

// hostConn is the connection to the server, nil without one.
func (d *dbSetup) hostConn() hostdb.Conn {
	if d == nil {
		return nil
	}
	return d.conn
}

func (d *dbSetup) close() {
	if d != nil && d.conn != nil {
		d.conn.Close()
	}
}

// checkWriters refuses a config entry that would write a key berth writes for the
// berth's databases, since the two would fight over its value, and a URL key naming a
// database the config gives no strategy, which would leave the berth on main's database.
func (d *dbSetup) checkWriters() error {
	all, err := d.conn.Databases()
	if err != nil {
		return apperr.New(apperr.Generic, "%v", err)
	}
	keys := map[string]string{}
	dbs := d.sources(all, nil, nil)
	if d.urlKey != "" && !d.urlListed(dbs) {
		if _, under := strings.CutPrefix(d.urlDatabase, d.source); d.source != "" && !under {
			return d.cfg.Invalid([]string{"state", d.engine.Table, "databases"}, "main's %s names database %s, which is not under main's prefix %s, so no entry here maps to it and the berth would keep using main's database; point %s at a database under the prefix and list it, or remove prefix", d.urlKey, d.urlDatabase, d.source, d.urlKey)
		}
		return d.cfg.Invalid([]string{"state", d.engine.Table, "databases"}, "main's %s names database %s, which has no entry here, so the berth would keep using main's database; list it, with skip to leave it shared", d.urlKey, d.urlDatabase)
	}
	if d.urlKey != "" {
		if i := slices.IndexFunc(dbs, func(db registry.Database) bool { return db.Source == d.urlDatabase }); i >= 0 && dbs[i].Strategy != config.None {
			keys[d.urlKey] = d.urlDatabase
		}
	}
	for _, db := range dbs {
		if db.Strategy != config.None {
			for _, k := range d.envKeys(db.Source) {
				keys[k] = db.Source
			}
		}
	}
	var problems []string
	for _, wr := range d.cfg.EnvWriters() {
		if src, ok := keys[wr.Env]; ok {
			problems = append(problems, d.cfg.Invalid(wr.Key, "berth writes %s with the berth's own database name, since main's .env names database %s in it; remove it", wr.Env, src).Error())
		}
	}
	if len(problems) > 0 {
		return apperr.New(apperr.ConfigInvalid, "%s", strings.Join(problems, "\n"))
	}
	return nil
}

// urlListed reports whether the database main's URL names is the source of one of dbs,
// or skipped by name. With a prefix, a key names the database under it, never one
// outside that happens to share the key's name.
func (d *dbSetup) urlListed(dbs []registry.Database) bool {
	if slices.ContainsFunc(dbs, func(db registry.Database) bool { return db.Source == d.urlDatabase }) {
		return true
	}
	key, under := strings.CutPrefix(d.urlDatabase, d.source)
	_, listed := d.host.Databases[key]
	return under && listed
}

// envKeys are the keys in main's .env whose value is the database name, apart from the
// server's own keys.
func (d *dbSetup) envKeys(name string) []string {
	skip := map[string]bool{d.prefixKey: true}
	for _, k := range d.location().ServerKeys() {
		skip[k] = true
	}
	var out []string
	for _, k := range sortedKeys(d.mainEnv) {
		if !skip[k] && d.mainEnv[k] == name {
			out = append(out, k)
		}
	}
	return out
}

// sources lists main's databases the config gives a strategy other than skip, without
// their targets. With a prefix, they are the server's databases under main's prefix,
// leaving out any berth's: those under a recorded prefix, and those whose part after
// main's prefix holds two underscores, as only a berth's prefix does, unless listed by
// name. Databases listed by name are included whether the server has them yet or not.
func (d *dbSetup) sources(all []string, others []registry.Record, own *registry.HostDB) []registry.Database {
	var out []registry.Database
	add := func(key, source string) {
		entry := d.host.Entry(key)
		if entry.Strategy == config.Skip || slices.ContainsFunc(out, func(db registry.Database) bool { return db.Source == source }) {
			return
		}
		out = append(out, registry.Database{Key: key, Source: source, Strategy: entry.Strategy, File: entry.File, Command: entry.Command})
	}
	listed := d.host.Listed()
	if d.source == "" {
		for _, k := range listed {
			add(k, k)
		}
		return out
	}
	var prefixes []string
	if own != nil && own.Prefix != "" {
		prefixes = append(prefixes, own.Prefix)
	}
	for _, o := range others {
		if m := o.DB(); m != nil && m.Prefix != "" && d.sameServer(m) {
			prefixes = append(prefixes, m.Prefix)
		}
	}
	sorted := slices.Clone(all)
	sort.Strings(sorted)
	for _, name := range sorted {
		key, ok := strings.CutPrefix(name, d.source)
		if !ok || key == "" || slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(name, p) }) {
			continue
		}
		if strings.Contains(key, "__") && !slices.Contains(listed, key) {
			continue
		}
		add(key, name)
	}
	for _, k := range listed {
		add(k, d.source+k)
	}
	return out
}

func (d *dbSetup) sameServer(m *registry.HostDB) bool {
	return d.server.Same(recordedServer(m))
}

// plan records in rec the berth's databases on the server, choosing its segment and
// prefix when it has none. It runs in provision, under the registry lock, so others is
// every other berth and the choice is saved before any database is created.
func (d *dbSetup) plan(rec *registry.Record, others []registry.Record) error {
	if d == nil {
		return nil
	}
	if m := rec.DB(); m != nil {
		if recorded := engineFor(*rec); recorded.Table != d.engine.Table {
			return apperr.New(apperr.Generic, "berth %s's databases are on a %s server, and the config now names a %s one, so add changed nothing; run berth remove and berth add to move the berth", rec.ID, recorded.Name, d.engine.Name)
		}
		recorded := recordedServer(m)
		if !d.server.Same(recorded) {
			return apperr.New(apperr.Generic, "main's .env now points at the %s server at %s, but berth %s's databases are on %s, so add changed nothing; point main's .env back at %s, or run berth remove and berth add to move the berth", d.engine.Name, d.server, rec.ID, recorded, recorded)
		}
		switch {
		case m.Prefix != "" && d.source == "":
			return apperr.New(apperr.Generic, "berth %s's databases are under the prefix %s, and [state.%s] no longer sets prefix, so add changed nothing; run berth remove and berth add to name them again", rec.ID, m.Prefix, d.engine.Table)
		case m.Prefix == "" && d.source != "":
			return apperr.New(apperr.Generic, "berth %s's databases were named without a prefix, and [state.%s] now sets prefix, so add changed nothing; run berth remove and berth add to name them again", rec.ID, d.engine.Table)
		}
	}
	all, err := d.conn.Databases()
	if err != nil {
		return apperr.New(apperr.Generic, "%v", err)
	}
	dbs := d.sources(all, others, rec.DB())
	if rec.DB() == nil {
		seg, prefix, err := d.choose(rec.Name, dbs, all, others)
		if err != nil {
			return err
		}
		setDB(rec, d.engine.Table, &registry.HostDB{Host: d.server.Host, Port: d.server.Port, Segment: seg, Prefix: prefix})
	}
	m := rec.DB()
	m.EnvPrefix, m.URLKey = d.envPrefix, d.urlKey
	if m.Prefix != "" {
		m.PrefixKey = d.prefixKey
	}
	var kept []registry.Database
	for _, old := range m.Databases {
		if old.Strategy != config.None {
			kept = append(kept, old)
		}
	}
	h := d.held(others)
	for _, db := range dbs {
		if db.Strategy != config.None {
			db.Env = d.envKeys(db.Source)
			db.URL = d.urlKey != "" && db.Source == d.urlDatabase
		}
		db.Status = dbCreating
		if i := slices.IndexFunc(kept, func(o registry.Database) bool { return o.Source == db.Source }); i >= 0 && db.Strategy != config.None {
			db.Target, db.Status, db.Error, db.Bytes = kept[i].Target, kept[i].Status, kept[i].Error, kept[i].Bytes
			kept[i] = db
			continue
		}
		db.Target = d.target(m, db)
		if m.Prefix == "" && db.Strategy != config.None {
			var err error
			if db.Target, err = d.freeName(rec.Name, db, all, h, kept); err != nil {
				return err
			}
		}
		kept = append(kept, db)
	}
	m.Databases = kept
	return nil
}

// held is what other berths on the server recorded as theirs.
type held struct{ prefixes, targets []string }

func (d *dbSetup) held(others []registry.Record) held {
	var h held
	for _, o := range others {
		m := o.DB()
		if m == nil || !d.sameServer(m) {
			continue
		}
		if m.Prefix != "" {
			h.prefixes = append(h.prefixes, m.Prefix)
		}
		for _, t := range m.Databases {
			if t.Strategy != config.None {
				h.targets = append(h.targets, t.Target)
			}
		}
	}
	return h
}

// nameTaken reports whether an exact name is the server's already, another berth's, or
// one of own.
func nameTaken(t string, all []string, h held, own []registry.Database) bool {
	return slices.Contains(all, t) || slices.Contains(h.targets, t) ||
		slices.ContainsFunc(h.prefixes, func(q string) bool { return strings.HasPrefix(t, q) }) ||
		slices.ContainsFunc(own, func(o registry.Database) bool { return o.Target == t })
}

// freeName is the name a database newly listed for a berth without a prefix gets: its
// target from the berth's segment, else the first of the berth's other candidates that
// nothing holds, so a database made by hand or by another berth is never taken.
func (d *dbSetup) freeName(name string, db registry.Database, all []string, h held, own []registry.Database) (string, error) {
	if !nameTaken(db.Target, all, h, own) {
		return db.Target, nil
	}
	var out string
	tries := 0
	names.DatabaseCandidates(name, names.MaxName, func(seg string) bool {
		tries++
		if t := names.Database(db.Source, seg, d.engine.MaxName); !nameTaken(t, all, h, own) {
			out = t
			return false
		}
		return tries < maxCandidates
	})
	if out == "" {
		return "", apperr.New(apperr.Generic, "berth found no free name for database %s on the %s server at %s in %d tries; drop leftover databases there", db.Source, d.engine.Name, d.server, maxCandidates)
	}
	return out, nil
}

func (d *dbSetup) target(m *registry.HostDB, db registry.Database) string {
	switch {
	case db.Strategy == config.None:
		return db.Source
	case m.Prefix != "":
		return m.Prefix + db.Key
	default:
		return names.Database(db.Source, m.Segment, d.engine.MaxName)
	}
}

// choose picks the berth's segment, and with a prefix configured its prefix: the first
// candidate whose names nothing on the server holds and no other berth on the server
// recorded, where a prefix is held when it begins, or is begun by, another's.
func (d *dbSetup) choose(name string, dbs []registry.Database, all []string, others []registry.Record) (seg, prefix string, err error) {
	h := d.held(others)
	prefixes, targets := h.prefixes, h.targets
	segMax := names.MaxName
	if d.source != "" {
		longest := ""
		for _, db := range dbs {
			if db.Strategy != config.None && len(db.Key) > len(longest) {
				longest = db.Key
			}
		}
		room := d.engine.MaxName - len(d.source) - len("__") - len(longest)
		if room < minSegment {
			return "", "", d.cfg.Invalid([]string{"state", d.engine.Table, "prefix"}, "main's prefix %q and database %s leave fewer than %d of %s's %d characters for the berth's part of the name; shorten main's prefix or database %s", d.source, d.source+longest, minSegment, d.engine.Name, d.engine.MaxName, longest)
		}
		segMax = min(segMax, room)
	}
	begins := func(list []string, p string) bool {
		return slices.ContainsFunc(list, func(n string) bool { return strings.HasPrefix(n, p) })
	}
	free := func(seg string) bool {
		if d.source != "" {
			p := names.DatabasePrefix(d.source, seg)
			return !begins(all, p) && !begins(prefixes, p) && !begins(targets, p) &&
				!slices.ContainsFunc(prefixes, func(q string) bool { return strings.HasPrefix(p, q) })
		}
		var mine []registry.Database
		for _, db := range dbs {
			if db.Strategy == config.None {
				continue
			}
			t := names.Database(db.Source, seg, d.engine.MaxName)
			if nameTaken(t, all, h, mine) {
				return false
			}
			mine = append(mine, registry.Database{Target: t})
		}
		return true
	}
	tries := 0
	names.DatabaseCandidates(name, segMax, func(s string) bool {
		tries++
		if free(s) {
			seg = s
			return false
		}
		return tries < maxCandidates
	})
	if seg == "" {
		return "", "", apperr.New(apperr.Generic, "berth found no free database names for %s on the %s server at %s in %d tries; drop leftover databases there, or remove berths you no longer need", name, d.engine.Name, d.server, maxCandidates)
	}
	if d.source != "" {
		prefix = names.DatabasePrefix(d.source, seg)
	}
	return seg, prefix, nil
}

// create makes the berth's fresh databases that don't exist yet, after the
// record naming them is saved. A database that can't be made is reported and recorded
// as failed rather than returned, as a failed volume clone is: the berth is usable
// without it, and status says why. Copies of main's are made by the clone process.
func (d *dbSetup) create(stderr io.Writer, rec *registry.Record) {
	if d == nil || rec.DB() == nil {
		return
	}
	for i := range rec.DB().Databases {
		db := &rec.DB().Databases[i]
		switch {
		case clonedStrategy(db.Strategy):
			continue
		case db.Strategy == config.None:
			db.Status, db.Error = volume.Ready, ""
			continue
		}
		if err := d.conn.Create(db.Target); err != nil {
			db.Status, db.Error = volume.Failed, err.Error()
			fmt.Fprintf(stderr, "berth: %v\n", err)
			continue
		}
		db.Status, db.Error = volume.Ready, ""
		if n, err := d.conn.Size(db.Target); err == nil {
			db.Bytes = n
		}
	}
}

// databaseFailed is add --wait's failure when a database could not be made.
func databaseFailed(rec registry.Record) error {
	if rec.DB() == nil {
		return nil
	}
	var failed []string
	for _, db := range rec.DB().Databases {
		if db.Status == volume.Failed && !clonedStrategy(db.Strategy) {
			failed = append(failed, fmt.Sprintf("%s: %s", db.Target, db.Error))
		}
	}
	if len(failed) > 0 {
		return apperr.New(apperr.StateCloneFailed, "creating failed for database %s", strings.Join(failed, "; database "))
	}
	return nil
}

// databaseEnv is the keys berth writes for the berth's databases: the prefix key, and
// each key main's .env names a database in that the berth has its own of.
func databaseEnv(rec registry.Record, env map[string]string) {
	m := rec.DB()
	if m == nil {
		return
	}
	if m.Prefix != "" && m.PrefixKey != "" {
		env[m.PrefixKey] = m.Prefix
	}
	for _, db := range m.Databases {
		if db.Strategy == config.None {
			continue
		}
		for _, k := range db.Env {
			env[k] = db.Target
		}
		if db.URL {
			if u, ok := berthURL(rec, m, db.Target); ok {
				env[m.URLKey] = u
			}
		}
	}
}

// berthURL is the URL the berth's .env holds for its database target. The record never
// holds a URL, since it carries the login, so berth reads main's URL as add copied it, and
// failing that the one the berth already has. A URL that no longer names the recorded
// server is passed over, so a moved main can't point a berth elsewhere.
func berthURL(rec registry.Record, m *registry.HostDB, target string) (string, bool) {
	loc := locationOf(rec, m)
	recorded := recordedServer(m)
	for _, dir := range []string{rec.Repository, rec.Worktree} {
		env, err := readEnvFile(filepath.Join(dir, ".env"))
		if err != nil {
			continue
		}
		if server, missing, err := loc.Server(env); err == nil && len(missing) == 0 && server.Same(recorded) {
			return hostdb.WithDatabase(env[m.URLKey], target), true
		}
	}
	return "", false
}

// secretEnvKey is the managed key whose value holds a login, empty when the berth has
// none: everything shown to the user prints that value through hostdb.Redacted.
func secretEnvKey(rec registry.Record) string {
	if m := rec.DB(); m != nil {
		return m.URLKey
	}
	return ""
}

// redactEnv is env with the value of the managed key holding a login made fit to print.
func redactEnv(rec registry.Record, env map[string]string) map[string]string {
	key := secretEnvKey(rec)
	if _, ok := env[key]; !ok || key == "" {
		return env
	}
	out := maps.Clone(env)
	out[key] = hostdb.Redacted(env[key])
	return out
}

// dbTeardown is the berth's databases a teardown drops, with what it needs to reach
// their server.
type dbTeardown struct {
	server hostdb.Server
	login  hostdb.Login
}

// planDatabases lists the berth's databases the recorded server still has: those the
// record names and any under the recorded prefix. It reads only the record and the
// login, from the berth's own .env or else the environment berth runs in, never main's
// .env or the config, so neither can redirect a drop or block one.
func (td *teardown) planDatabases() error {
	m := td.rec.DB()
	if m == nil || (m.Prefix == "" && !slices.ContainsFunc(m.Databases, func(db registry.Database) bool { return db.Strategy != config.None })) {
		return nil
	}
	server := recordedServer(m)
	engine := engineFor(td.rec)
	loc := locationOf(td.rec, m)
	own, _ := readEnvFile(filepath.Join(td.rec.Worktree, ".env"))
	login, ok := loc.Login(own, environ())
	if !ok {
		keys := loc.LoginKeys()
		return apperr.New(apperr.Generic, "berth %s's databases are on the %s server at %s, and berth reads the login for it from the berth's .env, which sets no %s, so nothing was removed; export %s and run this again", td.rec.ID, engine.Name, server, keys[0], strings.Join(keys, " and "))
	}
	conn, err := engine.Open(server, login)
	if err != nil {
		return apperr.New(apperr.Generic, "%v, so berth can't drop berth %s's databases there and nothing was removed; start the server and run this again", err, td.rec.ID)
	}
	defer conn.Close()
	all, err := conn.Databases()
	if err != nil {
		return apperr.New(apperr.Generic, "%v, so nothing was removed", err)
	}
	sort.Strings(all)
	for _, name := range all {
		if m.Owns(name) {
			td.items = append(td.items, item{kindDatabase, name})
		}
	}
	td.db = &dbTeardown{server, login}
	return nil
}

func environ() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env
}

// dropDatabase drops one of the berth's databases, connecting on first use. It refuses
// anything the record doesn't own, whatever put the name on the list.
func (td *teardown) dropDatabase(name string) error {
	if !td.rec.DB().Owns(name) {
		return fmt.Errorf("database %s is not recorded as berth %s's own, so berth won't drop it", name, td.rec.ID)
	}
	if err := td.connect(); err != nil {
		return err
	}
	return td.dbConn.Drop(name)
}

func (td *teardown) connect() error {
	if td.dbConn != nil {
		return nil
	}
	conn, err := engineFor(td.rec).Open(td.db.server, td.db.login)
	if err != nil {
		return err
	}
	td.dbConn = conn
	return nil
}

// endCopies ends any template copy the clone state still names before anything is
// dropped, whether or not the server lists its database yet: one still uncommitted is
// invisible, and would commit after the record is gone. A database such a copy finished
// meanwhile joins the drops.
func (td *teardown) endCopies() {
	if td.db == nil || td.rec.Postgres == nil {
		return
	}
	st, err := volume.Load(td.rec.ID)
	if err != nil || !slices.ContainsFunc(st.Databases, func(e volume.Entry) bool { return e.CopySession != nil }) {
		return
	}
	if td.connect() != nil {
		return
	}
	endCopySessions(td.dbConn, st)
	all, err := td.dbConn.Databases()
	if err != nil {
		return
	}
	sort.Strings(all)
	for _, name := range all {
		if td.rec.DB().Owns(name) && !slices.Contains(td.items, item{kindDatabase, name}) {
			td.items = append(td.items, item{kindDatabase, name})
		}
	}
}

// dbReset is what reset discards and makes again on the berth's server.
type dbReset struct {
	conn hostdb.Conn
	drop []string
}

// planDatabaseReset checks that main's .env still points at the server the record names
// and lists the berth's databases there, changing nothing. It is nil when the record has
// none to reset.
func planDatabaseReset(w *worktree.Worktree, rec registry.Record) (*dbReset, error) {
	m := rec.DB()
	if m == nil || (m.Prefix == "" && !slices.ContainsFunc(m.Databases, func(db registry.Database) bool { return db.Strategy != config.None })) {
		return nil, nil
	}
	engine := engineFor(rec)
	loc := locationOf(rec, m)
	recorded := recordedServer(m)
	env, err := readMainEnv(w, engine)
	if err != nil {
		return nil, err
	}
	server, missing, err := loc.Server(env)
	switch {
	case len(missing) > 0:
		return nil, apperr.New(apperr.Generic, "main's .env sets no %s, so berth can't tell that it still points at the %s server at %s that berth %s's databases are on; reset discarded nothing", strings.Join(missing, ", "), engine.Name, recorded, rec.ID)
	case err != nil:
		return nil, apperr.New(apperr.Generic, "in main's .env, %v; reset discarded nothing", err)
	case !server.Same(recorded):
		return nil, apperr.New(apperr.Generic, "main's .env now points at the %s server at %s, but berth %s's databases are on %s, so reset discarded nothing; point main's .env back at %s, or run berth remove and berth add to move the berth", engine.Name, server, rec.ID, recorded, recorded)
	}
	if err := refuseUnrestorable(rec, recorded); err != nil {
		return nil, err
	}
	login, _ := loc.Login(env)
	conn, err := engine.Open(recorded, login)
	if err != nil {
		return nil, apperr.New(apperr.Generic, "%v, so reset discarded nothing; start the server and run berth reset again", err)
	}
	all, err := conn.Databases()
	if err != nil {
		conn.Close()
		return nil, apperr.New(apperr.Generic, "%v; reset discarded nothing", err)
	}
	r := &dbReset{conn: conn}
	sort.Strings(all)
	for _, name := range all {
		if m.Owns(name) {
			r.drop = append(r.drop, name)
		}
	}
	return r, nil
}

func (r *dbReset) close() {
	if r != nil {
		r.conn.Close()
	}
}

// hostConn is the connection to the server, nil without one.
func (r *dbReset) hostConn() hostdb.Conn {
	if r == nil {
		return nil
	}
	return r.conn
}

func (r *dbReset) discarded() []discarded {
	if r == nil {
		return nil
	}
	var out []discarded
	for _, name := range r.drop {
		out = append(out, discarded{Kind: kindDatabase, Name: name, Target: name})
	}
	return out
}

// execute drops every database of the berth's on the server and makes its fresh ones
// again, empty, recording in rec how each stands.
func (r *dbReset) execute(stderr io.Writer, rec *registry.Record) ([]discarded, error) {
	if r == nil {
		return nil, nil
	}
	var gone []discarded
	for _, name := range r.drop {
		if !rec.DB().Owns(name) {
			return gone, apperr.New(apperr.Generic, "database %s is not recorded as berth %s's own, so reset won't drop it", name, rec.ID)
		}
		if err := r.conn.Drop(name); err != nil {
			return gone, apperr.New(apperr.Generic, "%v; run berth reset again", err)
		}
		gone = append(gone, discarded{Kind: kindDatabase, Name: name, Target: name})
	}
	for i := range rec.DB().Databases {
		db := &rec.DB().Databases[i]
		if db.Strategy != config.Fresh {
			continue
		}
		db.Bytes = 0
		if err := r.conn.Create(db.Target); err != nil {
			db.Status, db.Error = volume.Failed, err.Error()
			fmt.Fprintf(stderr, "berth: %v\n", err)
			continue
		}
		db.Status, db.Error = volume.Ready, ""
	}
	return gone, nil
}

// databaseFindings checks a berth's databases: values in its .env that still name one of
// main's databases the berth has its own of, a database shared with main through none,
// and main's .env pointing at another server than the one the berth's databases are on.
func databaseFindings(w *worktree.Worktree, rec registry.Record) []finding {
	m := rec.DB()
	if m == nil {
		return nil
	}
	engine := engineFor(rec)
	loc := locationOf(rec, m)
	var out []finding
	sources := map[string]string{}
	urlTarget := ""
	for _, db := range m.Databases {
		if db.URL {
			urlTarget = db.Target
		}
		if db.Strategy == config.None {
			out = append(out, finding{"database", warning, "database " + db.Source,
				fmt.Sprintf("the berth uses main's database %s itself ([state.%s] none), so state is shared: a migration or test run here changes main's data", db.Source, engine.Table), nil})
			continue
		}
		sources[db.Source] = db.Target
	}
	skip := map[string]bool{m.PrefixKey: true}
	for _, k := range loc.ServerKeys() {
		skip[k] = true
	}
	if env, err := readEnvFile(filepath.Join(w.Path, ".env")); err == nil {
		for _, k := range sortedKeys(env) {
			if target, ok := sources[env[k]]; ok && !skip[k] {
				out = append(out, finding{"env", problem, ".env " + k,
					fmt.Sprintf("%s=%s still names main's database %s, so the app here may use main's data; the berth's own is %s", k, env[k], env[k], target), nil})
			}
		}
		if m.URLKey != "" {
			if name, err := loc.URLDatabase(env); err == nil && urlTarget != "" && name != urlTarget {
				out = append(out, finding{"env", problem, ".env " + m.URLKey,
					fmt.Sprintf("%s names database %s, not the berth's own %s, so the app here may use another database's data", m.URLKey, name, urlTarget), nil})
			}
		}
	}
	recorded := recordedServer(m)
	env, err := readMainEnv(w, engine)
	if err != nil {
		return append(out, finding{"database", notVerified, "main's .env", err.Error(), nil})
	}
	if server, missing, err := loc.Server(env); err == nil && len(missing) == 0 && !server.Same(recorded) {
		out = append(out, finding{"database", problem, "main's .env",
			fmt.Sprintf("main's .env now points at the %s server at %s, but the berth's databases are on %s, so berth reset refuses; point main's .env back, or run berth remove and berth add to move the berth", engine.Name, server, recorded), nil})
	}
	return out
}

// mainDatabaseFindings checks the config's host database server against main's .env in
// the main worktree, before any berth exists.
func mainDatabaseFindings(w *worktree.Worktree, cfg *config.Config) []finding {
	m, table := cfg.HostDB()
	if m == nil {
		return nil
	}
	engine := engineNamed(table)
	var out []finding
	for _, k := range m.Listed() {
		if m.Strategy(k) == config.None {
			out = append(out, finding{"database", warning, "[state." + table + ".databases] " + k,
				fmt.Sprintf("every berth will use main's database for %s itself, so state is shared: a migration or test run in a berth changes main's data", k), nil})
		}
	}
	if m.Default == config.None {
		out = append(out, finding{"database", warning, "[state." + table + "] default",
			"every berth will use main's databases not listed under [state." + table + ".databases] itself, so state is shared with main", nil})
	}
	env, err := readMainEnv(w, engine)
	if err != nil {
		return append(out, finding{"database", notVerified, "main's .env", err.Error(), nil})
	}
	if _, missing, err := engine.Location(m.EnvPrefix(), m.URLKey()).Server(env); len(missing) > 0 {
		out = append(out, finding{"config", problem, "[state." + table + "] from", fmt.Sprintf("main's .env sets no %s, so berth add can't tell which %s server main uses", strings.Join(missing, ", "), engine.Name), nil})
	} else if err != nil {
		out = append(out, finding{"config", problem, "[state." + table + "] from", err.Error(), nil})
	}
	if k := m.PrefixKey(); k != "" && env[k] == "" {
		out = append(out, finding{"config", problem, "[state." + table + "] prefix", fmt.Sprintf("main's .env sets no %s, or sets it empty, so berth add has no prefix of main's to build a berth's from", k), nil})
	}
	return out
}
