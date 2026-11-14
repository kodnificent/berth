package mysql

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type tokenKind int

const (
	word    tokenKind = iota // a bare identifier, keyword or number
	quoted                   // a backtick identifier, or a double-quoted one under ANSI_QUOTES
	literal                  // a string literal
	punct
)

// token is one token of a statement: text is an identifier's name, unquoted, or the
// punctuation character, and start and end are its bytes in the statement.
type token struct {
	kind       tokenKind
	text       string
	start, end int
}

func (t token) identifier() bool { return t.kind == word || t.kind == quoted }

var errVersioned = errors.New("it holds a versioned comment or an optimizer hint, whose text the server may run")

// lex splits stmt into tokens the way the server reads it under sqlMode: comments are
// dropped, every whitespace character separates, and string literals are kept whole.
// A statement it can't read to the end is an error, and so is one holding a comment
// the server might or might not run as code, unless serverPrinted: the server prints a
// versioned comment only when it runs it, so its text is read as code.
func lex(stmt, sqlMode string, serverPrinted bool) ([]token, error) {
	modes := strings.Split(strings.ToUpper(sqlMode), ",")
	has := func(m string) bool {
		for _, x := range modes {
			if strings.TrimSpace(x) == m {
				return true
			}
		}
		return false
	}
	ansiQuotes := has("ANSI_QUOTES") || has("ANSI")
	backslash := !has("NO_BACKSLASH_ESCAPES")

	var out []token
	i := 0
	for i < len(stmt) {
		r, size := utf8.DecodeRuneInString(stmt[i:])
		c := stmt[i]
		switch {
		case unicode.IsSpace(r):
			i += size
		case c == '#':
			i = lineEnd(stmt, i)
		case c == '-' && strings.HasPrefix(stmt[i:], "--") && (i+2 == len(stmt) || stmt[i+2] <= ' ' || stmt[i+2] == 0x7f):
			i = lineEnd(stmt, i)
		case c == '/' && strings.HasPrefix(stmt[i:], "/*"):
			if rest := stmt[i+2:]; strings.HasPrefix(rest, "!") || strings.HasPrefix(rest, "M!") || strings.HasPrefix(rest, "+") {
				if !serverPrinted {
					return nil, errVersioned
				}
				i += 2 + strings.IndexAny(rest, "!+") + 1
				for i < len(stmt) && stmt[i] >= '0' && stmt[i] <= '9' {
					i++
				}
				continue
			}
			end := strings.Index(stmt[i+2:], "*/")
			if end < 0 {
				return nil, errors.New("a comment is never closed")
			}
			i += 2 + end + 2
		case c == '`':
			text, end, err := quotedText(stmt, i, '`', false)
			if err != nil {
				return nil, err
			}
			out = append(out, token{quoted, text, i, end})
			i = end
		case c == '"':
			// A string unless ANSI_QUOTES, but its text counts as a name either way: a
			// false match only leaves an object out.
			text, end, err := quotedText(stmt, i, '"', backslash && !ansiQuotes)
			if err != nil {
				return nil, err
			}
			out = append(out, token{quoted, text, i, end})
			i = end
		case c == '\'':
			text, end, err := quotedText(stmt, i, '\'', backslash)
			if err != nil {
				return nil, err
			}
			out = append(out, token{literal, text, i, end})
			i = end
		case identChar(r):
			start := i
			for i < len(stmt) {
				r, size := utf8.DecodeRuneInString(stmt[i:])
				if !identChar(r) {
					break
				}
				i += size
			}
			out = append(out, token{word, stmt[start:i], start, i})
		default:
			out = append(out, token{punct, stmt[i : i+size], i, i + size})
			i += size
		}
	}
	return out, nil
}

// identChar is a character the server reads as part of a bare name. It reads every
// non-ASCII byte as one; this splits at non-ASCII whitespace too, which only makes more
// names to check.
func identChar(r rune) bool {
	return r == '_' || r == '$' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
		r >= utf8.RuneSelf && !unicode.IsSpace(r)
}

// lineEnd is where a comment starting at i ends: the server reads it to the next line
// feed only.
func lineEnd(stmt string, i int) int {
	if n := strings.IndexByte(stmt[i:], '\n'); n >= 0 {
		return i + n + 1
	}
	return len(stmt)
}

// quotedText reads the quoted text starting at stmt[i], where a doubled quote stands for
// one and, with backslash, a backslash escapes the next character.
func quotedText(stmt string, i int, q byte, backslash bool) (string, int, error) {
	var b strings.Builder
	for j := i + 1; j < len(stmt); j++ {
		switch c := stmt[j]; {
		case backslash && c == '\\' && j+1 < len(stmt):
			b.WriteByte(stmt[j+1])
			j++
		case c == q && j+1 < len(stmt) && stmt[j+1] == q:
			b.WriteByte(q)
			j++
		case c == q:
			return b.String(), j + 1, nil
		default:
			b.WriteByte(c)
		}
	}
	return "", 0, errors.New("a quoted name or string is never closed")
}

// referencedDatabase is the first of dbs that stmt, read under sqlMode, has a name
// equal to, with or without a dot after it, ignoring case. An error means stmt couldn't
// be read through, and must be treated as naming one.
func referencedDatabase(stmt, sqlMode string, dbs []string) (string, error) {
	tokens, err := lex(stmt, sqlMode, false)
	if err != nil {
		return "", err
	}
	for _, t := range tokens {
		if !t.identifier() {
			continue
		}
		// A server that folds names, as lower_case_table_names 1 and 2 do, folds them by
		// its own collation, under which İ is I; only ASCII folds the same in Go.
		if strings.IndexFunc(t.text, func(r rune) bool { return r >= utf8.RuneSelf }) >= 0 {
			return "", fmt.Errorf("it holds the name %q, and the server may read a name outside ASCII as another database's", t.text)
		}
		name := strings.TrimRight(t.text, " ")
		for _, d := range dbs {
			if strings.EqualFold(name, d) {
				return d, nil
			}
		}
	}
	return "", nil
}

// placedOutside is the words in a CREATE TABLE statement the server printed that can put
// a table's rows or files outside its database: a MERGE table's UNION, a FEDERATED
// table's CONNECTION, and DATA or INDEX DIRECTORY.
var placedOutside = map[string]bool{"UNION": true, "CONNECTION": true, "DIRECTORY": true}

// outsideOption is the first such word stmt holds, or "". An error means stmt couldn't
// be read through.
func outsideOption(stmt string) (string, error) {
	tokens, err := lex(stmt, "", true)
	if err != nil {
		return "", err
	}
	for _, t := range tokens {
		if t.kind == word && placedOutside[strings.ToUpper(t.text)] {
			return strings.ToUpper(t.text), nil
		}
	}
	return "", nil
}

// withoutForeignDatabases drops from a CREATE TABLE statement the server printed each
// table element that references a table in another database, as a foreign key does
// with REFERENCES `db`.`table`, and names those databases. The caller still checks the
// created table, since this reads only what the server prints.
func withoutForeignDatabases(stmt string) (string, []string, error) {
	tokens, err := lex(stmt, "", true)
	if err != nil {
		return "", nil, err
	}
	type span struct{ start, end int }
	var elements []span
	var commas []int
	depth, from := 0, -1
scan:
	for i, t := range tokens {
		if t.kind != punct {
			continue
		}
		switch t.text {
		case "(":
			depth++
			if depth == 1 {
				from = i + 1
			}
		case ")":
			if depth == 1 {
				elements = append(elements, span{from, i})
				break scan
			}
			depth--
		case ",":
			if depth == 1 {
				elements = append(elements, span{from, i})
				commas = append(commas, i)
				from = i + 1
			}
		}
	}
	var dropped []string
	var cuts []span
	for n, e := range elements {
		db := ""
		for i := e.start; i+3 < e.end; i++ {
			if tokens[i].kind == word && strings.EqualFold(tokens[i].text, "REFERENCES") &&
				tokens[i+1].identifier() && tokens[i+2].kind == punct && tokens[i+2].text == "." && tokens[i+3].identifier() {
				db = tokens[i+1].text
				break
			}
		}
		if db == "" {
			continue
		}
		dropped = append(dropped, db)
		// A table's columns come first, so a reference is never its first element, and
		// the comma before it goes with it.
		if n == 0 {
			return "", nil, errors.New("the table's first element references another database")
		}
		cuts = append(cuts, span{tokens[commas[n-1]].start, tokens[e.end-1].end})
	}
	for i := len(cuts) - 1; i >= 0; i-- {
		stmt = stmt[:cuts[i].start] + stmt[cuts[i].end:]
	}
	return stmt, dropped, nil
}
