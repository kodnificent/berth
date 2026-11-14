package cookie

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRewriteReplacesOnlyAWideDomainsValue(t *testing.T) {
	cases := map[string]string{
		"s=v1; Domain=.myapp.localhost; Path=/; HttpOnly":      "s=v1; Domain=feat-x.myapp.localhost; Path=/; HttpOnly",
		"s=v1;domain=localhost;path=/":                         "s=v1;domain=feat-x.myapp.localhost;path=/",
		"s=v1; DOMAIN = MyApp.Localhost ; Secure":              "s=v1; DOMAIN = feat-x.myapp.localhost ; Secure",
		"s=a=b; Path=/x; Domain=.myapp.localhost":              "s=a=b; Path=/x; Domain=feat-x.myapp.localhost",
		"s=1; Domain=feat-x.myapp.localhost; Domain=localhost": "s=1; Domain=feat-x.myapp.localhost; Domain=feat-x.myapp.localhost",
	}
	for raw, want := range cases {
		got, changed := RewriteDomain(raw, "feat-x.myapp.localhost")
		if got != want || !changed {
			t.Errorf("RewriteDomain(%q) = %q, %v; want %q", raw, got, changed, want)
		}
	}
}

func TestRewriteLeavesTheBerthsOwnAndUnrelatedDomainsByteForByte(t *testing.T) {
	for _, raw := range []string{
		"own=v1; Domain=feat-x.myapp.localhost; Path=/; HttpOnly; SameSite=Lax",
		"pref=v2; domain=app.feat-x.myapp.localhost; path=/; secure",
		"host_only=v3; Path=/",
		"other=v4; Domain=example.com",
		"sibling=v5; Domain=shop.myapp.localhost",
		"value_says_domain=Domain=localhost; Path=/",
	} {
		if got, changed := RewriteDomain(raw, "feat-x.myapp.localhost"); got != raw || changed {
			t.Errorf("RewriteDomain(%q) = %q, %v; want it unchanged", raw, got, changed)
		}
	}
}

func TestNoteKeepsAtMostMaxSeenCookiesAndTheNewest(t *testing.T) {
	var list []Seen
	for n := range MaxSeen + 10 {
		list, _ = Note(list, Seen{Name: fmt.Sprintf("c%d", n), Domain: "localhost"})
	}

	if len(list) != MaxSeen || list[len(list)-1].Name != fmt.Sprintf("c%d", MaxSeen+9) {
		t.Errorf("after %d cookies the list holds %d ending in %q, want %d ending in the newest", MaxSeen+10, len(list), list[len(list)-1].Name, MaxSeen)
	}
}

func TestNoteReplacesWhatItHeldForTheSameName(t *testing.T) {
	list, _ := Note(nil, Seen{Name: "s", Domain: "localhost"})

	list, changed := Note(list, Seen{Name: "s", Domain: ".myapp.localhost"})

	if !changed || len(list) != 1 || list[0].Domain != ".myapp.localhost" {
		t.Errorf("after a second sighting of s: %+v, changed %v; want one entry with the newer domain", list, changed)
	}
}

func TestClearDropsACookieTheAppNowScopesToTheBerth(t *testing.T) {
	list, _ := Note(nil, Seen{Name: "s", Domain: "localhost"})
	list, _ = Note(list, Seen{Name: "t", Domain: "localhost"})

	list, changed := Clear(list, "s")

	if !changed || len(list) != 1 || list[0].Name != "t" {
		t.Errorf("after clearing s: %+v, changed %v; want only t", list, changed)
	}
}

func TestNameAndDomainKeepNothingATerminalCouldActOn(t *testing.T) {
	name, domain := NameAndDomain("bad\x1b[31mname\r\n=v; Domain=x; domain=.myapp.localhost\x07; Domain=")

	if name != "bad?[31mname" || domain != ".myapp.localhost?" {
		t.Errorf("name %q, domain %q; want control characters replaced and the last non-empty Domain", name, domain)
	}
}

func TestNameAndDomainBoundTheName(t *testing.T) {
	name, _ := NameAndDomain(strings.Repeat("é", 200) + "=v")

	if len(name) > maxName || !utf8.ValidString(name) {
		t.Errorf("a 400-byte name came back as %d bytes, valid UTF-8 %v; want at most %d, cut on a rune", len(name), utf8.ValidString(name), maxName)
	}
}
