package initconf

import (
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var frameworks = []string{"laravel", "artisan", "vite", "rails", "nextjs", "nuxt", "sveltekit", "django", "symfony"}

// Doctor's build_env check (design, Doctor) warns about env keys by the prefixes bundlers
// inline, so these literals are specified behaviour rather than framework knowledge.
const buildTimePrefixList = "internal/envfile/buildtime.go"

var (
	camelBoundary = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	notAlnum      = regexp.MustCompile(`[^a-z0-9.]+`)
)

func wordsOf(text string) []string {
	return strings.Fields(notAlnum.ReplaceAllString(strings.ToLower(camelBoundary.ReplaceAllString(text, "$1 $2")), " "))
}

func namesAFramework(text string) string {
	for _, w := range wordsOf(text) {
		w = strings.Trim(w, ".")
		if slices.Contains(frameworks, strings.ReplaceAll(w, ".", "")) {
			return w
		}
	}
	return ""
}

func isPresetLoader(rel string) bool {
	for _, part := range strings.Split(strings.TrimSuffix(rel, ".go"), "/") {
		if part == "preset" || part == "presets" {
			return true
		}
	}
	return false
}

// The design's "Framework-agnostic core". Comments are left out: only code
// can act on a framework's name.
func TestNoCodeOutsideThePresetLoaderNamesAFramework(t *testing.T) {
	root := filepath.Join("..", "..")
	skip := []string{".git", ".claude", "site", "node_modules", "testdata"}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if slices.Contains(skip, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") || rel == buildTimePrefixList || isPresetLoader(rel) {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		var s scanner.Scanner
		s.Init(fset.AddFile(rel, fset.Base(), len(src)), src, nil, 0)
		for {
			pos, tok, lit := s.Scan()
			if tok == token.EOF {
				return nil
			}
			if tok != token.IDENT && tok != token.STRING && tok != token.CHAR {
				continue
			}
			if name := namesAFramework(lit); name != "" {
				t.Errorf("%s names the framework %q in %s", fset.Position(pos), name, lit)
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}
