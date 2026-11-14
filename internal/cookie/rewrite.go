package cookie

import (
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

// RewriteDomain replaces the value of each Domain attribute in the Set-Cookie header raw
// that is wider than host with host, and changes nothing else in the header, byte for
// byte. A domain that is host, under it, or unrelated to it is left as it is.
func RewriteDomain(raw, host string) (string, bool) {
	parts := strings.Split(raw, ";")
	changed := false
	for i := 1; i < len(parts); i++ {
		name, value, ok := strings.Cut(parts[i], "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "domain") {
			continue
		}
		trimmed := strings.TrimSpace(value)
		if !Wider(host, &http.Cookie{Domain: trimmed}) {
			continue
		}
		start := strings.Index(value, trimmed)
		parts[i] = name + "=" + value[:start] + host + value[start+len(trimmed):]
		changed = true
	}
	return strings.Join(parts, ";"), changed
}

// NameAndDomain reads the Set-Cookie header raw as a browser does, whatever Go's own
// parser makes of it: a browser stores `cart[items]=3` or a name over non-ASCII bytes all
// the same. The name is the text before the first "=" of the first segment, empty when
// there is none, and the domain is the value of the last Domain attribute that has one.
// The cookie's value is never returned. Both come back printable and bounded, since an
// app can put anything in them.
func NameAndDomain(raw string) (name, domain string) {
	parts := strings.Split(raw, ";")
	if n, _, ok := strings.Cut(parts[0], "="); ok {
		name = strings.TrimSpace(n)
	}
	for _, p := range parts[1:] {
		k, v, ok := strings.Cut(p, "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "domain") {
			if v = strings.TrimSpace(v); v != "" {
				domain = v
			}
		}
	}
	return Printable(name, maxName), Printable(domain, maxDomain)
}

// Printable is s with every rune a terminal or log could act on replaced by "?", cut to
// at most n bytes on a rune boundary.
func Printable(s string, n int) string {
	var b strings.Builder
	for _, r := range s {
		if r == utf8.RuneError || !unicode.IsPrint(r) {
			r = '?'
		}
		if b.Len()+utf8.RuneLen(r) > n {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}
