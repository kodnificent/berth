//go:build e2e

package e2e

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const hostMySQLRootPassword = "hostroot"

// hostMySQL is a MySQL 8 server in Docker, published on a host port, so berth reaches it
// exactly as it would a server installed on the machine.
type hostMySQL struct {
	Container string
	Port      int
	User      string
	Password  string
}

// startHostMySQL starts a server for repo, named after its project so cleanupDocker
// would catch it too, and removes it with every database on it when the test ends.
func startHostMySQL(t *testing.T, repo *Repo, suffix string) *hostMySQL {
	t.Helper()
	m := &hostMySQL{
		Container: repo.Project + "-hostmysql" + suffix,
		User:      "u" + randomHex(t, 6),
		Password:  "pw" + randomHex(t, 12),
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", "-v", m.Container).Run() })
	// A port free here may still be taken where Docker binds it, as inside WSL's VM for a
	// Windows client, so a port Docker can't bind is traded for another.
	for attempt := 1; ; attempt++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		m.Port = l.Addr().(*net.TCPAddr).Port
		l.Close()
		out, err := exec.Command("docker", "run", "-d", "--name", m.Container,
			"-e", "MYSQL_ROOT_PASSWORD="+hostMySQLRootPassword,
			"-p", fmt.Sprintf("127.0.0.1:%d:3306", m.Port),
			"mysql:8.4.4").CombinedOutput()
		if err == nil {
			break
		}
		if attempt == 3 || !strings.Contains(string(out), "address already in use") {
			t.Fatalf("starting the host MySQL server: %v\n%s", err, out)
		}
		_ = exec.Command("docker", "rm", "-f", "-v", m.Container).Run()
	}
	m.waitReady(t)
	m.sql(t, fmt.Sprintf("CREATE USER '%s'@'%%' IDENTIFIED BY '%s'; GRANT ALL ON *.* TO '%s'@'%%'", m.User, m.Password, m.User))
	return m
}

// waitReady waits for TCP inside the container, because the image's first-run server
// answers on the socket only, before it has finished initialising.
func (m *hostMySQL) waitReady(t *testing.T) {
	t.Helper()
	waitFor(t, 3*time.Minute, "the host MySQL server to accept connections", func() bool {
		return m.query("SELECT 1") == nil
	})
}

func (m *hostMySQL) command(q string) *exec.Cmd {
	return exec.Command("docker", "exec", "-e", "MYSQL_PWD="+hostMySQLRootPassword, m.Container,
		"mysql", "-h127.0.0.1", "-uroot", "-N", "-B", "-e", q)
}

func (m *hostMySQL) query(q string) error { return m.command(q).Run() }

func (m *hostMySQL) sql(t *testing.T, q string) string {
	t.Helper()
	out, err := m.command(q).CombinedOutput()
	if err != nil {
		t.Fatalf("mysql %q on %s: %v\n%s", q, m.Container, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (m *hostMySQL) create(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		m.sql(t, "CREATE DATABASE `"+n+"`")
	}
}

// databases lists every database on the server apart from MySQL's own.
func (m *hostMySQL) databases(t *testing.T) []string {
	t.Helper()
	system := map[string]bool{"mysql": true, "sys": true, "information_schema": true, "performance_schema": true}
	var out []string
	for _, n := range strings.Fields(m.sql(t, "SHOW DATABASES")) {
		if !system[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (m *hostMySQL) under(t *testing.T, prefix string) []string {
	t.Helper()
	var out []string
	for _, n := range m.databases(t) {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	return out
}

func (m *hostMySQL) has(t *testing.T, name string) bool {
	t.Helper()
	for _, n := range m.databases(t) {
		if n == name {
			return true
		}
	}
	return false
}

func (m *hostMySQL) tables(t *testing.T, db string) string {
	t.Helper()
	return m.sql(t, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = '"+db+"'")
}

func (m *hostMySQL) stop(t *testing.T) {
	t.Helper()
	mustDocker(t, "stop", "-t", "2", m.Container)
}

func (m *hostMySQL) start(t *testing.T) {
	t.Helper()
	mustDocker(t, "start", m.Container)
	m.waitReady(t)
}

// pointMainAt sets the server keys in main's .env to m, along with extra key=value
// pairs, leaving the fixture's other keys as they are.
func pointMainAt(t *testing.T, repo *Repo, m *hostMySQL, extra ...string) {
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

// blockEnv is the key=value pairs in the managed block of the .env at path.
func blockEnv(t *testing.T, path string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, line := range strings.Split(managedBlock(t, path), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(k, "#") {
			// berth quotes a value only as a whole, with nothing escaped inside, as a
			// COMPOSE_FILE holding Windows' ; separator is.
			if len(v) >= 2 && (v[0] == '\'' || v[0] == '"') && v[len(v)-1] == v[0] {
				v = v[1 : len(v)-1]
			}
			out[k] = v
		}
	}
	return out
}

// The fixture's compose db service publishes DB_PORT, the key main's .env also uses for
// the host server, so berths leave that port unpublished and never rewrite the key.
const unpublishedDB = "[compose.ports]\n\"db:3306\" = \"none\"\n\n"

const hostDBConfig = unpublishedDB + `[state.mysql]
from = "env:DB_*"
prefix = "env:DB_DATABASE_PREFIX"
default = "skip"

[state.mysql.databases]
central = "fresh"
"1" = "fresh"
`

// prefixedServer is a repo whose main uses a host server holding app_central (with a
// table), app_1 and shop, with app_ as its database prefix and hostDBConfig as config.
func prefixedServer(t *testing.T) (*Repo, *hostMySQL) {
	t.Helper()
	repo := NewRepo(t)
	m := startHostMySQL(t, repo, "")
	m.create(t, "app_central", "app_1", "shop")
	m.sql(t, "CREATE TABLE app_central.users (id INT PRIMARY KEY); INSERT INTO app_central.users VALUES (1)")
	pointMainAt(t, repo, m, "DB_DATABASE_PREFIX=app_", "DB_DATABASE=app_central")
	writeFile(t, repo.privateConfig(), hostDBConfig)
	return repo, m
}

func berthPrefix(t *testing.T, wt string) string {
	t.Helper()
	p := blockEnv(t, filepath.Join(wt, ".env"))["DB_DATABASE_PREFIX"]
	if p == "" {
		t.Fatalf("the managed block in %s sets no DB_DATABASE_PREFIX", wt)
	}
	return p
}

func mustHave(t *testing.T, m *hostMySQL, when string, names ...string) {
	t.Helper()
	for _, n := range names {
		if !m.has(t, n) {
			t.Errorf("%s, database %s is gone from the server: %v", when, n, m.databases(t))
		}
	}
}

func chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
