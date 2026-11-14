package names

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// Random berth names beside random main prefixes, each berth taking its first segment no
// other has, give prefixes that neither begin one another nor begin with main's.
func TestFuzzRedisPrefixesStayClearOfEachOtherAndMain(t *testing.T) {
	alphabet := []rune("ab01-_x")
	r := rand.New(rand.NewPCG(5, 6))
	for _, source := range []string{"app_", "a", "b", "b0", "a_", "ab_1_", "x-", "A_", "a?p[1]_"} {
		used := map[string]bool{}
		var prefixes []string
		for i := 0; i < 500; i++ {
			b := make([]rune, 1+r.IntN(30))
			for j := range b {
				b[j] = alphabet[r.IntN(len(alphabet))]
			}
			name := string(b)
			if r.IntN(4) == 0 {
				name = source + name
			}
			got := false
			RedisCandidates(name, source, func(seg string) bool {
				if used[seg] {
					return true
				}
				used[seg], got = true, true
				prefixes = append(prefixes, RedisPrefix(seg, source))
				return false
			})
			if !got {
				t.Fatalf("berth %q beside main's %q got no segment", name, source)
			}
		}
		for _, p := range prefixes {
			if strings.HasPrefix(p, source) {
				t.Fatalf("prefix %q begins with main's %q", p, source)
			}
			for _, q := range prefixes {
				if p != q && strings.HasPrefix(q, p) {
					t.Fatalf("prefix %q begins prefix %q", p, q)
				}
			}
		}
	}
}

func TestRedisPrefixOfABranchNamedLikeMainsPrefixDoesNotBeginWithIt(t *testing.T) {
	var first string
	RedisCandidates("app-redesign", "app_", func(seg string) bool {
		first = seg
		return false
	})
	if p := RedisPrefix(first, "app_"); strings.HasPrefix(p, "app_") {
		t.Errorf("berth app-redesign's prefix %q begins with main's app_", p)
	}
}
