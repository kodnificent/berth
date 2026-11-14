package config

import (
	"fmt"
	"strings"
)

// Values are what templates expand to. A port missing from Ports has not been assigned
// yet, so a template that names it stays unresolved instead of guessing.
type Values struct {
	Name  string
	Host  string
	URL   string
	Ports map[string]int
}

type part struct {
	literal string
	token   string
}

func parseTemplate(s string) ([]part, error) {
	var parts []part
	var lit strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '{' && strings.HasPrefix(s[i:], "{{"):
			lit.WriteByte('{')
			i++
		case c == '}' && strings.HasPrefix(s[i:], "}}"):
			lit.WriteByte('}')
			i++
		case c == '{':
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				return nil, fmt.Errorf("unclosed { in %q; write {{ for a literal brace", s)
			}
			if lit.Len() > 0 {
				parts = append(parts, part{literal: lit.String()})
				lit.Reset()
			}
			parts = append(parts, part{token: s[i+1 : i+end]})
			i += end
		case c == '}':
			return nil, fmt.Errorf("unmatched } in %q; write }} for a literal brace", s)
		default:
			lit.WriteByte(c)
		}
	}
	if lit.Len() > 0 {
		parts = append(parts, part{literal: lit.String()})
	}
	return parts, nil
}

// expand returns the template with every token replaced, and ok false when a port it
// names has no value yet.
func expand(parts []part, v Values) (out string, ok bool) {
	var b strings.Builder
	ok = true
	for _, p := range parts {
		switch {
		case p.token == "":
			b.WriteString(p.literal)
		case p.token == "name":
			b.WriteString(v.Name)
		case p.token == "host":
			b.WriteString(v.Host)
		case p.token == "url":
			b.WriteString(v.URL)
		default:
			port, found := v.Ports[strings.TrimPrefix(p.token, "port.")]
			if !found {
				ok = false
				continue
			}
			fmt.Fprintf(&b, "%d", port)
		}
	}
	return b.String(), ok
}
