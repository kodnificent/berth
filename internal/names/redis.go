package names

import "strings"

// RedisPrefix is the berth's Redis key prefix: its segment and two underscores in front
// of main's prefix, so app_ becomes feat_x__app_.
func RedisPrefix(seg, source string) string { return seg + "__" + source }

// RedisCandidates yields segments for name in order, as DatabaseCandidates does, leaving
// out any whose prefix begins with source: every key of main's starts with source, so a
// key main writes later could fall under such a prefix. When name's own segment is one,
// as branch app-x's is beside main's app_, every segment is given a first letter source
// doesn't start with, so none can be.
func RedisCandidates(name, source string, yield func(seg string) bool) {
	if source == "" {
		return
	}
	base := name
	DatabaseCandidates(name, MaxName, func(seg string) bool {
		if strings.HasPrefix(RedisPrefix(seg, source), source) {
			lead := "b"
			if source[0] == 'b' {
				lead = "c"
			}
			base = lead + "-" + name
		}
		return false
	})
	DatabaseCandidates(base, MaxName, func(seg string) bool {
		if strings.HasPrefix(RedisPrefix(seg, source), source) {
			return true
		}
		return yield(seg)
	})
}
