// Package envfile writes berth's managed block into a .env file and leaves every other
// byte of the file as it was.
package envfile

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	Start = "# >>> berth (managed, do not edit)"
	End   = "# <<< berth"
)

// Block renders env as the managed block, keys sorted so the same env always gives the
// same bytes.
func Block(env map[string]string) ([]byte, error) {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	b.WriteString(Start + "\n")
	for _, k := range keys {
		v, err := quote(env[k])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		fmt.Fprintf(&b, "%s=%s\n", k, v)
	}
	b.WriteString(End + "\n")
	return b.Bytes(), nil
}

var bare = regexp.MustCompile(`^[A-Za-z0-9_./:,@%+=-]*$`)

// quote writes a value so Docker Compose and dotenv parsers read it back verbatim. They
// agree on bare words, on single quotes as fully literal, and on double quotes only when
// nothing inside could be escaped or interpolated.
func quote(v string) (string, error) {
	switch {
	case bare.MatchString(v):
		return v, nil
	case !strings.ContainsAny(v, "'\n\r"):
		return "'" + v + "'", nil
	case !strings.ContainsAny(v, "\"$\\`\n\r"):
		return `"` + v + `"`, nil
	default:
		return "", fmt.Errorf("value %q holds both a single quote and one of \" $ \\ ` or a newline, which .env parsers read differently", v)
	}
}

// Apply returns content with its managed block replaced by block, as the file's last
// lines. sep says the newline before the existing block was berth's own, added because
// the content before it had no final newline; newSep says the same of the result.
func Apply(content, block []byte, sep bool) (out []byte, newSep bool) {
	base := Strip(content, sep)
	newSep = len(base) > 0 && base[len(base)-1] != '\n'
	out = append([]byte{}, base...)
	if newSep {
		out = append(out, '\n')
	}
	return append(out, block...), newSep
}

// Strip returns content without the managed block, and without the separating newline
// when sep says berth added it.
func Strip(content []byte, sep bool) []byte {
	start := lineIndex(content, Start, 0)
	if start < 0 {
		return content
	}
	stop := len(content)
	if end := lineIndex(content, End, start); end >= 0 {
		stop = end + len(End)
		if stop < len(content) && content[stop] == '\n' {
			stop++
		}
	}
	before := content[:start]
	if sep && start > 0 {
		before = before[:start-1]
	}
	out := append(append([]byte{}, before...), content[stop:]...)
	return Strip(out, false)
}

// lineIndex finds line as a whole line at or after from.
func lineIndex(content []byte, line string, from int) int {
	for i := from; i < len(content); {
		j := bytes.Index(content[i:], []byte(line))
		if j < 0 {
			return -1
		}
		at := i + j
		after := at + len(line)
		if (at == 0 || content[at-1] == '\n') && (after == len(content) || content[after] == '\n' || content[after] == '\r') {
			return at
		}
		i = at + 1
	}
	return -1
}
