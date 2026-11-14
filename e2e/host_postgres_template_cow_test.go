//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// hostPostgres18Bin finds the bin directory of a Postgres 18 installed on the machine, on
// PATH or where Homebrew, Postgres.app and apt put it.
func hostPostgres18Bin(t *testing.T) string {
	t.Helper()
	dirs := []string{
		"/opt/homebrew/opt/postgresql@18/bin",
		"/usr/local/opt/postgresql@18/bin",
		"/Applications/Postgres.app/Contents/Versions/18/bin",
		"/usr/lib/postgresql/18/bin",
	}
	if p, err := exec.LookPath("initdb"); err == nil {
		dirs = append([]string{filepath.Dir(p)}, dirs...)
	}
	for _, d := range dirs {
		out, err := exec.Command(filepath.Join(d, "initdb"), "--version").Output()
		if err == nil && strings.Contains(string(out), "PostgreSQL) 18.") {
			return d
		}
	}
	t.Skipf("no Postgres 18 initdb on the host, on PATH or in %s", strings.Join(dirs, ", "))
	return ""
}

// copyOnWriteDir is a temporary directory on a filesystem with copy-on-write: APFS on macOS,
// btrfs or XFS on Linux, where the CI btrfs job's BERTH_TEST_BTRFS_DIR is tried too.
func copyOnWriteDir(t *testing.T) string {
	t.Helper()
	switch runtime.GOOS {
	case "darwin":
		dir := t.TempDir()
		out, err := exec.Command("df", "-P", dir).Output()
		if err != nil {
			t.Fatalf("df -P %s: %v", dir, err)
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		device := strings.Fields(lines[len(lines)-1])[0]
		mounts, err := exec.Command("mount").Output()
		if err != nil {
			t.Fatalf("mount: %v", err)
		}
		for _, m := range strings.Split(string(mounts), "\n") {
			if strings.HasPrefix(m, device+" on ") && strings.Contains(m, "(apfs") {
				return dir
			}
		}
		t.Skipf("the temporary directory %s is not on APFS", dir)
	case "linux":
		candidates := []string{t.TempDir()}
		if d := os.Getenv("BERTH_TEST_BTRFS_DIR"); d != "" {
			candidates = append(candidates, d)
		}
		for _, d := range candidates {
			if fs := fsType(t, d); fs == "btrfs" || fs == "xfs" {
				dir, err := os.MkdirTemp(d, "pg18cow")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.RemoveAll(dir) })
				return dir
			}
		}
		t.Skipf("none of %v is on btrfs or XFS", candidates)
	}
	t.Skipf("no copy-on-write filesystem this test knows on %s", runtime.GOOS)
	return ""
}

// The copy-on-write half: a Postgres 18 on the host with its data directory
// on APFS, btrfs or XFS copies a 1 GB database in under 2 seconds, and add says it took the
// copy-on-write path.
func TestTemplateOnPostgres18WithCopyOnWriteCopiesAGigabyteInstantly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("initdb refuses to run as root")
	}
	bin := hostPostgres18Bin(t)
	dir := copyOnWriteDir(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	data := filepath.Join(dir, "data")
	if out, err := exec.Command(filepath.Join(bin, "initdb"), "-D", data, "-U", "postgres", "-A", "trust").CombinedOutput(); err != nil {
		t.Fatalf("initdb: %v\n%s", err, out)
	}
	pgctl := filepath.Join(bin, "pg_ctl")
	start := exec.Command(pgctl, "-D", data, "-l", filepath.Join(dir, "server.log"), "-w",
		"-o", fmt.Sprintf("-p %d -c listen_addresses=127.0.0.1 -c unix_socket_directories=''", port), "start")
	if out, err := start.CombinedOutput(); err != nil {
		log, _ := os.ReadFile(filepath.Join(dir, "server.log"))
		t.Fatalf("pg_ctl start: %v\n%s\n%s", err, out, log)
	}
	t.Cleanup(func() { _ = exec.Command(pgctl, "-D", data, "-m", "immediate", "-w", "stop").Run() })
	psql := func(db, q string) string {
		t.Helper()
		out, err := exec.Command(filepath.Join(bin, "psql"), "-h", "127.0.0.1", "-p", fmt.Sprint(port), "-U", "postgres",
			"-d", db, "-At", "-v", "ON_ERROR_STOP=1", "-c", q).CombinedOutput()
		if err != nil {
			t.Fatalf("psql %q on %s: %v\n%s", q, db, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	m := &hostPostgres{Port: port, User: "u" + randomHex(t, 6), Password: "pw" + randomHex(t, 12)}
	psql("postgres", fmt.Sprintf("CREATE ROLE %q LOGIN CREATEDB PASSWORD '%s'", m.User, m.Password))
	psql("postgres", fmt.Sprintf("CREATE DATABASE app_central OWNER %q", m.User))
	psql("app_central", "CREATE TABLE big AS SELECT g AS id, repeat(md5(g::text), 28) AS pad FROM generate_series(1, 1150000) g")
	// A settled database, as main's is, rather than one whose writes are still in memory.
	psql("postgres", "CHECKPOINT")
	if size := psql("postgres", "SELECT pg_database_size('app_central') >= 1000000000"); size != "t" {
		t.Fatalf("main's app_central is under 1 GB")
	}
	rows := "SELECT COUNT(*) || ' ' || md5(string_agg(md5(t::text), '' ORDER BY id)) FROM big t"
	want := psql("app_central", rows)

	repo := NewRepo(t)
	repo.Git(t, "rm", "-q", "compose.yml")
	repo.Git(t, "commit", "-q", "-m", "no compose project")
	pointMainAtPostgres(t, repo, m, "DB_DATABASE_PREFIX=app_", "DB_DATABASE=app_central")
	writeFile(t, repo.privateConfig(), strings.TrimPrefix(centralTemplateConfig, unpublishedDB))
	wt := repo.AddWorktree(t, "feat-cow")
	res := mustBerth(t, repo, wt, "add", "--wait")
	p := berthPrefix(t, wt)

	status := mustBerth(t, repo, wt, "status", "--json")
	var s struct {
		State []struct {
			Kind    string   `json:"kind"`
			Name    string   `json:"name"`
			Status  string   `json:"status"`
			Seconds *float64 `json:"seconds"`
		} `json:"state"`
	}
	if err := json.Unmarshal([]byte(status.Stdout), &s); err != nil {
		t.Fatalf("berth status --json: %v\n%s", err, status.Stdout)
	}
	reported := false
	for _, e := range s.State {
		if e.Kind != "database" || e.Name != "central" {
			continue
		}
		reported = true
		if e.Status != "ready" {
			t.Errorf("central after add --wait is %q, want ready", e.Status)
		}
		if e.Seconds == nil || *e.Seconds >= 2 {
			t.Errorf("central's 1 GB copy took %v seconds, want under 2", e.Seconds)
		} else {
			t.Logf("central's 1 GB copy took %.2fs", *e.Seconds)
		}
	}
	if !reported {
		t.Fatalf("status lists no database central:\n%s", status.Stdout)
	}
	lines := dependencyReport(res, "central")
	cow := false
	for _, l := range lines {
		cow = cow || saysCopyOnWrite(l)
	}
	if !cow || reportsFullCopy(lines) {
		t.Errorf("add does not say central took the copy-on-write path, and only that; lines naming it: %q\nstdout:\n%s\nstderr:\n%s", lines, res.Stdout, res.Stderr)
	}
	if got := psql(p+"central", rows); got != want {
		t.Errorf("the berth's %scentral holds %q, main's app_central %q", p, got, want)
	}
}
