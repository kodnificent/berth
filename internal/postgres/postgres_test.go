package postgres

import (
	"strings"
	"testing"
)

// Postgres cuts a longer name to 63 bytes without an error, so a drop sent with one would
// act on whichever database has the cut name.
func TestDropRefusesANameThePostgresServerWouldCut(t *testing.T) {
	c := &Conn{}
	if err := c.Drop(strings.Repeat("a", MaxName+1)); err == nil {
		t.Error("Drop accepted a 64-byte name")
	}
}

// pgx and libpq follow dbname in the query over the path berth rewrites.
func TestURLWhoseQueryNamesTheDatabaseIsRefused(t *testing.T) {
	env := map[string]string{"DATABASE_URL": "postgres://u:p@h:5432/app?dbname=app&sslmode=disable"}
	if _, _, err := Engine.Location("", "DATABASE_URL").Server(env); err == nil {
		t.Error("a URL whose query sets dbname was accepted")
	}
}
