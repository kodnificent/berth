package cli

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kodnificent/berth/internal/apperr"
)

func TestSourceFormsCoverEscapes(t *testing.T) {
	forms := sourceForms("/Users/you/app")
	for _, want := range []string{"/Users/you/app", `\/Users\/you\/app`, `\Users\you\app`, `\\Users\\you\\app`} {
		if !slices.Contains(forms, want) {
			t.Errorf("forms %q lack %q", forms, want)
		}
	}
}

func TestSourceFormsAddPrivateAlias(t *testing.T) {
	real, err := filepath.EvalSymlinks("/var")
	if err != nil || real != "/private/var" {
		t.Skip("no /var alias for /private/var here")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	alias, ok := strings.CutPrefix(dir, "/private")
	if !ok {
		t.Skipf("%s is not under /private", dir)
	}
	if forms := sourceForms(dir); !slices.Contains(forms, alias) {
		t.Errorf("forms %q lack the alias %q", forms, alias)
	}
}

func TestFindSourceMatchesWholePathsOnly(t *testing.T) {
	dir := t.TempDir()
	needles := sourceForms("/src/app")
	self := sourceForms("/src/app/.worktrees/feat")
	cases := []struct {
		content string
		line    int
	}{
		{"a\nroot=/src/app\n", 2},
		{"x /src/app/vendor/autoload.php\n", 1},
		{`{"p":"\/src\/app\/x"}`, 1},
		{"/src/application\n", 0},
		{"/src/app-old/x\n", 0},
		{"/src/app/.worktrees/feat/vendor\n", 0},
		{"one /src/app/.worktrees/feat\ntwo /src/app\n", 2},
		{"bin\x00/src/app\n", 0},
	}
	for i, c := range cases {
		p := filepath.Join(dir, "f")
		if err := os.WriteFile(p, []byte(c.content), 0o644); err != nil {
			t.Fatal(err)
		}
		line, _, ok := findSource(p, needles, self)
		if !ok {
			line = 0
		}
		if line != c.line {
			t.Errorf("case %d %q: line %d, want %d", i, c.content, line, c.line)
		}
	}
}

func TestRemoveUntrackedKeepsTrackedFiles(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"web/Dockerfile", "web/extra.txt", "web/sub/tracked.txt", "web/sub/new.txt", "web/gen/a.txt"} {
		p := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tracked := map[string]bool{"web/Dockerfile": true, "web/sub/tracked.txt": true}
	if err := removeUntracked(root, "web", tracked); err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]bool{"web/Dockerfile": true, "web/sub/tracked.txt": true, "web/extra.txt": false, "web/sub/new.txt": false, "web/gen": false} {
		_, err := os.Lstat(filepath.Join(root, f))
		if got := err == nil; got != want {
			t.Errorf("%s exists %v after removal, want %v", f, got, want)
		}
	}
}

func TestConfigFindingsSplitByFile(t *testing.T) {
	repo := t.TempDir()
	private, shared := filepath.Join(repo, ".git", "berth.toml"), filepath.Join(repo, "berth.toml")
	err := apperr.New(apperr.ConfigInvalid, "%s: unknown key berth.hots\n%s: [pool] ports is not a range", private, shared)
	got := configFindings(err)
	if len(got) != 2 || got[0].Location != private || got[0].Message != "unknown key berth.hots" || got[1].Location != shared {
		t.Errorf("findings %+v", got)
	}
	if got := configFindings(errors.New("no file here")); len(got) != 1 || got[0].Location != "config" {
		t.Errorf("findings %+v", got)
	}
}
