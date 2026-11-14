package config

import (
	"github.com/BurntSushi/toml"
)

// RedisPrefix is the one strategy for a host Redis server: the berth's keys under a
// prefix of its own on main's server.
const RedisPrefix = "prefix"

// Redis is a host Redis server a berth shares with main through a key prefix.
type Redis struct {
	Strategy string `toml:"strategy"`
	From     string `toml:"from"`
	Prefix   string `toml:"prefix"`
}

// EnvPrefix is the prefix from names, under which main's .env sets the server's keys.
func (r *Redis) EnvPrefix() string {
	if g := fromPattern.FindStringSubmatch(r.From); g != nil {
		return g[1]
	}
	return ""
}

// PrefixKey is the env key main's key prefix is read from and the berth's is written to.
func (r *Redis) PrefixKey() string {
	if g := prefixPattern.FindStringSubmatch(r.Prefix); g != nil {
		return g[1]
	}
	return ""
}

func (c *Config) validateRedis(r *Redis, bad func(toml.Key, string, ...any)) {
	switch {
	case r.Strategy == "":
		bad(toml.Key{"state", "redis"}, "needs strategy = %q, the one way this version of berth shares a host Redis server", RedisPrefix)
	case r.Strategy != RedisPrefix:
		bad(toml.Key{"state", "redis", "strategy"}, "unknown strategy %q; want %q. A Redis in the compose project is a volume, set under [compose.volumes]", r.Strategy, RedisPrefix)
	}
	switch {
	case r.From == "":
		bad(toml.Key{"state", "redis"}, "needs from = \"env:<PREFIX>*\" naming the keys main's .env sets the server in, such as \"env:REDIS_*\"")
	case r.EnvPrefix() == "":
		bad(toml.Key{"state", "redis", "from"}, "%q is not \"env:<PREFIX>*\"; want the prefix of the keys main's .env sets the server in, such as \"env:REDIS_*\"", r.From)
	}
	switch {
	case r.Prefix == "":
		bad(toml.Key{"state", "redis"}, "needs prefix = \"env:<KEY>\" naming the key the app puts in front of every Redis key, such as \"env:REDIS_PREFIX\"")
	case r.PrefixKey() == "":
		bad(toml.Key{"state", "redis", "prefix"}, "%q is not \"env:<KEY>\"; want the key main's .env sets its Redis key prefix in, such as \"env:REDIS_PREFIX\"", r.Prefix)
	}
	pk := r.PrefixKey()
	if pk == "" {
		return
	}
	for _, w := range c.EnvWriters() {
		if w.Env == pk {
			bad(w.Key, "%s is written by berth with the berth's Redis key prefix ([state.redis] prefix); remove it", pk)
		}
	}
	if m, table := c.HostDB(); m != nil && m.PrefixKey() == pk {
		bad(toml.Key{"state", "redis", "prefix"}, "%s is also [state.%s] prefix, and berth writes a different prefix to each; give Redis its own key", pk, table)
	}
}
