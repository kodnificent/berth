package names

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode/utf8"
)

// DatabaseSegment is the berth's part of its database names: name with every run of
// characters other than lowercase letters and digits turned into one underscore and
// none at either end, cut to max. It never holds two underscores in a row, so a prefix
// built from it and ending in two underscores can't begin another berth's.
func DatabaseSegment(name string, max int) string {
	var b strings.Builder
	under := false
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			under = false
		} else if !under && b.Len() > 0 {
			b.WriteByte('_')
			under = true
		}
	}
	return trimUnder(b.String(), max)
}

func trimUnder(s string, max int) string {
	if max < 0 {
		max = 0
	}
	if len(s) > max {
		s = s[:max]
	}
	return strings.Trim(s, "_")
}

// DatabaseCandidates yields segments for name in order: its segment, then the segment
// with _2, _3 and so on, each cut to fit max. Cutting can give two berths one segment,
// so callers take the first one nothing on the server or in the registry holds.
func DatabaseCandidates(name string, max int, yield func(seg string) bool) {
	seg := DatabaseSegment(name, max)
	if seg == "" {
		seg = fallback
	}
	if !yield(seg) {
		return
	}
	for i := 2; ; i++ {
		suffix := "_" + strconv.Itoa(i)
		if !yield(trimUnder(seg, max-len(suffix)) + suffix) {
			return
		}
	}
}

// DatabasePrefix is the berth's database prefix: main's prefix, the berth's segment and
// two underscores, so app_ becomes app_feat_x__.
func DatabasePrefix(source, seg string) string { return source + seg + "__" }

// Database is the berth's name for main's database source when no prefix is
// configured: source, two underscores and the segment. When that passes limit, the
// engine's longest name, source is cut and ends in a hash of its whole name, so two
// sources that share their start still get names of their own.
func Database(source, seg string, limit int) string {
	room := limit - len("__") - len(seg)
	if len(source) > room {
		sum := sha256.Sum256([]byte(source))
		tag := "_" + hex.EncodeToString(sum[:4])
		source = cutRunes(source, max(room-len(tag), 0)) + tag
	}
	return source + "__" + seg
}

// cutRunes is s cut to at most n bytes on a character boundary, since a cut through a
// multibyte character gives a name the server rejects.
func cutRunes(s string, n int) string {
	for n > 0 && n < len(s) && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
