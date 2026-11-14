//go:build e2e

package e2e

import (
	"bufio"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const redisImage = "redis:7.4.2-alpine"

// hostRedis is a Redis server in Docker, published on a host port, so berth reaches it
// exactly as it would a server installed on the machine.
type hostRedis struct {
	Container string
	Port      int
}

// startHostRedis starts a server for repo, named after its project so cleanupDocker
// would catch it too, and removes it with every key on it when the test ends.
func startHostRedis(t *testing.T, repo *Repo) *hostRedis {
	t.Helper()
	r := &hostRedis{Container: repo.Project + "-hostredis"}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", "-v", r.Container).Run() })
	// A port free here may still be taken where Docker binds it, as inside WSL's VM for a
	// Windows client, so a port Docker can't bind is traded for another.
	for attempt := 1; ; attempt++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		r.Port = l.Addr().(*net.TCPAddr).Port
		l.Close()
		out, err := exec.Command("docker", "run", "-d", "--name", r.Container,
			"-p", fmt.Sprintf("127.0.0.1:%d:6379", r.Port), redisImage).CombinedOutput()
		if err == nil {
			break
		}
		if attempt == 3 || !strings.Contains(string(out), "address already in use") {
			t.Fatalf("starting the host Redis server: %v\n%s", err, out)
		}
		_ = exec.Command("docker", "rm", "-f", "-v", r.Container).Run()
	}
	waitFor(t, time.Minute, "the host Redis server to accept connections", func() bool {
		out, err := exec.Command("docker", "exec", r.Container, "redis-cli", "PING").Output()
		return err == nil && strings.TrimSpace(string(out)) == "PONG"
	})
	r.waitPublished(t)
	return r
}

// waitPublished waits for the server to answer on the host port. Docker returns from run,
// and the server answers inside the container, before the port is reachable from every
// host: WSL forwards a port to Windows a moment after Docker binds it in the VM.
func (r *hostRedis) waitPublished(t *testing.T) {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", r.Port)
	const timeout = time.Minute
	deadline := time.Now().Add(timeout)
	for {
		err := pingRedis(addr)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the host Redis server answers inside %s but not at its published address %s after %s: %v", r.Container, addr, timeout, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func pingRedis(addr string) error {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte("PING\r\n")); err != nil {
		return err
	}
	reply, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return err
	}
	if strings.TrimSpace(reply) != "+PONG" {
		return fmt.Errorf("PING answered %q", reply)
	}
	return nil
}

func (r *hostRedis) cli(t *testing.T, db int, args ...string) string {
	t.Helper()
	full := append([]string{"exec", r.Container, "redis-cli", "-n", fmt.Sprint(db)}, args...)
	out, err := exec.Command("docker", full...).CombinedOutput()
	if err != nil || strings.HasPrefix(string(out), "ERR") {
		t.Fatalf("redis-cli -n %d %v on %s: %v\n%s", db, args, r.Container, err, out)
	}
	return string(out)
}

// set writes each key on logical database db, with its own name as the value.
func (r *hostRedis) set(t *testing.T, db int, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if got := strings.TrimSpace(r.cli(t, db, "SET", k, k)); got != "OK" {
			t.Fatalf("SET %q on database %d: %s", k, db, got)
		}
	}
}

// keys lists every key on logical database db. SCAN with no MATCH, so a key name holding
// glob characters is listed as it is.
func (r *hostRedis) keys(t *testing.T, db int) []string {
	t.Helper()
	var out []string
	for _, k := range strings.Split(r.cli(t, db, "--scan"), "\n") {
		if k != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func (r *hostRedis) has(t *testing.T, db int, key string) bool {
	t.Helper()
	return strings.TrimSpace(r.cli(t, db, "EXISTS", key)) == "1"
}

// under lists the keys on db that begin with prefix, compared as plain bytes.
func (r *hostRedis) under(t *testing.T, db int, prefix string) []string {
	t.Helper()
	var out []string
	for _, k := range r.keys(t, db) {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out
}

func (r *hostRedis) mustHave(t *testing.T, when string, db int, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if !r.has(t, db, k) {
			t.Errorf("%s, key %q is gone from database %d: %q", when, k, db, r.keys(t, db))
		}
	}
}

func (r *hostRedis) mustLack(t *testing.T, when string, db int, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if r.has(t, db, k) {
			t.Errorf("%s, key %q remains on database %d", when, k, db)
		}
	}
}

const hostRedisConfig = `[state.redis]
strategy = "prefix"
from = "env:REDIS_*"
prefix = "env:REDIS_PREFIX"
`

// pointMainAtRedis sets the server keys and REDIS_PREFIX in main's .env, leaving the
// fixture's other keys as they are, and writes hostRedisConfig.
func pointMainAtRedis(t *testing.T, repo *Repo, r *hostRedis, prefix string) {
	t.Helper()
	path := filepath.Join(repo.Dir, ".env")
	env := readDotEnv(t, path)
	env["REDIS_HOST"] = "127.0.0.1"
	env["REDIS_PORT"] = fmt.Sprint(r.Port)
	env["REDIS_PREFIX"] = prefix
	writeDotEnv(t, path, env)
	writeFile(t, repo.privateConfig(), hostRedisConfig)
}

func redisPrefix(t *testing.T, wt string) string {
	t.Helper()
	p := blockEnv(t, filepath.Join(wt, ".env"))["REDIS_PREFIX"]
	if p == "" {
		t.Fatalf("the managed block in %s sets no REDIS_PREFIX", wt)
	}
	return p
}
