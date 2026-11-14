package envfile

import (
	"strings"
	"testing"
)

func TestApplyKeepsEverythingOutsideTheBlock(t *testing.T) {
	block, err := Block(map[string]string{"B": "2", "A": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if want := Start + "\nA=1\nB=2\n" + End + "\n"; string(block) != want {
		t.Fatalf("Block = %q, want %q", block, want)
	}
	for _, original := range []string{"", "X=1\n", "X=1", "X=1\n\n", "# only a comment"} {
		out, sep := Apply([]byte(original), block, false)
		if !strings.HasSuffix(string(out), string(block)) {
			t.Errorf("Apply(%q) = %q; the block is not last", original, out)
		}
		if got := string(Strip(out, sep)); got != original {
			t.Errorf("Strip(Apply(%q)) = %q", original, got)
		}
		again, sep2 := Apply(out, block, sep)
		if string(again) != string(out) || sep2 != sep {
			t.Errorf("applying the same block twice to %q changed it: %q then %q", original, out, again)
		}
	}
}

func TestApplyMovesTheBlockLastAndReplacesIt(t *testing.T) {
	old := "X=1\n" + Start + "\nA=old\n" + End + "\nY=2\n"
	block, _ := Block(map[string]string{"A": "new"})
	out, _ := Apply([]byte(old), block, false)
	if want := "X=1\nY=2\n" + string(block); string(out) != want {
		t.Errorf("Apply = %q, want %q", out, want)
	}
}

func TestStripIgnoresMarkersInsideLines(t *testing.T) {
	content := "NOTE=\"" + Start + "\"\n"
	if got := string(Strip([]byte(content), false)); got != content {
		t.Errorf("Strip removed a marker that is not a whole line: %q", got)
	}
}

func TestQuote(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"http://a.localhost:20001", "http://a.localhost:20001"},
		{"", ""},
		{"a,b.c", "a,b.c"},
		{"two words", "'two words'"},
		{"$HOME #x \"q\"", `'$HOME #x "q"'`},
		{"it's", `"it's"`},
	} {
		got, err := quote(c.in)
		if err != nil || got != c.want {
			t.Errorf("quote(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"it's $HOME", "line\nbreak", `it's \`} {
		if _, err := quote(bad); err == nil {
			t.Errorf("quote(%q) succeeded; parsers would read it differently", bad)
		}
	}
}

func TestBuildTimeKeys(t *testing.T) {
	for key, want := range map[string]bool{
		"VITE_API_URL": true, "NEXT_PUBLIC_HOST": true, "REACT_APP_X": true, "PUBLIC_URL": true, "NUXT_PUBLIC_API": true,
		"APP_URL": false, "VITE": false, "MY_VITE_URL": false,
	} {
		if got := BuildTime(key); got != want {
			t.Errorf("BuildTime(%q) = %v, want %v", key, got, want)
		}
	}
}
