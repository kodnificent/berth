package redis

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kodnificent/berth/internal/hostdb"
)

// fakeServer speaks enough RESP to stand in for Redis. Its SCAN ignores MATCH and
// returns every key, as a server reading the pattern differently might, so only berth's
// own byte comparison keeps other keys safe.
type fakeServer struct {
	dbs      []map[string]bool
	noConfig bool
	noInfo   bool
	noUnlink bool
	password string
	// authReply, when set, is how a server without a password answers AUTH.
	authReply string
}

func startFake(t *testing.T, s *fakeServer) hostdb.Server {
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
			go s.serve(nc)
		}
	}()
	return hostdb.Server{Host: "127.0.0.1", Port: l.Addr().(*net.TCPAddr).Port}
}

func (s *fakeServer) serve(nc net.Conn) {
	defer nc.Close()
	r, w := bufio.NewReader(nc), bufio.NewWriter(nc)
	db, authed := 0, s.password == ""
	for {
		req, err := readReply(r)
		if err != nil {
			return
		}
		var args []string
		for _, a := range req.Array {
			args = append(args, string(a.Str))
		}
		out := s.answer(args, &db, &authed)
		w.WriteString(out)
		w.Flush()
	}
}

func bulk(s string) string { return fmt.Sprintf("$%d\r\n%s\r\n", len(s), s) }

func (s *fakeServer) answer(args []string, db *int, authed *bool) string {
	cmd := strings.ToUpper(args[0])
	if cmd == "AUTH" && s.authReply != "" {
		return "-" + s.authReply + "\r\n"
	}
	if cmd == "AUTH" {
		if args[len(args)-1] != s.password {
			return "-WRONGPASS invalid username-password pair or user is disabled.\r\n"
		}
		*authed = true
		return "+OK\r\n"
	}
	if !*authed {
		return "-NOAUTH Authentication required.\r\n"
	}
	switch cmd {
	case "PING":
		return "+PONG\r\n"
	case "CONFIG":
		if s.noConfig {
			return "-ERR unknown command 'CONFIG'\r\n"
		}
		return "*2\r\n" + bulk("databases") + bulk(strconv.Itoa(len(s.dbs)))
	case "INFO":
		if s.noInfo {
			return "-NOPERM this user has no permissions to run the 'info' command\r\n"
		}
		var b strings.Builder
		b.WriteString("# Keyspace\r\n")
		for i, keys := range s.dbs {
			if len(keys) > 0 {
				fmt.Fprintf(&b, "db%d:keys=%d,expires=0,avg_ttl=0\r\n", i, len(keys))
			}
		}
		return bulk(b.String())
	case "SELECT":
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 0 || n >= len(s.dbs) {
			return "-ERR DB index is out of range\r\n"
		}
		*db = n
		return "+OK\r\n"
	case "SCAN":
		keys := slices.Sorted(func(yield func(string) bool) {
			for k := range s.dbs[*db] {
				if !yield(k) {
					return
				}
			}
		})
		out := "*2\r\n" + bulk("0") + fmt.Sprintf("*%d\r\n", len(keys))
		for _, k := range keys {
			out += bulk(k)
		}
		return out
	case "UNLINK", "DEL":
		if cmd == "UNLINK" && s.noUnlink {
			return "-ERR unknown command 'UNLINK'\r\n"
		}
		n := 0
		for _, k := range args[1:] {
			if s.dbs[*db][k] {
				delete(s.dbs[*db], k)
				n++
			}
		}
		return fmt.Sprintf(":%d\r\n", n)
	}
	return "-ERR unknown command\r\n"
}

func keysOn(keys ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range keys {
		m[k] = true
	}
	return m
}

func remaining(m map[string]bool) []string {
	return slices.Sorted(func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	})
}

func open(t *testing.T, s *fakeServer, login hostdb.Login) *Conn {
	t.Helper()
	c, err := Open(startFake(t, s), login)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestDeleteUnderLeavesEveryKeyNotBeginningWithThePrefixByteForByte(t *testing.T) {
	prefix := "feat__a?p[1]_"
	outside := []string{"feat__axp1_x", "feat__a?p[1]", "feat__app1_x", "a?p[1]_x", "f*", "\xfffeat__a?p[1]_x"}
	s := &fakeServer{dbs: []map[string]bool{
		keysOn(append([]string{prefix + "x", prefix + "*", prefix + "\x00\xff"}, outside...)...),
		{},
		keysOn(append([]string{prefix + "session:1"}, outside...)...),
	}}
	c := open(t, s, hostdb.Login{})

	n, err := c.DeleteUnder([]byte(prefix))
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("deleted %d keys, want 4", n)
	}
	want := slices.Sorted(slices.Values(outside))
	for _, db := range []int{0, 2} {
		if got := remaining(s.dbs[db]); !slices.Equal(got, want) {
			t.Errorf("database %d keeps %q, want %q", db, got, want)
		}
	}
}

func TestEmptyPrefixDeletesNothing(t *testing.T) {
	s := &fakeServer{dbs: []map[string]bool{keysOn("app_x", "session:1")}}
	c := open(t, s, hostdb.Login{})

	if _, err := c.DeleteUnder(nil); err == nil {
		t.Error("DeleteUnder with an empty prefix returned no error")
	}
	if got := remaining(s.dbs[0]); len(got) != 2 {
		t.Errorf("database 0 keeps %q, want both keys", got)
	}
}

func TestAnyUnderFindsAKeyOnALaterDatabaseOnly(t *testing.T) {
	s := &fakeServer{dbs: []map[string]bool{keysOn("feat__app_", "feat__appx"), {}, keysOn("feat__app_x")}}
	c := open(t, s, hostdb.Login{})

	if found, err := c.AnyUnder([]byte("feat__app_x")); err != nil || !found {
		t.Errorf("AnyUnder(feat__app_x) = %v, %v; want the key on database 2", found, err)
	}
	if found, err := c.AnyUnder([]byte("feat__app_y")); err != nil || found {
		t.Errorf("AnyUnder(feat__app_y) = %v, %v; want nothing found", found, err)
	}
}

func TestDatabasesComeFromInfoKeyspaceWhenConfigIsRefused(t *testing.T) {
	dbs := make([]map[string]bool, 20)
	for i := range dbs {
		dbs[i] = map[string]bool{}
	}
	dbs[0], dbs[17] = keysOn("app_x"), keysOn("feat__app_x", "app_x")
	s := &fakeServer{dbs: dbs, noConfig: true}
	c := open(t, s, hostdb.Login{})

	if got, err := c.Databases(); err != nil || !slices.Equal(got, []int{0, 17}) {
		t.Fatalf("Databases() = %v, %v; want [0 17]", got, err)
	}
	if _, err := c.DeleteUnder([]byte("feat__app_")); err != nil {
		t.Fatal(err)
	}
	if got := remaining(s.dbs[17]); !slices.Equal(got, []string{"app_x"}) {
		t.Errorf("database 17 keeps %q, want only app_x", got)
	}
}

func TestDatabasesAreSelectedUpwardWhenConfigAndInfoAreRefused(t *testing.T) {
	s := &fakeServer{dbs: []map[string]bool{{}, {}, {}, keysOn("feat__app_x")}, noConfig: true, noInfo: true}
	c := open(t, s, hostdb.Login{})

	if got, err := c.Databases(); err != nil || !slices.Equal(got, []int{0, 1, 2, 3}) {
		t.Fatalf("Databases() = %v, %v; want [0 1 2 3]", got, err)
	}
	if _, err := c.DeleteUnder([]byte("feat__app_")); err != nil {
		t.Fatal(err)
	}
	if len(s.dbs[3]) != 0 {
		t.Errorf("database 3 keeps %q", remaining(s.dbs[3]))
	}
}

func TestDeleteFallsBackToDelOnAServerWithoutUnlink(t *testing.T) {
	s := &fakeServer{dbs: []map[string]bool{keysOn("feat__app_x", "app_x")}, noUnlink: true}
	c := open(t, s, hostdb.Login{})

	if _, err := c.DeleteUnder([]byte("feat__app_")); err != nil {
		t.Fatal(err)
	}
	if got := remaining(s.dbs[0]); !slices.Equal(got, []string{"app_x"}) {
		t.Errorf("database 0 keeps %q, want only app_x", got)
	}
}

func TestOpenLogsInWithThePassword(t *testing.T) {
	s := &fakeServer{dbs: []map[string]bool{keysOn("feat__app_x")}, password: "s3cret-pw"}
	c := open(t, s, hostdb.Login{User: "default", Password: "s3cret-pw"})

	if found, err := c.AnyUnder([]byte("feat__app_")); err != nil || !found {
		t.Errorf("AnyUnder after logging in = %v, %v", found, err)
	}
}

func TestAPasswordAServerWithoutOneRejectsIsPassedOver(t *testing.T) {
	for _, answer := range []string{
		"ERR AUTH <password> called without any password configured for the default user. Are you sure your configuration is correct?",
		"ERR Client sent AUTH, but no password is set",
	} {
		s := &fakeServer{dbs: []map[string]bool{keysOn("feat__app_x")}, authReply: answer}
		c, err := Open(startFake(t, s), hostdb.Login{Password: "null"})
		if err != nil {
			t.Errorf("Open against a server answering %q: %v", answer, err)
			continue
		}
		if found, err := c.AnyUnder([]byte("feat__app_")); err != nil || !found {
			t.Errorf("AnyUnder after %q = %v, %v", answer, found, err)
		}
		c.Close()
	}
}

func TestAWrongPasswordOnAProtectedServerIsRefused(t *testing.T) {
	s := &fakeServer{dbs: []map[string]bool{{}}, password: "right"}
	if _, err := Open(startFake(t, s), hostdb.Login{Password: "wrong"}); err == nil || !strings.Contains(err.Error(), "refused the login") {
		t.Errorf("Open with a wrong password = %v, want the login refused", err)
	}
}

func TestRefusedLoginNamesNeitherUserNorPassword(t *testing.T) {
	s := &fakeServer{dbs: []map[string]bool{{}}, password: "right"}
	_, err := Open(startFake(t, s), hostdb.Login{User: "someuser", Password: "wrong-pw"})
	if err == nil {
		t.Fatal("Open with a wrong password returned no error")
	}
	if strings.Contains(err.Error(), "wrong-pw") || strings.Contains(err.Error(), "someuser") {
		t.Errorf("the error shows the login: %v", err)
	}
}

func TestPatternEscapesEveryGlobCharacter(t *testing.T) {
	if got, want := string(Pattern([]byte(`a*b?c[d]e\f`))), `a\*b\?c\[d\]e\\f*`; got != want {
		t.Errorf("Pattern = %q, want %q", got, want)
	}
}

func TestReadReplyReadsNestedArraysNullsAndBinaryBulk(t *testing.T) {
	raw := "*3\r\n$-1\r\n*2\r\n:42\r\n$3\r\n\x00\r\n\r\n+OK\r\n"
	rep, err := readReply(bufio.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Array) != 3 || rep.Array[0].Str != nil || rep.Array[1].Array[0].Int != 42 ||
		!bytes.Equal(rep.Array[1].Array[1].Str, []byte("\x00\r\n")) || string(rep.Array[2].Str) != "OK" {
		t.Errorf("readReply = %+v", rep)
	}
}

func TestReadReplyRefusesALengthPastTheBound(t *testing.T) {
	if _, err := readReply(bufio.NewReader(strings.NewReader("*999999999999\r\n"))); err == nil {
		t.Error("readReply accepted an array length past its bound")
	}
}
