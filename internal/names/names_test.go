package names

import (
	"math/rand/v2"
	"strings"
	"testing"
)

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"feat-x":                       "feat-x",
		"Feat_X":                       "feat-x",
		"--a..b--":                     "a-b",
		"café":                         "cafe",
		"Fix/ünïcödé-ñame":             "fix-unicode-name",
		"日本語":                          "",
		"v1.2-release":                 "v1-2-release",
		strings.Repeat("a", 70):        strings.Repeat("a", MaxName),
		strings.Repeat("a", 39) + "/b": strings.Repeat("a", 39),
	} {
		if got := Slug(in, MaxName); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCandidatesStayValidAndDistinct(t *testing.T) {
	var got []string
	Candidates(strings.Repeat("x", 200), 10, func(n string) bool {
		got = append(got, n)
		return len(got) < 12
	})
	seen := map[string]bool{}
	for _, n := range got {
		if !ValidLabel(n) || len(n) > 10 || seen[n] {
			t.Errorf("candidate %q is invalid, too long or repeated (all: %v)", n, got)
		}
		seen[n] = true
	}
	var first string
	Candidates("日本語", 40, func(n string) bool { first = n; return false })
	if first != "berth" {
		t.Errorf("first candidate for a name with no ASCII = %q, want berth", first)
	}
}

func TestProjectIsOneLabel(t *testing.T) {
	for _, c := range []struct{ main, name, want string }{
		{"shop", "feat-x", "shop-feat-x"},
		{"my_app", "a", "my-app-a"},
		{"", "a", "a"},
	} {
		if got := Project(c.main, c.name); got != c.want {
			t.Errorf("Project(%q, %q) = %q, want %q", c.main, c.name, got, c.want)
		}
	}
	long := Project(strings.Repeat("p", 80), strings.Repeat("n", MaxName))
	if !ValidLabel(long) || !strings.HasSuffix(long, "-"+strings.Repeat("n", MaxName)) {
		t.Errorf("Project with long inputs = %q, want a valid label ending in the name", long)
	}
}

// Random branch-like strings, taken first-free as add does, always give valid,
// distinct names.
func TestFuzzNamesAreValidAndUnique(t *testing.T) {
	alphabet := []rune("abcXYZ019/-_.äöüñ日本🚀 ")
	r := rand.New(rand.NewPCG(1, 2))
	used := map[string]string{}
	for i := 0; i < 3000; i++ {
		b := make([]rune, 1+r.IntN(220))
		for j := range b {
			b[j] = alphabet[r.IntN(len(alphabet))]
		}
		branch := string(b)
		Candidates(branch, MaxName, func(n string) bool {
			if _, taken := used[n]; taken {
				return true
			}
			if !ValidLabel(n) || !ValidLabel(Project("main_project", n)) {
				t.Fatalf("branch %q gave invalid name %q or project %q", branch, n, Project("main_project", n))
			}
			used[n] = branch
			return false
		})
	}
}
