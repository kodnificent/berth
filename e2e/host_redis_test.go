//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mainRedisPrefix = "app_"

var mainRedisKeys = []string{"app_session:1", "app_cache:users", "app_queue:default"}

// redisBerth is a repo whose main uses a host Redis with app_ as its prefix and keys of
// its own on logical databases 0 and 1.
func redisBerth(t *testing.T) (*Repo, *hostRedis) {
	t.Helper()
	repo := NewRepo(t)
	r := startHostRedis(t, repo)
	r.set(t, 0, mainRedisKeys...)
	r.set(t, 1, mainRedisKeys...)
	pointMainAtRedis(t, repo, r, mainRedisPrefix)
	return repo, r
}

func TestHostRedisPrefixesAreTheirOwnAndRemoveLeavesTheRest(t *testing.T) {
	repo, r := redisBerth(t)
	names := []string{"feat", "feat-x", "feat-x-y"}
	wts, prefixes := map[string]string{}, map[string]string{}
	for _, n := range names {
		wts[n] = repo.AddWorktree(t, n)
		mustBerth(t, repo, wts[n], "add", "--wait")
		prefixes[n] = redisPrefix(t, wts[n])
	}
	for _, a := range names {
		for _, b := range names {
			if a != b && strings.HasPrefix(prefixes[b], prefixes[a]) {
				t.Errorf("berth %s's prefix %q begins berth %s's prefix %q", a, prefixes[a], b, prefixes[b])
			}
		}
		// Either way round, some key main's app could write would fall under the berth's prefix.
		if strings.HasPrefix(mainRedisPrefix, prefixes[a]) || strings.HasPrefix(prefixes[a], mainRedisPrefix) {
			t.Errorf("berth %s's prefix %q and main's prefix %q overlap", a, prefixes[a], mainRedisPrefix)
		}
		for _, db := range []int{0, 1} {
			if got := r.under(t, db, prefixes[a]); len(got) > 0 {
				t.Errorf("berth %s starts with keys of main's under its prefix on database %d: %q", a, db, got)
			}
			r.set(t, db, prefixes[a]+"session:1", prefixes[a]+"cache:users")
		}
	}

	for i, n := range names {
		removeJSON(t, repo, wts[n])
		for _, db := range []int{0, 1} {
			if left := r.under(t, db, prefixes[n]); len(left) > 0 {
				t.Errorf("after removing %s, its keys remain on database %d: %q", n, db, left)
			}
			r.mustHave(t, "after removing "+n, db, mainRedisKeys...)
			for _, other := range names[i+1:] {
				r.mustHave(t, "after removing "+n, db, prefixes[other]+"session:1", prefixes[other]+"cache:users")
			}
		}
	}
}

func TestHostRedisRemoveDeletesTheBerthsKeysOnEveryLogicalDatabase(t *testing.T) {
	repo, r := redisBerth(t)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")
	p := redisPrefix(t, wt)
	dbs := []int{0, 1, 15}
	for _, db := range dbs {
		r.set(t, db, p+"session:1", p+"cache:users")
	}

	removeJSON(t, repo, wt)
	for _, db := range dbs {
		if left := r.under(t, db, p); len(left) > 0 {
			t.Errorf("after remove, the berth's keys remain on database %d: %q", db, left)
		}
	}
	r.mustHave(t, "after remove", 0, mainRedisKeys...)
	r.mustHave(t, "after remove", 1, mainRedisKeys...)
}

func TestHostRedisPrefixWithKeysIsPassedOver(t *testing.T) {
	repo, r := redisBerth(t)
	// Whether {name} keeps feat-x's dash or not, its first choice already has a key.
	taken := []string{"feat_x__app_cache:kept", "feat-x__app_cache:kept"}
	r.set(t, 1, taken...)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")
	p := redisPrefix(t, wt)

	for _, db := range []int{0, 1} {
		if got := r.under(t, db, p); len(got) > 0 {
			t.Errorf("berth feat-x got prefix %q, which keys on database %d are already under: %q", p, db, got)
		}
	}
	r.set(t, 0, p+"session:1")
	removeJSON(t, repo, wt)
	r.mustHave(t, "after remove", 1, taken...)
	r.mustHave(t, "after remove", 0, mainRedisKeys...)
}

// globTwin is name with each glob character in it replaced by text the glob would match,
// so a deletion that read the berth's prefix as a pattern would delete keys under it.
func globTwin(name string) string {
	return strings.NewReplacer("[1]", "1", "?", "x", "*", "zz", `\`, "").Replace(name)
}

func TestHostRedisRemoveTreatsGlobCharactersInKeysAndPrefixesAsText(t *testing.T) {
	cases := []struct {
		name, mainPrefix string
	}{
		{"plain", "app_"},
		{"brackets", "app[1]_"},
		{"question mark", "a?p_"},
		{"star", "ap*_"},
		{"backslash", `app\_`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := NewRepo(t)
			r := startHostRedis(t, repo)
			pointMainAtRedis(t, repo, r, c.mainPrefix)
			wt := repo.AddWorktree(t, "feat")
			mustBerth(t, repo, wt, "add", "--wait")
			p := redisPrefix(t, wt)

			own := []string{p + "x", p + "*", p + "?", p + "[x]", p + `\`}
			survivors := []string{
				c.mainPrefix + "x", c.mainPrefix + "*", c.mainPrefix + "?", c.mainPrefix + "[f]eat",
				"fea?__" + c.mainPrefix + "x", "f*",
			}
			if twin := globTwin(p); twin != p {
				survivors = append(survivors, twin+"x", strings.ReplaceAll(twin, "zz", "")+"x")
			} else if c.mainPrefix != "app_" {
				t.Fatalf("berth feat's prefix %q holds none of main's prefix %q, which the design puts in it", p, c.mainPrefix)
			}
			r.set(t, 0, own...)
			r.set(t, 0, survivors...)

			removeJSON(t, repo, wt)
			r.mustLack(t, "after remove", 0, own...)
			r.mustHave(t, "after remove", 0, survivors...)
		})
	}
}

func TestHostRedisAddRefusesAnEmptyMainPrefix(t *testing.T) {
	repo := NewRepo(t)
	r := startHostRedis(t, repo)
	r.set(t, 0, "session:1", "cache:users")
	pointMainAtRedis(t, repo, r, "")
	wt := repo.AddWorktree(t, "feat-x")

	res := repo.Berth(t, wt, "add", "--wait")
	if res.Exit != 1 {
		t.Errorf("add with main's REDIS_PREFIX empty: exit %d, want 1\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "REDIS_PREFIX") {
		t.Errorf("add with main's REDIS_PREFIX empty does not say to set REDIS_PREFIX:\n%s", res.Stderr)
	}
	if got := strings.Join(r.keys(t, 0), ","); got != "cache:users,session:1" {
		t.Errorf("the refused add changed main's keys to %q", got)
	}
}

func TestHostRedisRemoveWorksWithMainsEnvDeleted(t *testing.T) {
	repo, r := redisBerth(t)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")
	p := redisPrefix(t, wt)
	r.set(t, 0, p+"session:1")
	r.set(t, 1, p+"session:1")
	if err := os.Remove(filepath.Join(repo.Dir, ".env")); err != nil {
		t.Fatal(err)
	}

	removeJSON(t, repo, wt)
	for _, db := range []int{0, 1} {
		if left := r.under(t, db, p); len(left) > 0 {
			t.Errorf("after remove with main's .env deleted, the berth's keys remain on database %d: %q", db, left)
		}
		r.mustHave(t, "after remove", db, mainRedisKeys...)
	}
}

func TestHostRedisPruneDeletesAnOrphansKeysOnEveryLogicalDatabase(t *testing.T) {
	repo, r := redisBerth(t)
	gone, kept := repo.AddWorktree(t, "feat-x"), repo.AddWorktree(t, "feat-y")
	mustBerth(t, repo, gone, "add", "--wait")
	mustBerth(t, repo, kept, "add", "--wait")
	p, other := redisPrefix(t, gone), redisPrefix(t, kept)
	for _, db := range []int{0, 1} {
		r.set(t, db, p+"session:1", other+"session:1")
	}
	repo.Git(t, "worktree", "remove", "--force", gone)

	pruneJSON(t, repo)
	for _, db := range []int{0, 1} {
		if left := r.under(t, db, p); len(left) > 0 {
			t.Errorf("after prune, the orphan's keys remain on database %d: %q", db, left)
		}
		r.mustHave(t, "after prune", db, mainRedisKeys...)
		r.mustHave(t, "after prune", db, other+"session:1")
	}
}

func TestHostRedisResetDeletesTheBerthsKeysOnEveryLogicalDatabase(t *testing.T) {
	repo, r := redisBerth(t)
	wt, sibling := repo.AddWorktree(t, "feat-x"), repo.AddWorktree(t, "feat-y")
	mustBerth(t, repo, wt, "add", "--wait")
	mustBerth(t, repo, sibling, "add", "--wait")
	p, other := redisPrefix(t, wt), redisPrefix(t, sibling)
	for _, db := range []int{0, 1} {
		r.set(t, db, p+"session:1", other+"session:1")
	}

	mustBerth(t, repo, wt, "reset")
	for _, db := range []int{0, 1} {
		if left := r.under(t, db, p); len(left) > 0 {
			t.Errorf("after reset, the berth's keys remain on database %d: %q", db, left)
		}
		r.mustHave(t, "after reset", db, mainRedisKeys...)
		r.mustHave(t, "after reset", db, other+"session:1")
	}
}

func TestHostRedisStatusShowsThePrefix(t *testing.T) {
	repo, _ := redisBerth(t)
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")
	p := redisPrefix(t, wt)

	if out := mustBerth(t, repo, wt, "status").Stdout; !strings.Contains(out, p) {
		t.Errorf("status does not show the berth's Redis prefix %q:\n%s", p, out)
	}
}
