package config

import (
	"strings"
	"testing"
)

const redisTable = "[state.redis]\nstrategy = \"prefix\"\nfrom = \"env:REDIS_*\"\nprefix = \"env:REDIS_PREFIX\"\n"

func TestInvalidRedisConfig(t *testing.T) {
	cases := []struct{ name, content, want string }{
		{"unknown strategy", "[state.redis]\nstrategy = \"fresh\"\nfrom = \"env:REDIS_*\"\nprefix = \"env:REDIS_PREFIX\"\n", "state.redis.strategy: unknown strategy \"fresh\""},
		{"no strategy", "[state.redis]\nfrom = \"env:REDIS_*\"\nprefix = \"env:REDIS_PREFIX\"\n", "state.redis: needs strategy = \"prefix\""},
		{"no prefix", "[state.redis]\nstrategy = \"prefix\"\nfrom = \"env:REDIS_*\"\n", "state.redis: needs prefix"},
		{"url from", "[state.redis]\nstrategy = \"prefix\"\nfrom = \"env:REDIS_URL\"\nprefix = \"env:REDIS_PREFIX\"\n", "state.redis.from"},
		{"unknown key", redisTable + "databases = 16\n", "unknown key state.redis.databases"},
		{"prefix key in env.set", redisTable + "[env.set]\nREDIS_PREFIX = \"x_\"\n", "env.set.REDIS_PREFIX: REDIS_PREFIX is written by berth"},
		{"prefix key shared with mysql", redisTable + "[state.mysql]\nfrom = \"env:DB_*\"\nprefix = \"env:REDIS_PREFIX\"\n", "state.redis.prefix: REDIS_PREFIX is also [state.mysql] prefix"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			paths := layers(t, c.content)
			msg := loadErr(t, paths...)
			if !strings.Contains(msg, paths[0]) {
				t.Errorf("message %q does not name %s", msg, paths[0])
			}
			if !strings.Contains(msg, c.want) {
				t.Errorf("message %q does not contain %q", msg, c.want)
			}
		})
	}
}

func TestRedisBesideAHostDatabaseLoadsWithItsKeys(t *testing.T) {
	r := mustLoad(t, layers(t, redisTable+"[state.mysql]\nfrom = \"env:DB_*\"\nprefix = \"env:DB_DATABASE_PREFIX\"\n")...).State.Redis
	if r.EnvPrefix() != "REDIS_" || r.PrefixKey() != "REDIS_PREFIX" {
		t.Errorf("EnvPrefix, PrefixKey = %q, %q; want REDIS_, REDIS_PREFIX", r.EnvPrefix(), r.PrefixKey())
	}
}
