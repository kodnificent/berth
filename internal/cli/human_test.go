package cli

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/kodnificent/berth/internal/registry"
)

var statusRows = []row{
	{rowOther, "berth", "9f2c", markNone},
	{rowName, "name", "feat-x", markNone},
	{rowHost, "host", "feat-x.localhost", markNone},
	{rowOther, "port db:3306", "none", markNone},
	{rowAssignedPort, "port web:80", "20000 (assign)", markNone},
	{rowOther, "files", ".env", markNone},
	{rowState, "volume cache", "ready (copy, 1.0 MB)", markReady},
	{rowOther, "volume cache", "note: copied while main ran", markNone},
	{rowState, "database app_feat_x", "cloning (62%, 680 MB of 1.1 GB)", markBusy},
	{rowState, "sqlite db.sqlite3", "failed: disk full", markFailed},
	{rowPostAdd, "post-add", "pending", markBusy},
	{rowNext, "next", "run berth status --wait", markNone},
}

func labels(rows []row) string {
	var out []string
	for _, r := range rows {
		out = append(out, r.label)
	}
	return strings.Join(out, ",")
}

func TestSummaryShowsTheHostWhenTheBerthHasNoURL(t *testing.T) {
	want := "name,host,port web:80,volume cache,database app_feat_x,sqlite db.sqlite3,post-add,next"
	if got := labels(summaryRows(statusRows)); got != want {
		t.Errorf("summary rows %q, want %q", got, want)
	}
}

func TestSummaryShowsTheURLInPlaceOfTheHost(t *testing.T) {
	rows := append([]row{{rowURL, "url", "http://feat-x.localhost:20000", markNone}}, statusRows...)
	if got := labels(summaryRows(rows)); !strings.HasPrefix(got, "url,name,port web:80,") {
		t.Errorf("summary rows %q, want the url and no host", got)
	}
}

func TestRowsOffATerminalHoldNoEscapeByte(t *testing.T) {
	var b bytes.Buffer
	if err := writeRows(&b, statusRows); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "\x1b") {
		t.Errorf("rows written to a buffer hold an escape byte:\n%q", b.String())
	}
	if !strings.Contains(b.String(), "volume cache         ready (copy, 1.0 MB)\n") {
		t.Errorf("plain rows lost their two-column layout:\n%s", b.String())
	}
}

func TestStyledRowsGiveEachStateItsOwnColourAndKeepTheText(t *testing.T) {
	out := styledRows(statusRows)
	sgr := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	colour := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		for _, state := range []string{"ready", "cloning", "failed"} {
			if strings.Contains(line, " "+state) {
				colour[state] = sgr.FindAllString(line, -1)[2]
			}
		}
	}
	if colour["ready"] == colour["cloning"] || colour["ready"] == colour["failed"] || colour["cloning"] == colour["failed"] {
		t.Errorf("states share a colour: %q", colour)
	}
	var plain bytes.Buffer
	if err := writeRows(&plain, statusRows); err != nil {
		t.Fatal(err)
	}
	unmarked := strings.NewReplacer("✓ ", "", "… ", "", "✗ ", "").Replace(sgr.ReplaceAllString(out, ""))
	if unmarked != plain.String() {
		t.Errorf("styling changed more than ticks and colour:\nstyled:\n%s\nplain:\n%s", unmarked, plain.String())
	}
}

func TestStyledRowsPairEachStateWithItsGlyphAndDimTheLabels(t *testing.T) {
	out := styledRows(statusRows)
	for _, want := range []string{
		"\x1b[2mvolume cache\x1b[0m         \x1b[32m✓ ready\x1b[0m (copy, 1.0 MB)\n",
		"\x1b[33m… cloning\x1b[0m (62%, 680 MB of 1.1 GB)\n",
		"\x1b[31m✗ failed\x1b[0m: disk full\n",
		"\x1b[33m… pending\x1b[0m\n",
		"\x1b[2mname\x1b[0m                 feat-x\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("styled rows lack %q:\n%q", want, out)
		}
	}
}

func TestEveryStatusHasItsMark(t *testing.T) {
	for status, want := range map[string]mark{"ready": markReady, "cloning": markBusy, "creating": markBusy, "failed": markFailed} {
		if got := stateMark(status); got != want {
			t.Errorf("a %s volume or database is marked %d, want %d", status, got, want)
		}
	}
	for status, want := range map[string]mark{"ok": markReady, "pending": markBusy, "running": markBusy, "failed": markFailed, "skipped": markFailed} {
		if got := postAddMark(status); got != want {
			t.Errorf("a %s post-add is marked %d, want %d", status, got, want)
		}
	}
}

func TestSQLiteFileMainLacksCarriesNoMark(t *testing.T) {
	v := berthView{state: []stateJSON{{Name: "db.sqlite3", Kind: kindSQLite, Status: "ready", SourceMissing: true}}}
	for _, r := range v.rows(registry.Record{}) {
		if r.label == "sqlite db.sqlite3" && (r.mark != markNone || r.value != "none: main has no such file") {
			t.Errorf("row for a SQLite file main lacks is %+v, want no mark", r)
		}
	}
}
