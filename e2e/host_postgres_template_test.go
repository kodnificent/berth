//go:build e2e

package e2e

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

const templateBase = unpublishedDB + `[state.postgres]
from = "env:DB_*"
prefix = "env:DB_DATABASE_PREFIX"
default = "skip"
`

const templateConfig = templateBase + `
[state.postgres.databases]
central = "template"
"1" = "template"
`

// centralTemplateConfig copies central alone, so the only copy the server runs is the
// one of main's database a test watches.
const centralTemplateConfig = templateBase + `
[state.postgres.databases]
central = "template"
`

const disconnectConfig = templateBase + `disconnect = true

[state.postgres.databases]
central = "template"
"1" = "template"
`

const templateSeed = `CREATE TABLE accounts (
  id BIGINT PRIMARY KEY,
  email TEXT NOT NULL UNIQUE,
  balance NUMERIC(12,2) NOT NULL,
  joined TIMESTAMPTZ NOT NULL,
  note TEXT,
  avatar BYTEA NOT NULL
);
INSERT INTO accounts
  SELECT g, 'user' || g || '@example.test é😀', g * 1.25,
    TIMESTAMPTZ '2024-01-01 00:00:00+00' + g * INTERVAL '1 second',
    CASE WHEN g % 7 = 0 THEN NULL ELSE 'it''s a \ note ' || repeat('x', g % 300) END,
    decode(md5(g::text), 'hex')
  FROM generate_series(1, 20000) g;
CREATE TABLE orders (id BIGSERIAL PRIMARY KEY, account_id BIGINT NOT NULL REFERENCES accounts (id), total BIGINT NOT NULL);
INSERT INTO orders (account_id, total) SELECT g, g * 100 FROM generate_series(1, 20000) g`

// templateServer is prefixedPostgres with rows of several types in app_central, a table in
// app_1, and both copied by template.
func templateServer(t *testing.T, version string) (*Repo, *hostPostgres) {
	t.Helper()
	repo, m := prefixedPostgres(t, version)
	m.sqlIn(t, "app_central", templateSeed)
	m.sqlIn(t, "app_1", "CREATE TABLE tenants (id INT PRIMARY KEY, name TEXT); INSERT INTO tenants VALUES (1, 'one'), (2, 'two')")
	for _, db := range []string{"app_central", "app_1"} {
		// The seed runs as the superuser, and main's app reads its own tables.
		m.sqlIn(t, db, fmt.Sprintf(`GRANT ALL ON ALL TABLES IN SCHEMA public TO %q`, m.User))
	}
	writeFile(t, repo.privateConfig(), templateConfig)
	return repo, m
}

// fillBig adds about 400 MB to db, so copying it takes long enough on a disk without
// copy-on-write to be watched while it runs.
func (m *hostPostgres) fillBig(t *testing.T, db string) {
	t.Helper()
	m.sqlIn(t, db, "CREATE TABLE big AS SELECT g AS id, repeat(md5(g::text), 28) AS pad FROM generate_series(1, 450000) g")
	// A settled database, as main's is, rather than one whose writes are still in memory.
	m.sql(t, "CHECKPOINT")
}

type outLine struct {
	at   time.Time
	text string
}

// addWaitTimed runs add --wait in wt and notes when each line of its output arrived.
func addWaitTimed(t *testing.T, repo *Repo, wt string) ([]outLine, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	add := exec.Command(berthBin, "add", "--wait")
	add.Dir, add.Env = wt, repo.Env()
	add.Stdout, add.Stderr = w, w
	if err := add.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	var lines []outLine
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		lines = append(lines, outLine{time.Now(), sc.Text()})
	}
	return lines, add.Wait()
}

func firstLine(lines []outLine, match func(string) bool) time.Time {
	for _, l := range lines {
		if match(l.text) {
			return l.at
		}
	}
	return time.Time{}
}

func joinLines(lines []outLine) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.text + "\n")
	}
	return b.String()
}

func containerIP(t *testing.T, container string) string {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", container).Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Fatalf("docker inspect %s for its address: %v %s", container, err, out)
	}
	return strings.TrimSpace(string(out))
}

// "The source is never harmed": the berth's databases hold main's rows,
// and every one of main's databases keeps its checksums.
func TestTemplateCopiesMainsRowsAndLeavesMainsChecksumsUnchanged(t *testing.T) {
	for _, version := range []string{postgres13, postgres18} {
		t.Run(version, func(t *testing.T) {
			repo, m := templateServer(t, version)
			before := map[string]string{}
			for _, db := range postgresMain {
				before[db] = m.contents(t, db)
			}
			wt := repo.AddWorktree(t, "feat-x")
			mustBerth(t, repo, wt, "add", "--wait")
			p := berthPrefix(t, wt)

			for _, key := range []string{"central", "1"} {
				if e := dbState(t, repo, nil, wt, key); e.Status != "ready" || e.Strategy != "template" || e.Target != p+key {
					t.Errorf("database %s after add --wait: status %q strategy %q target %q, want ready template into %s%s", key, e.Status, e.Strategy, e.Target, p, key)
				}
				if got := m.contents(t, p+key); got != before["app_"+key] {
					t.Errorf("the berth's %s%s differs from main's app_%s:\nmain:\n%s\nberth:\n%s", p, key, key, before["app_"+key], got)
				}
			}
			for _, db := range postgresMain {
				if got := m.contents(t, db); got != before[db] {
					t.Errorf("main's %s changed during the copy:\nbefore:\n%s\nafter:\n%s", db, before[db], got)
				}
			}
		})
	}
}

// A session on main's database fails that database, naming the session's
// application and client address, the other database finishes, and the session is left
// as it was.
func TestTemplateFailsADatabaseMainHasASessionOnAndFinishesTheOthers(t *testing.T) {
	for _, version := range []string{postgres13, postgres18} {
		t.Run(version, func(t *testing.T) {
			repo, m := templateServer(t, version)
			// From the container's own address, so the client address can't be mistaken for
			// the server's 127.0.0.1 in a message.
			addr := containerIP(t, m.Container)
			hold := exec.Command("docker", "exec", "-i", "-e", "PGPASSWORD="+m.Password, "-e", "PGAPPNAME=billing_worker", m.Container,
				"psql", "-h", addr, "-U", m.User, "-d", "app_central", "-At")
			stdin, err := hold.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := hold.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { stdin.Close(); _ = hold.Process.Kill(); _ = hold.Wait() })
			if _, err := io.WriteString(stdin, "BEGIN; SELECT 1;\n"); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 30*time.Second, "billing_worker's session on app_central", func() bool { return m.sessions(t, "app_central", "billing_worker") == "1" })
			session := "SELECT pid || ' ' || host(client_addr) || ' ' || xact_start FROM pg_stat_activity WHERE datname = 'app_central' AND application_name = 'billing_worker'"
			before := m.sql(t, session)
			if !strings.Contains(before, " "+addr+" ") {
				t.Fatalf("the held session comes from %q, want %s", before, addr)
			}
			wantOther := m.contents(t, "app_1")
			wt := repo.AddWorktree(t, "feat-x")

			res := repo.Berth(t, wt, "add", "--wait")
			if res.Exit == 0 || res.Exit == 5 {
				t.Fatalf("berth add --wait with a session on app_central: exit %d, want a failed copy\n%s%s", res.Exit, res.Stdout, res.Stderr)
			}
			p := berthPrefix(t, wt)
			e := dbState(t, repo, nil, wt, "central")
			if e.Status != "failed" {
				t.Errorf("central with a session on app_central is %q, want failed: %+v", e.Status, e)
			}
			if !strings.Contains(e.Error, "billing_worker") || !strings.Contains(e.Error, addr) {
				t.Errorf("central's failure does not name the session's application billing_worker and client address %s: %q", addr, e.Error)
			}
			if o := dbState(t, repo, nil, wt, "1"); o.Status != "ready" {
				t.Errorf("database 1, with no session on main's, is %q, want ready: %+v", o.Status, o)
			}
			if got := m.contents(t, p+"1"); got != wantOther {
				t.Errorf("the berth's %s1 differs from main's app_1:\nmain:\n%s\nberth:\n%s", p, wantOther, got)
			}
			if after := m.sql(t, session); after != before {
				t.Errorf("billing_worker's session on app_central changed: before %q, after %q", before, after)
			}
		})
	}
}

var endsSessions = regexp.MustCompile(`(?i)\b(end|ends|ending|disconnect\w*|terminat\w*|clos\w*)\b`)

// With disconnect = true, add says it will end main's sessions before they
// end, copies, and main's app connects again afterwards.
func TestTemplateDisconnectAnnouncesEndingMainsSessionsThenCopies(t *testing.T) {
	for _, version := range []string{postgres13, postgres18} {
		t.Run(version, func(t *testing.T) {
			repo, m := templateServer(t, version)
			writeFile(t, repo.privateConfig(), disconnectConfig)
			want := m.contents(t, "app_central")
			accounts := m.sqlIn(t, "app_central", "SELECT COUNT(*) FROM accounts")
			m.hold(t, "app_central", "main_web_app")
			wt := repo.AddWorktree(t, "feat-x")

			stop, done := make(chan struct{}), make(chan struct{})
			var endedAt time.Time
			go func() {
				defer close(done)
				for {
					select {
					case <-stop:
						return
					default:
					}
					if n, err := m.query("postgres", "SELECT COUNT(*) FROM pg_stat_activity WHERE datname = 'app_central' AND application_name = 'main_web_app'"); err == nil && n == "0" {
						endedAt = time.Now()
						return
					}
					time.Sleep(50 * time.Millisecond)
				}
			}()
			lines, err := addWaitTimed(t, repo, wt)
			close(stop)
			<-done
			if err != nil {
				t.Fatalf("berth add --wait with disconnect = true: %v\n%s", err, joinLines(lines))
			}

			announcedAt := firstLine(lines, func(l string) bool {
				return strings.Contains(strings.ToLower(l), "session") && endsSessions.MatchString(l) &&
					(strings.Contains(l, "central") || strings.Contains(l, "main_web_app"))
			})
			if announcedAt.IsZero() {
				t.Fatalf("berth add never said it would end main's sessions on app_central:\n%s", joinLines(lines))
			}
			if endedAt.IsZero() {
				t.Errorf("main's session on app_central was never ended, yet the copy needs it gone")
			} else if announcedAt.After(endedAt) {
				t.Errorf("main's session on app_central ended by %s, before add said so at %s:\n%s", endedAt.Format(time.StampMilli), announcedAt.Format(time.StampMilli), joinLines(lines))
			}
			p := berthPrefix(t, wt)
			if e := dbState(t, repo, nil, wt, "central"); e.Status != "ready" {
				t.Errorf("central after ending main's sessions is %q, want ready: %+v", e.Status, e)
			}
			if got := m.contents(t, p+"central"); got != want {
				t.Errorf("the berth's %scentral differs from main's app_central:\nmain:\n%s\nberth:\n%s", p, want, got)
			}
			if got, err := m.asLogin("app_central", "SELECT COUNT(*) FROM accounts"); err != nil || got != accounts {
				t.Errorf("main's app connecting to app_central after the copy: %q, %v; want %s accounts", got, err, accounts)
			}
		})
	}
}

// The negative case: disconnect ends sessions on the databases being copied
// and on nothing else of main's.
func TestTemplateDisconnectLeavesSessionsOnOtherDatabases(t *testing.T) {
	repo, m := templateServer(t, postgres18)
	writeFile(t, repo.privateConfig(), disconnectConfig)
	m.hold(t, "shop", "shop_app")
	wt := repo.AddWorktree(t, "feat-x")
	mustBerth(t, repo, wt, "add", "--wait")

	if got := m.sessions(t, "shop", "shop_app"); got != "1" {
		t.Errorf("after add with disconnect = true, the session on shop, which berth doesn't copy: %s open, want 1", got)
	}
}

// Add announces the pause, and a connection to main's database opened while
// the copy runs waits for it and then succeeds.
func TestTemplateAnnouncesThePauseAndMainsNewConnectionsWaitForTheCopy(t *testing.T) {
	for _, version := range []string{postgres13, postgres18} {
		t.Run(version, func(t *testing.T) {
			repo, m := templateServer(t, version)
			writeFile(t, repo.privateConfig(), centralTemplateConfig)
			m.fillBig(t, "app_central")
			accounts := m.sqlIn(t, "app_central", "SELECT COUNT(*) FROM accounts")
			wt := repo.AddWorktree(t, "feat-x")

			stop, done := make(chan struct{}), make(chan struct{})
			var copyStart, copyEnd, connEnd time.Time
			var connOut string
			var connErr error
			go func() {
				defer close(done)
				// Two polls in a row, so a statement over in milliseconds isn't taken for the copy.
				for seen := 0; seen < 2; {
					select {
					case <-stop:
						return
					default:
					}
					if m.busy("app_central") {
						seen++
					} else {
						seen = 0
					}
					time.Sleep(50 * time.Millisecond)
				}
				copyStart = time.Now()
				connDone := make(chan struct{})
				go func() {
					defer close(connDone)
					connOut, connErr = m.asLogin("app_central", "SELECT COUNT(*) FROM accounts")
					connEnd = time.Now()
				}()
				for m.busy("app_central") {
					time.Sleep(50 * time.Millisecond)
				}
				copyEnd = time.Now()
				<-connDone
			}()
			lines, err := addWaitTimed(t, repo, wt)
			close(stop)
			<-done
			if err != nil {
				t.Fatalf("berth add --wait: %v\n%s", err, joinLines(lines))
			}

			announcedAt := firstLine(lines, func(l string) bool {
				return strings.Contains(strings.ToLower(l), "pause") && strings.Contains(l, "central")
			})
			if announcedAt.IsZero() {
				t.Fatalf("berth add never announced the pause on app_central's new connections:\n%s", joinLines(lines))
			}
			if copyStart.IsZero() {
				t.Fatalf("the copy of app_central was never seen running on the server; add said:\n%s", joinLines(lines))
			}
			if announcedAt.After(copyStart) {
				t.Errorf("the copy was running by %s, before add announced the pause at %s", copyStart.Format(time.StampMilli), announcedAt.Format(time.StampMilli))
			}
			if connErr != nil || connOut != accounts {
				t.Errorf("a connection to app_central opened during the copy: %q, %v; want it to succeed with %s accounts", connOut, connErr, accounts)
			}
			if connEnd.Before(copyEnd.Add(-time.Second)) {
				t.Errorf("a connection opened during the copy finished at %s, before the copy ended at %s, so it never waited", connEnd.Format(time.StampMilli), copyEnd.Format(time.StampMilli))
			}
			t.Logf("copy ran from %s to %s; the connection opened during it returned at %s", copyStart.Format(time.StampMilli), copyEnd.Format(time.StampMilli), connEnd.Format(time.StampMilli))
		})
	}
}

// The half without copy-on-write: the Docker server's data directory is on
// ext4, so the copy runs in full and add says so.
func TestTemplateWithoutCopyOnWriteCopiesInFullAndSaysSo(t *testing.T) {
	repo, _ := templateServer(t, postgres18)
	wt := repo.AddWorktree(t, "feat-x")
	res := mustBerth(t, repo, wt, "add", "--wait")

	lines := dependencyReport(res, "central")
	if !reportsFullCopy(lines) {
		t.Errorf("add does not say central was copied in full; lines naming it: %q\nstdout:\n%s\nstderr:\n%s", lines, res.Stdout, res.Stderr)
	}
}

func reportsFullCopy(lines []string) bool {
	for _, l := range lines {
		if l := strings.ToLower(l); strings.Contains(l, "full") || strings.Contains(l, "linear") {
			return true
		}
	}
	return false
}

// kill -9 of the clone process mid-copy reads as failed, the database the
// server was making is gone once berth has seen the failure, and add again copies main's.
func TestKilledTemplateCopyFailsDropsItsDatabaseAndAddRecovers(t *testing.T) {
	for _, version := range []string{postgres13, postgres18} {
		t.Run(version, func(t *testing.T) {
			repo, m := templateServer(t, version)
			writeFile(t, repo.privateConfig(), centralTemplateConfig)
			m.fillBig(t, "app_central")
			want := m.contents(t, "app_central")
			wt := repo.AddWorktree(t, "feat-kill")

			if res := repo.Berth(t, wt, "add"); res.Exit != 0 {
				t.Fatalf("berth add: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
			}
			p := berthPrefix(t, wt)
			var pid int
			waitFor(t, 60*time.Second, "the copy to be running on the server", func() bool {
				e := dbState(t, repo, nil, wt, "central")
				if e.Status == "ready" {
					t.Fatalf("central reported ready before its copy could be killed: %+v", e)
				}
				pid = e.PID
				return e.Status == "cloning" && pid > 0 && m.busy("app_central")
			})

			if err := killNow(pid); err != nil {
				t.Fatalf("kill -9 %d: %v", pid, err)
			}
			waitFor(t, 30*time.Second, "status to report the killed copy as failed", func() bool {
				e := dbState(t, repo, nil, wt, "central")
				if e.Status == "ready" {
					t.Fatalf("central reported ready after its copy was killed mid-way: %+v", e)
				}
				return e.Status == "failed"
			})
			if res := repo.Berth(t, wt, "status", "--wait"); res.Exit == 0 {
				t.Errorf("status --wait exited 0 with central's copy killed\n%s", res.Stdout)
			}
			waitFor(t, 2*time.Minute, "the killed copy's work on the server to end", func() bool { return !m.busy("app_central") })
			mustBerth(t, repo, wt, "status")
			if m.has(t, p+"central") {
				t.Errorf("after the killed copy failed, the database it was making, %scentral, is still on the server: %v", p, m.databases(t))
			}

			mustBerth(t, repo, wt, "add", "--wait")
			if e := dbState(t, repo, nil, wt, "central"); e.Status != "ready" {
				t.Fatalf("after add again, central is %q, want ready: %+v", e.Status, e)
			}
			if got := m.contents(t, p+"central"); got != want {
				t.Errorf("after recovery the berth's %scentral differs from main's app_central:\nmain:\n%s\nberth:\n%s", p, want, got)
			}
		})
	}
}

// add returns while a template copy runs, status shows it cloning, and status --wait
// waits for it.
func TestAddReturnsWhileTemplateCopyingAndStatusShowsCloning(t *testing.T) {
	repo, m := templateServer(t, postgres18)
	want := m.contents(t, "app_central")
	wt := repo.AddWorktree(t, "feat-bg")

	start := time.Now()
	if res := berthWithDelay(t, repo, wt, "15s", "add"); res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("berth add took %s with a 15s copy, want it to return while the copy runs", took)
	}
	if e := dbState(t, repo, nil, wt, "central"); e.Status != "cloning" {
		t.Errorf("central is %q while its copy runs, want cloning: %+v", e.Status, e)
	}
	if human := mustBerth(t, repo, wt, "status").Stdout; !regexp.MustCompile(`database \S*central\s+cloning`).MatchString(human) {
		t.Errorf("human status does not show central cloning:\n%s", human)
	}

	mustBerth(t, repo, wt, "status", "--wait")
	if e := dbState(t, repo, nil, wt, "central"); e.Status != "ready" {
		t.Fatalf("after status --wait, central is %q, want ready: %+v", e.Status, e)
	}
	if got := m.contents(t, berthPrefix(t, wt)+"central"); got != want {
		t.Errorf("after the background copy the berth's central differs from main's:\nmain:\n%s\nberth:\n%s", want, got)
	}
}

func TestAddWaitWaitsForTheTemplateCopy(t *testing.T) {
	repo, m := templateServer(t, postgres18)
	want := m.contents(t, "app_central")
	wt := repo.AddWorktree(t, "feat-wait")

	if res := berthWithDelay(t, repo, wt, "5s", "add", "--wait"); res.Exit != 0 {
		t.Fatalf("berth add --wait: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	if e := dbState(t, repo, nil, wt, "central"); e.Status != "ready" {
		t.Errorf("central when add --wait returned is %q, want ready: %+v", e.Status, e)
	}
	if got := m.contents(t, berthPrefix(t, wt)+"central"); got != want {
		t.Errorf("when add --wait returned the berth's central differs from main's:\nmain:\n%s\nberth:\n%s", want, got)
	}
}

// reset copies main's database again, as main is at the time of the reset.
func TestResetCopiesMainsDatabaseAgainByTemplate(t *testing.T) {
	repo, m := templateServer(t, postgres18)
	wt := repo.AddWorktree(t, "feat-reset")
	mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)
	m.sqlIn(t, p+"central", "DELETE FROM orders WHERE id <= 100; CREATE TABLE berth_only (id INT)")
	m.sqlIn(t, "app_central", "INSERT INTO orders (account_id, total) VALUES (1, 999999); UPDATE accounts SET balance = balance + 1 WHERE id <= 10")
	want := m.contents(t, "app_central")

	mustBerth(t, repo, wt, "reset")
	mustBerth(t, repo, wt, "status", "--wait")

	if e := dbState(t, repo, nil, wt, "central"); e.Status != "ready" || e.Target != p+"central" {
		t.Errorf("central after reset is %q in %q, want ready in %scentral: %+v", e.Status, e.Target, p, e)
	}
	if got := m.contents(t, p+"central"); got != want {
		t.Errorf("after reset the berth's %scentral differs from main's app_central at the time of the reset:\nmain:\n%s\nberth:\n%s", p, want, got)
	}
}
