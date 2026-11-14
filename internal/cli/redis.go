package cli

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/hostdb"
	"github.com/kodnificent/berth/internal/names"
	"github.com/kodnificent/berth/internal/redis"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/worktree"
)

// kindRedis is every key under a berth's Redis prefix, on every logical database.
const kindRedis = "redis_keys"

// redisSetup is what add reads from the config and main's .env about the host Redis
// server, with a connection to it.
type redisSetup struct {
	server    hostdb.Server
	conn      *redis.Conn
	envPrefix string
	prefixKey string
	// source is main's key prefix, never empty.
	source string
}

func redisServer(m *registry.Redis) hostdb.Server { return hostdb.Server{Host: m.Host, Port: m.Port} }

// prepareRedis checks [state.redis] against main's .env and connects to the server it
// names, before the berth is registered, so a setup that can't work creates nothing. It
// is nil without [state.redis].
func prepareRedis(w *worktree.Worktree, cfg *config.Config) (*redisSetup, error) {
	r := cfg.State.Redis
	if r == nil {
		return nil, nil
	}
	path := filepath.Join(w.Repository, ".env")
	env, err := readEnvFile(path)
	if err != nil {
		return nil, apperr.New(apperr.Generic, "reading main's %s for the Redis server [state.redis] from names: %v", path, err)
	}
	loc := redis.Location{Prefix: r.EnvPrefix()}
	server, missing, err := loc.Server(env)
	if len(missing) > 0 {
		return nil, cfg.Invalid([]string{"state", "redis", "from"}, "main's .env (%s) sets no %s, so berth can't tell which Redis server main uses; set it there or fix from", path, strings.Join(missing, ", "))
	}
	if err != nil {
		return nil, cfg.Invalid([]string{"state", "redis", "from"}, "in main's .env (%s), %v", path, err)
	}
	key := r.PrefixKey()
	source := env[key]
	if source == "" {
		return nil, apperr.New(apperr.Generic, "main's .env (%s) sets no %s, or sets it empty, so main's Redis keys could look like anything and no prefix of the berth's could be kept apart from them; set %s there to the prefix main's app puts in front of its keys, such as app_, and run berth add again", path, key, key)
	}
	conn, err := redis.Open(server, loc.Login(env))
	if err != nil {
		return nil, apperr.New(apperr.Generic, "%v; start it, or point main's .env at the server main uses, and run this again", err)
	}
	return &redisSetup{server: server, conn: conn, envPrefix: r.EnvPrefix(), prefixKey: key, source: source}, nil
}

func (r *redisSetup) close() {
	if r != nil {
		r.conn.Close()
	}
}

// plan records in rec the berth's key prefix, choosing it when the berth has none. It
// runs in provision, under the registry lock, so others is every other berth and the
// prefix is saved before it is written to the berth's .env.
func (r *redisSetup) plan(rec *registry.Record, others []registry.Record) error {
	if r == nil {
		return nil
	}
	if m := rec.Redis; m != nil {
		if recorded := redisServer(m); !r.server.Same(recorded) {
			return apperr.New(apperr.Generic, "main's .env now points at the Redis server at %s, but berth %s's keys are on %s, so add changed nothing; point main's .env back at %s, or run berth remove and berth add to move the berth", r.server, rec.ID, recorded, recorded)
		}
		m.EnvPrefix, m.PrefixKey = r.envPrefix, r.prefixKey
		return nil
	}
	seg, err := r.choose(rec.Name, others)
	if err != nil {
		return err
	}
	rec.Redis = &registry.Redis{Host: r.server.Host, Port: r.server.Port, EnvPrefix: r.envPrefix, Segment: seg,
		Prefix: names.RedisPrefix(seg, r.source), PrefixKey: r.prefixKey}
	return nil
}

// choose is the first segment whose prefix no other berth recorded, or begins or is
// begun by one that did, and that no key on any logical database is under. Every other
// berth's prefix counts, whatever server it names: two spellings of one server can't
// always be told apart, and a prefix shared across them would let one berth's removal
// delete the other's keys.
func (r *redisSetup) choose(name string, others []registry.Record) (string, error) {
	var held []string
	for _, o := range others {
		if m := o.Redis; m != nil && m.Prefix != "" {
			held = append(held, m.Prefix)
		}
	}
	var seg string
	var failed error
	tries := 0
	names.RedisCandidates(name, r.source, func(s string) bool {
		tries++
		p := names.RedisPrefix(s, r.source)
		if slices.ContainsFunc(held, func(q string) bool { return strings.HasPrefix(p, q) || strings.HasPrefix(q, p) }) {
			return tries < maxCandidates
		}
		taken, err := r.conn.AnyUnder([]byte(p))
		switch {
		case err != nil:
			failed = err
			return false
		case !taken:
			seg = s
			return false
		}
		return tries < maxCandidates
	})
	if failed != nil {
		return "", apperr.New(apperr.Generic, "%v, so add changed nothing", failed)
	}
	if seg == "" {
		return "", apperr.New(apperr.Generic, "berth found no free Redis key prefix for %s on the server at %s in %d tries; delete leftover keys there, or remove berths you no longer need", name, r.server, maxCandidates)
	}
	return seg, nil
}

// redisEnv is the key berth writes for the berth's Redis prefix.
func redisEnv(rec registry.Record, env map[string]string) {
	if m := rec.Redis; m != nil && m.Prefix != "" && m.PrefixKey != "" {
		env[m.PrefixKey] = m.Prefix
	}
}

// redisLogin is how a teardown reaches the berth's Redis server.
type redisLogin struct {
	server hostdb.Server
	login  hostdb.Login
}

// planRedis lists the berth's keys when the recorded server holds any under the recorded
// prefix. Like planDatabases, it reads only the record and the login, from the berth's
// own .env or else the environment berth runs in, never main's .env or the config.
func (td *teardown) planRedis() error {
	m := td.rec.Redis
	if m == nil || m.Prefix == "" {
		return nil
	}
	server := redisServer(m)
	loc := redis.Location{Prefix: m.EnvPrefix}
	own, _ := readEnvFile(filepath.Join(td.rec.Worktree, ".env"))
	login := loc.Login(own, environ())
	conn, err := redis.Open(server, login)
	if err != nil {
		return apperr.New(apperr.Generic, "%v, so berth can't delete berth %s's keys under %s there and nothing was removed; start the server, or export %sPASSWORD when it needs a login, and run this again", err, td.rec.ID, m.Prefix, m.EnvPrefix)
	}
	defer conn.Close()
	found, err := conn.AnyUnder([]byte(m.Prefix))
	if err != nil {
		return apperr.New(apperr.Generic, "%v, so nothing was removed", err)
	}
	if found {
		td.items = append(td.items, item{kindRedis, m.Prefix})
	}
	td.redis = &redisLogin{server, login}
	return nil
}

// deleteRedisKeys deletes every key under prefix, which must be the recorded one.
func (td *teardown) deleteRedisKeys(prefix string) error {
	if m := td.rec.Redis; m == nil || m.Prefix == "" || m.Prefix != prefix {
		return fmt.Errorf("keys under %s are not recorded as berth %s's own, so berth won't delete them", prefix, td.rec.ID)
	}
	conn, err := redis.Open(td.redis.server, td.redis.login)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.DeleteUnder([]byte(prefix))
	return err
}

// redisReset is the berth's keys reset deletes.
type redisReset struct {
	redisLogin
	prefix string
	found  bool
}

// planRedisReset checks that main's .env, where it names a Redis server, names the
// recorded one, and looks for the berth's keys there, changing nothing.
func planRedisReset(w *worktree.Worktree, rec registry.Record) (*redisReset, error) {
	m := rec.Redis
	if m == nil || m.Prefix == "" {
		return nil, nil
	}
	recorded := redisServer(m)
	loc := redis.Location{Prefix: m.EnvPrefix}
	path := filepath.Join(w.Repository, ".env")
	env, err := readEnvFile(path)
	if err != nil {
		return nil, apperr.New(apperr.Generic, "reading main's %s for the Redis server: %v; reset discarded nothing", path, err)
	}
	if server, missing, err := loc.Server(env); err == nil && len(missing) == 0 && !server.Same(recorded) {
		return nil, apperr.New(apperr.Generic, "main's .env now points at the Redis server at %s, but berth %s's keys are on %s, so reset discarded nothing; point main's .env back at %s, or run berth remove and berth add to move the berth", server, rec.ID, recorded, recorded)
	}
	own, _ := readEnvFile(filepath.Join(rec.Worktree, ".env"))
	login := loc.Login(env, own, environ())
	conn, err := redis.Open(recorded, login)
	if err != nil {
		return nil, apperr.New(apperr.Generic, "%v, so reset discarded nothing; start the server and run berth reset again", err)
	}
	defer conn.Close()
	found, err := conn.AnyUnder([]byte(m.Prefix))
	if err != nil {
		return nil, apperr.New(apperr.Generic, "%v; reset discarded nothing", err)
	}
	return &redisReset{redisLogin{recorded, login}, m.Prefix, found}, nil
}

func (r *redisReset) discarded() []discarded {
	if r == nil || !r.found {
		return nil
	}
	return []discarded{{Kind: kindRedis, Name: r.prefix, Target: r.prefix}}
}

// execute deletes every key under the berth's prefix, including any written since the
// plan looked.
func (r *redisReset) execute() ([]discarded, error) {
	if r == nil {
		return nil, nil
	}
	conn, err := redis.Open(r.server, r.login)
	if err != nil {
		return nil, apperr.New(apperr.Generic, "%v; run berth reset again", err)
	}
	defer conn.Close()
	n, err := conn.DeleteUnder([]byte(r.prefix))
	if err != nil {
		return nil, apperr.New(apperr.Generic, "%v; run berth reset again", err)
	}
	r.found = r.found || n > 0
	return r.discarded(), nil
}
