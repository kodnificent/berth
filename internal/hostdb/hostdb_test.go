package hostdb

import (
	"strings"
	"testing"
)

const secretURL = "postgres://app%40corp:p%2Fss%3Aw0rd@db.local:6543/shop?sslmode=require&application_name=web"

func TestBerthURLKeepsTheEncodedLoginAndQuery(t *testing.T) {
	got := WithDatabase(secretURL, "shop__feat_x")
	want := "postgres://app%40corp:p%2Fss%3Aw0rd@db.local:6543/shop__feat_x?sslmode=require&application_name=web"
	if got != want {
		t.Errorf("WithDatabase = %q, want %q", got, want)
	}
}

func TestURLLoginIsDecoded(t *testing.T) {
	login, ok := Location{URLKey: "DATABASE_URL"}.Login(map[string]string{"DATABASE_URL": secretURL})
	if !ok || login.User != "app@corp" || login.Password != "p/ss:w0rd" {
		t.Errorf("Login = %+v, %v; want app@corp and p/ss:w0rd", login, ok)
	}
}

func TestURLServerDefaultsThePort(t *testing.T) {
	s, missing, err := Location{URLKey: "DATABASE_URL", DefaultPort: 5432}.Server(map[string]string{"DATABASE_URL": "postgresql://u:pw@localhost/app"})
	if err != nil || len(missing) > 0 || s.Port != 5432 || s.Host != "localhost" {
		t.Errorf("Server = %+v, %v, %v; want localhost:5432", s, missing, err)
	}
}

func TestUnreadableURLErrorNeverQuotesIt(t *testing.T) {
	raw := "mysql://app:hunter2secret@db.local:notaport/shop"
	_, _, err := Location{URLKey: "DATABASE_URL", DefaultPort: 3306}.Server(map[string]string{"DATABASE_URL": raw})
	if err == nil {
		t.Fatal("Server accepted a URL with a port that is not a number")
	}
	if strings.Contains(err.Error(), "hunter2") || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("error = %q, want one naming DATABASE_URL and never its password", err)
	}
}

func TestRedactedHidesTheLoginAndQuery(t *testing.T) {
	got := Redacted(secretURL)
	for _, secret := range []string{"app", "p%2Fss", "sslmode"} {
		if strings.Contains(got, secret) {
			t.Errorf("Redacted = %q, which still holds %q", got, secret)
		}
	}
	if !strings.Contains(got, "db.local:6543/shop") {
		t.Errorf("Redacted = %q, want the server and database kept", got)
	}
}

func TestURLQueryThatRedirectsTheConnectionIsRefused(t *testing.T) {
	cases := []struct {
		name    string
		routing []string
		raw     string
		param   string
	}{
		{"postgres dbname", []string{"dbname", "host"}, "postgres://u:hunter2@h:5432/app?dbname=app&sslmode=disable", "dbname"},
		{"postgres host in any case", []string{"dbname", "host"}, "postgresql://u:hunter2@h:5432/app?HOST=elsewhere", "HOST"},
		{"mysql socket", []string{"database", "socket"}, "mysql://u:hunter2@h:3306/app?socket=/tmp/other.sock", "socket"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := Location{URLKey: "DATABASE_URL", Routing: c.routing}.Server(map[string]string{"DATABASE_URL": c.raw})
			if err == nil {
				t.Fatalf("Server accepted %s, whose query sets %s", c.raw, c.param)
			}
			msg := err.Error()
			if !strings.Contains(msg, "DATABASE_URL") || !strings.Contains(msg, c.param) || strings.Contains(msg, "hunter2") || strings.Contains(msg, "elsewhere") {
				t.Errorf("error = %q, want one naming DATABASE_URL and %s, and neither the password nor the value", msg, c.param)
			}
		})
	}
}

func TestURLQueryThatOnlyTunesTheConnectionIsKept(t *testing.T) {
	loc := Location{URLKey: "DATABASE_URL", DefaultPort: 5432, Routing: []string{"dbname", "host"}}
	if _, _, err := loc.Server(map[string]string{"DATABASE_URL": "postgresql://u:pw@h/app?sslmode=require&application_name=web"}); err != nil {
		t.Errorf("Server refused a URL whose query only sets sslmode and application_name: %v", err)
	}
}

func TestURLNamingMoreThanOneHostIsRefused(t *testing.T) {
	_, _, err := Location{URLKey: "DATABASE_URL", DefaultPort: 5432}.Server(map[string]string{"DATABASE_URL": "postgres://u:hunter2@primary:5432,replica:5432/app"})
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("Server on a URL naming two hosts gave %v, want a refusal naming DATABASE_URL and not its password", err)
	}
}
