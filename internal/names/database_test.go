package names

import (
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/kodnificent/berth/internal/mysql"
)

// Random berth names, each taking its first segment no other has, give prefixes none of
// which begins another's.
func TestFuzzDatabasePrefixesNeverBeginOneAnother(t *testing.T) {
	alphabet := []rune("ab01-_x")
	r := rand.New(rand.NewPCG(3, 4))
	used := map[string]bool{}
	var prefixes []string
	for i := 0; i < 2000; i++ {
		b := make([]rune, 1+r.IntN(60))
		for j := range b {
			b[j] = alphabet[r.IntN(len(alphabet))]
		}
		DatabaseCandidates(string(b), 12, func(seg string) bool {
			if used[seg] {
				return true
			}
			if len(seg) > 12 {
				t.Fatalf("segment %q is longer than 12", seg)
			}
			used[seg] = true
			prefixes = append(prefixes, DatabasePrefix("app_", seg))
			return false
		})
	}
	for _, p := range prefixes {
		for _, q := range prefixes {
			if p != q && strings.HasPrefix(q, p) {
				t.Fatalf("prefix %q begins prefix %q", p, q)
			}
		}
	}
}

func TestDatabaseNameFitsMySQL(t *testing.T) {
	seg := DatabaseSegment(strings.Repeat("feat-", 20), MaxName)
	got := Database(strings.Repeat("d", 60), seg, mysql.MaxName)
	if len(got) > mysql.MaxName || !strings.HasSuffix(got, "__"+seg) {
		t.Errorf("Database of a 60-character source = %q (%d characters), want at most %d ending in __%s", got, len(got), mysql.MaxName, seg)
	}
	if got := Database("myapp", DatabaseSegment("feat-x", MaxName), mysql.MaxName); got != "myapp__feat_x" {
		t.Errorf("Database(myapp, feat-x) = %q, want myapp__feat_x", got)
	}
}

func TestLongDatabasesSharingAStemGetDistinctNames(t *testing.T) {
	seg := DatabaseSegment(strings.Repeat("feature-", 25), MaxName)
	a := Database("company_analytics_warehouse_events", seg, mysql.MaxName)
	b := Database("company_analytics_warehouse_orders", seg, mysql.MaxName)
	if a == b || len(a) > 64 || len(b) > 64 {
		t.Errorf("Database gave %q and %q, want two distinct names of at most 64 characters", a, b)
	}
}
