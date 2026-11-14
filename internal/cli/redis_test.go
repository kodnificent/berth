package cli

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kodnificent/berth/internal/hostdb"
	"github.com/kodnificent/berth/internal/redis"
	"github.com/kodnificent/berth/internal/registry"
)

// emptyRedis is a server with one logical database and no keys, enough for berth to
// look for keys and find none.
func emptyRedis(t *testing.T) hostdb.Server {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			nc, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer nc.Close()
				r := bufio.NewReader(nc)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if !strings.HasPrefix(line, "*") {
						continue
					}
					var cmd string
					for i := 0; i < 2; i++ {
						if cmd, err = r.ReadString('\n'); err != nil {
							return
						}
					}
					answers := map[string]string{
						"PING":   "+PONG\r\n",
						"CONFIG": "*2\r\n$9\r\ndatabases\r\n$1\r\n1\r\n",
						"SELECT": "+OK\r\n",
						"SCAN":   "*2\r\n$1\r\n0\r\n*0\r\n",
					}
					if a, ok := answers[strings.ToUpper(strings.TrimSpace(cmd))]; ok {
						nc.Write([]byte(a))
					}
				}
			}()
		}
	}()
	return hostdb.Server{Host: "127.0.0.1", Port: l.Addr().(*net.TCPAddr).Port}
}

func TestRedisPrefixHeldByAnotherBerthOnTheServerIsPassedOver(t *testing.T) {
	server := emptyRedis(t)
	conn, err := redis.Open(server, hostdb.Login{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cases := []struct {
		name, source, held, host string
	}{
		{"begun by the other's", "app_", "feat_x__app_cache", "localhost"},
		{"beginning the other's", "app_", "feat_x__ap", "localhost"},
		{"on a differently spelled host", "app_", "feat_x__app_", "0.0.0.0"},
		{"begun by the other's on a differently spelled host", "x_", "feat_x__x_app_", "redis.local"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &redisSetup{server: server, conn: conn, source: c.source}
			other := registry.Record{Redis: &registry.Redis{Host: c.host, Port: server.Port, Prefix: c.held}}
			seg, err := r.choose("feat-x", []registry.Record{other})
			if err != nil {
				t.Fatal(err)
			}
			if seg == "feat_x" {
				t.Errorf("with %s held on %s, choose gave feat_x", c.held, c.host)
			}
		})
	}
}

func TestRemoveKeepsTheBerthsEnvWhileItsRedisKeysAreLeft(t *testing.T) {
	t.Setenv("BERTH_STATE_DIR", t.TempDir())
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("REDIS_PASSWORD=pw\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := hostdb.Server{Host: "127.0.0.1", Port: port}
	rec := registry.Record{ID: "a", Files: []string{".env"}, Redis: &registry.Redis{Host: server.Host, Port: port, EnvPrefix: "REDIS_", Prefix: "feat__app_"}}
	td := &teardown{rec: rec, dir: dir, tracked: map[string]bool{},
		items: []item{{kindRedis, "feat__app_"}, {kindFile, ".env"}},
		redis: &redisLogin{server, hostdb.Login{Password: "pw"}}}
	reg, err := registry.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	if _, err := td.execute(reg); err == nil {
		t.Fatal("execute with the server gone succeeded, want an error")
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); err != nil {
		t.Errorf("the berth's .env, which holds the login a retry needs, is gone: %v", err)
	}
}

func TestRemoveKeepsTheRecordWhenTheRedisHostCannotBeFound(t *testing.T) {
	t.Setenv("BERTH_STATE_DIR", t.TempDir())
	server := hostdb.Server{Host: "berth-redis-test.invalid", Port: 6379}
	rec := registry.Record{ID: "a", Redis: &registry.Redis{Host: server.Host, Port: server.Port, EnvPrefix: "REDIS_", Prefix: "feat__app_"}}
	td := &teardown{rec: rec, tracked: map[string]bool{}, items: []item{{kindRedis, "feat__app_"}}, redis: &redisLogin{server: server}}
	reg, err := registry.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	if err := reg.Update(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := td.execute(reg); err == nil {
		t.Error("execute with the Redis host unknown succeeded, want an error")
	}
	if _, ok, err := reg.Get("a"); err != nil || !ok {
		t.Errorf("the berth's record is gone (%v), with its keys still on the server", err)
	}
}

func TestRemoveRefusesKeysUnderAPrefixTheRecordDoesNotHold(t *testing.T) {
	server := emptyRedis(t)
	for _, recorded := range []string{"feat__app_", ""} {
		td := &teardown{rec: registry.Record{ID: "a", Redis: &registry.Redis{Prefix: recorded}}, redis: &redisLogin{server: server}}
		for _, asked := range []string{"app_", ""} {
			if err := td.deleteRedisKeys(asked); err == nil {
				t.Errorf("with %q recorded, deleting keys under %q succeeded", recorded, asked)
			}
		}
	}
}
