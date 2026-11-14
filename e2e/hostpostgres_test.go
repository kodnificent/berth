//go:build e2e

package e2e

import (
	"fmt"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	postgres13 = "13.22"
	postgres18 = "18.1"
)

// hostPostgres is a Postgres server in Docker, published on a host port, so berth
// reaches it exactly as it would a server installed on the machine.
type hostPostgres struct {
	Container string
	Port      int
	User      string
	Password  string
}

// startHostPostgres starts a server for repo, named after its project so cleanupDocker
// would catch it too, and removes it with every database on it when the test ends. The
// login is not a superuser, so berth is held to what an app's own role can do.
func startHostPostgres(t *testing.T, repo *Repo, suffix, version string) *hostPostgres {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	m := &hostPostgres{
		Container: repo.Project + "-hostpg" + suffix,
		Port:      port,
		User:      "u" + randomHex(t, 6),
		Password:  "pw" + randomHex(t, 12),
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", "-v", m.Container).Run() })
	mustDocker(t, "run", "-d", "--name", m.Container,
		"-e", "POSTGRES_PASSWORD=hostroot",
		"-p", fmt.Sprintf("127.0.0.1:%d:5432", port),
		"postgres:"+version)
	waitFor(t, 3*time.Minute, "the host Postgres server to accept connections", func() bool {
		// The image's first-run server answers on the socket only, before it has finished
		// initialising, so readiness has to be asked over TCP.
		return exec.Command("docker", "exec", "-e", "PGPASSWORD=hostroot", m.Container,
			"psql", "-h", "127.0.0.1", "-U", "postgres", "-c", "SELECT 1").Run() == nil
	})
	m.sql(t, fmt.Sprintf("CREATE ROLE %q LOGIN CREATEDB PASSWORD '%s'", m.User, m.Password))
	return m
}

// query runs q on db as the server's superuser without failing the test, so it can be
// polled from a goroutine.
func (m *hostPostgres) query(db, q string) (string, error) {
	out, err := exec.Command("docker", "exec", "-e", "PGPASSWORD=hostroot", m.Container,
		"psql", "-h", "127.0.0.1", "-U", "postgres", "-d", db, "-At", "-v", "ON_ERROR_STOP=1", "-c", q).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (m *hostPostgres) sqlIn(t *testing.T, db, q string) string {
	t.Helper()
	out, err := m.query(db, q)
	if err != nil {
		t.Fatalf("psql %q on %s/%s: %v\n%s", q, m.Container, db, err, out)
	}
	return out
}

// asLogin opens a new connection to db with the login, as main's app would, and runs q.
func (m *hostPostgres) asLogin(db, q string) (string, error) {
	out, err := exec.Command("docker", "exec", "-e", "PGPASSWORD="+m.Password, "-e", "PGAPPNAME=main_web_app", m.Container,
		"psql", "-h", "127.0.0.1", "-U", m.User, "-d", db, "-At", "-v", "ON_ERROR_STOP=1", "-c", q).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// busy reports whether the login is running a statement on any database but source, which
// while berth copies source is that copy.
func (m *hostPostgres) busy(source string) bool {
	out, err := m.query("postgres", fmt.Sprintf("SELECT COUNT(*) FROM pg_stat_activity WHERE usename = '%s' AND state = 'active' AND datname IS DISTINCT FROM '%s'", m.User, source))
	return err == nil && out != "0"
}

// contents describes db's tables, their columns and a checksum of each table's rows, with
// nothing naming the database, so a copy compares equal to its source.
func (m *hostPostgres) contents(t *testing.T, db string) string {
	t.Helper()
	cols := m.sqlIn(t, db, "SELECT table_name, column_name, data_type, is_nullable FROM information_schema.columns WHERE table_schema = 'public' ORDER BY table_name, ordinal_position")
	sums := []string{cols}
	for _, tb := range strings.Fields(m.sqlIn(t, db, "SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY tablename")) {
		sums = append(sums, tb+" "+m.sqlIn(t, db, fmt.Sprintf(`SELECT COUNT(*) || ' ' || COALESCE(md5(string_agg(md5(t::text), '' ORDER BY md5(t::text))), '') FROM %q t`, tb)))
	}
	return strings.Join(sums, "\n")
}

func (m *hostPostgres) sql(t *testing.T, q string) string {
	t.Helper()
	return m.sqlIn(t, "postgres", q)
}

// create makes databases owned by the login, as an app connecting with it would.
func (m *hostPostgres) create(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		m.sql(t, fmt.Sprintf("CREATE DATABASE %q OWNER %q", n, m.User))
	}
}

// databases lists every database on the server apart from Postgres's own.
func (m *hostPostgres) databases(t *testing.T) []string {
	t.Helper()
	out := strings.Fields(m.sql(t, "SELECT datname FROM pg_database WHERE datname NOT IN ('postgres', 'template0', 'template1')"))
	sort.Strings(out)
	return out
}

func (m *hostPostgres) under(t *testing.T, prefix string) []string {
	t.Helper()
	var out []string
	for _, n := range m.databases(t) {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	return out
}

func (m *hostPostgres) has(t *testing.T, name string) bool {
	t.Helper()
	for _, n := range m.databases(t) {
		if n == name {
			return true
		}
	}
	return false
}

func (m *hostPostgres) mustHave(t *testing.T, when string, names ...string) {
	t.Helper()
	for _, n := range names {
		if !m.has(t, n) {
			t.Errorf("%s, database %s is gone from the server: %v", when, n, m.databases(t))
		}
	}
}

func (m *hostPostgres) sessions(t *testing.T, db, app string) string {
	t.Helper()
	return m.sql(t, fmt.Sprintf("SELECT COUNT(*) FROM pg_stat_activity WHERE datname = '%s' AND application_name = '%s'", db, app))
}

// hold opens a session on db with the login and leaves it idle in a transaction, as an
// app's pooled connection can be, until the test ends. It returns once the server shows
// the session.
func (m *hostPostgres) hold(t *testing.T, db, app string) {
	t.Helper()
	cmd := exec.Command("docker", "exec", "-i", "-e", "PGPASSWORD="+m.Password, "-e", "PGAPPNAME="+app, m.Container,
		"psql", "-h", "127.0.0.1", "-U", m.User, "-d", db, "-At")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	if _, err := io.WriteString(stdin, "BEGIN; SELECT 1;\n"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "session "+app+" on "+db, func() bool { return m.sessions(t, db, app) == "1" })
}

func pointMainAtPostgres(t *testing.T, repo *Repo, m *hostPostgres, extra ...string) {
	t.Helper()
	path := filepath.Join(repo.Dir, ".env")
	env := readDotEnv(t, path)
	env["DB_HOST"] = "127.0.0.1"
	env["DB_PORT"] = fmt.Sprint(m.Port)
	env["DB_USERNAME"] = m.User
	env["DB_PASSWORD"] = m.Password
	for _, kv := range extra {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	writeDotEnv(t, path, env)
}

const hostPostgresConfig = unpublishedDB + `[state.postgres]
from = "env:DB_*"
prefix = "env:DB_DATABASE_PREFIX"
default = "skip"

[state.postgres.databases]
central = "fresh"
"1" = "fresh"
`

// prefixedPostgres is a repo whose main uses a host server holding app_central (with a
// table), app_1 and shop, with app_ as its database prefix and hostPostgresConfig as
// config.
func prefixedPostgres(t *testing.T, version string) (*Repo, *hostPostgres) {
	t.Helper()
	repo := NewRepo(t)
	m := startHostPostgres(t, repo, "", version)
	m.create(t, "app_central", "app_1", "shop")
	m.sqlIn(t, "app_central", "CREATE TABLE users (id INT PRIMARY KEY); INSERT INTO users VALUES (1)")
	pointMainAtPostgres(t, repo, m, "DB_DATABASE_PREFIX=app_", "DB_DATABASE=app_central")
	writeFile(t, repo.privateConfig(), hostPostgresConfig)
	return repo, m
}
