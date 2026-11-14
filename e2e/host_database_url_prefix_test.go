//go:build e2e

package e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// With a prefix, key laravel maps to main's tenant_laravel, so a URL naming laravel
// itself names no entry's database and the berth would keep main's URL.
func TestDatabaseURLOutsideThePrefixExitsFive(t *testing.T) {
	cases := []struct {
		engine string
		start  func(t *testing.T, repo *Repo) (scheme, user, password string, port int, databases func() string)
	}{
		{"mysql", func(t *testing.T, repo *Repo) (string, string, string, int, func() string) {
			m := startHostMySQL(t, repo, "")
			m.create(t, "laravel", "tenant_laravel")
			return "mysql", m.User, m.Password, m.Port, func() string { return strings.Join(m.databases(t), ",") }
		}},
		{"postgres", func(t *testing.T, repo *Repo) (string, string, string, int, func() string) {
			m := startHostPostgres(t, repo, "", postgres18)
			m.create(t, "laravel", "tenant_laravel")
			return "postgres", m.User, m.Password, m.Port, func() string { return strings.Join(m.databases(t), ",") }
		}},
	}
	for _, c := range cases {
		t.Run(c.engine, func(t *testing.T) {
			repo := NewRepo(t)
			scheme, user, password, port, databases := c.start(t, repo)
			env := readDotEnv(t, filepath.Join(repo.Dir, ".env"))
			env["DATABASE_URL"] = fmt.Sprintf("%s://%s:%s@127.0.0.1:%d/laravel", scheme, user, password, port)
			env["DB_DATABASE_PREFIX"] = "tenant_"
			writeDotEnv(t, filepath.Join(repo.Dir, ".env"), env)
			writeFile(t, repo.privateConfig(), fmt.Sprintf(`[state.%s]
from = "env:DATABASE_URL"
prefix = "env:DB_DATABASE_PREFIX"

[state.%[1]s.databases]
laravel = "fresh"
`, c.engine))
			before := databases()
			wt := repo.AddWorktree(t, "feat-x")

			res := repo.Berth(t, wt, "add")
			if res.Exit != 5 {
				t.Errorf("add with a URL naming a database outside the prefix: exit %d, want 5\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
			}
			if !strings.Contains(res.Stderr, "DATABASE_URL") || !strings.Contains(res.Stderr, "prefix") {
				t.Errorf("add's message %q names neither DATABASE_URL nor the prefix it would have to fit", res.Stderr)
			}
			if got := databases(); got != before {
				t.Errorf("the refused add changed the server's databases from %q to %q", before, got)
			}
		})
	}
}
