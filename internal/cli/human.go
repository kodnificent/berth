package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/kodnificent/berth/internal/hook"
	"github.com/kodnificent/berth/internal/volume"
)

type rowKind int

const (
	rowOther rowKind = iota
	rowName
	rowHost
	rowURL
	rowAssignedPort
	rowState
	rowPostAdd
	rowNext
)

type mark int

const (
	markNone mark = iota
	markReady
	markBusy
	markFailed
)

// row is one line of status: a label and its value. mark is the state the value
// reports, for the rows that report one.
type row struct {
	kind  rowKind
	label string
	value string
	mark  mark
}

func stateMark(status string) mark {
	switch status {
	case volume.Ready:
		return markReady
	case volume.Cloning, dbCreating:
		return markBusy
	}
	return markFailed
}

func postAddMark(status string) mark {
	switch status {
	case hook.OK:
		return markReady
	case hook.Pending, hook.Running:
		return markBusy
	}
	return markFailed
}

// summaryRows are the status rows add ends with: where the berth is, what state its
// data is in and what to do next.
func summaryRows(rows []row) []row {
	hasURL := false
	for _, r := range rows {
		hasURL = hasURL || r.kind == rowURL
	}
	var out []row
	for _, r := range rows {
		switch r.kind {
		case rowName, rowURL, rowAssignedPort, rowState, rowPostAdd, rowNext:
			out = append(out, r)
		case rowHost:
			if !hasURL {
				out = append(out, r)
			}
		}
	}
	return out
}

// styled reports whether w is a terminal that should get ticks and colour.
func styled(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return takesEscapes(f)
}

func writeRows(w io.Writer, rows []row) error {
	if styled(w) {
		_, err := io.WriteString(w, styledRows(rows))
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\n", r.label, r.value)
	}
	return tw.Flush()
}

const (
	sgrReset = "\x1b[0m"
	sgrDim   = "\x1b[2m"
)

var marks = map[mark]struct{ glyph, colour string }{
	markReady:  {"✓", "\x1b[32m"},
	markBusy:   {"…", "\x1b[33m"},
	markFailed: {"✗", "\x1b[31m"},
}

// styledRows lays the rows out itself: tabwriter would count the escape codes as text
// and push the values out of line.
func styledRows(rows []row) string {
	width := 0
	for _, r := range rows {
		width = max(width, utf8.RuneCountInString(r.label))
	}
	var b strings.Builder
	for _, r := range rows {
		pad := strings.Repeat(" ", width-utf8.RuneCountInString(r.label)+2)
		if r.label != "" {
			b.WriteString(sgrDim + r.label + sgrReset)
		}
		b.WriteString(pad)
		if m, ok := marks[r.mark]; ok {
			word, rest := r.value, ""
			if i := strings.IndexAny(r.value, " :"); i >= 0 {
				word, rest = r.value[:i], r.value[i:]
			}
			b.WriteString(m.colour + m.glyph + " " + word + sgrReset + rest)
		} else {
			b.WriteString(r.value)
		}
		b.WriteString("\n")
	}
	return b.String()
}
