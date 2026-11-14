//go:build e2e

package e2e

import (
	"testing"
	"time"
)

// CREATE DATABASE stops autovacuum on its source itself, and a login that is not a
// superuser sees an autovacuum worker with no role and no type, so berth must not take it
// for one of main's sessions and refuse the copy.
func TestTemplateCopiesWhileAutovacuumWorksOnMainsDatabase(t *testing.T) {
	for _, version := range []string{postgres13, postgres18} {
		t.Run(version, func(t *testing.T) {
			repo, m := templateServer(t, version)
			writeFile(t, repo.privateConfig(), centralTemplateConfig)
			m.sql(t, "ALTER SYSTEM SET autovacuum_naptime = 1")
			m.sql(t, "SELECT pg_reload_conf()")
			// A vacuum held to one page of work per 100 ms, on a table with plenty to clean,
			// is still running when berth looks.
			m.sqlIn(t, "app_central", `CREATE TABLE churn (id INT, pad TEXT) WITH (autovacuum_vacuum_cost_delay = 100, autovacuum_vacuum_cost_limit = 1, autovacuum_vacuum_threshold = 0, autovacuum_vacuum_scale_factor = 0);
INSERT INTO churn SELECT g, repeat('x', 200) FROM generate_series(1, 200000) g;
DELETE FROM churn WHERE id % 2 = 0`)
			waitFor(t, 2*time.Minute, "autovacuum to work on app_central", func() bool {
				return m.sql(t, "SELECT COUNT(*) FROM pg_stat_activity WHERE datname = 'app_central' AND backend_type = 'autovacuum worker'") != "0"
			})
			want := m.contents(t, "app_central")
			wt := repo.AddWorktree(t, "feat-x")

			res := repo.Berth(t, wt, "add", "--wait")
			if e := dbState(t, repo, nil, wt, "central"); e.Status != "ready" {
				t.Fatalf("central with autovacuum on app_central is %q, want ready: %+v\n%s%s", e.Status, e, res.Stdout, res.Stderr)
			}
			if got := m.contents(t, berthPrefix(t, wt)+"central"); got != want {
				t.Errorf("the berth's central differs from main's app_central:\nmain:\n%s\nberth:\n%s", want, got)
			}
		})
	}
}
