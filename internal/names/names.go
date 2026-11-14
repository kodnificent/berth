// Package names turns branch and project names into berth names, compose project names
// and host labels that are valid DNS labels and unique on the machine.
package names

import (
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const (
	// MaxLabel is the longest DNS label, and so the longest name or compose project.
	MaxLabel = 63
	// MaxName leaves room in a 63-character label for a project prefix and for hosts
	// such as app.{name}.myapp.localhost to stay readable.
	MaxName  = 40
	fallback = "berth"
)

// Slug lowercases s, drops accents, and turns every run of other characters into one
// hyphen, so the result is a valid DNS label of at most max characters, or empty.
func Slug(s string, max int) string {
	var b strings.Builder
	dash := false
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		switch {
		case unicode.Is(unicode.Mn, r):
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	return trim(b.String(), max)
}

func trim(s string, max int) string {
	if len(s) > max {
		s = s[:max]
	}
	return strings.Trim(s, "-")
}

// Candidates yields names for base in order: its slug, then the slug with -2, -3 and so
// on, each cut to fit max. Two different bases can share a slug, so callers take the
// first candidate nothing else on the machine uses.
func Candidates(base string, max int, yield func(name string) bool) {
	slug := Slug(base, max)
	if slug == "" {
		slug = fallback
	}
	if !yield(slug) {
		return
	}
	for i := 2; ; i++ {
		suffix := "-" + strconv.Itoa(i)
		if !yield(trim(slug, max-len(suffix)) + suffix) {
			return
		}
	}
}

// Project is the compose project name for the berth called name in a repository whose
// main project is mainProject: the main project's name, a hyphen and the berth's name,
// cut to one DNS label.
func Project(mainProject, name string) string {
	prefix := Slug(mainProject, MaxLabel-1-len(name))
	if prefix == "" {
		return name
	}
	return prefix + "-" + name
}

// ValidLabel reports whether s is a lowercase DNS label.
func ValidLabel(s string) bool {
	if s == "" || len(s) > MaxLabel || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// ValidHost reports whether s is a hostname made of lowercase DNS labels.
func ValidHost(s string) bool {
	if len(s) > 253 {
		return false
	}
	for _, l := range strings.Split(s, ".") {
		if !ValidLabel(l) {
			return false
		}
	}
	return true
}

// Within reports whether name is host or a name under it. Host names ignore case and a
// trailing dot, as browsers compare them.
func Within(host, name string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	return host != "" && (name == host || strings.HasSuffix(name, "."+host))
}
