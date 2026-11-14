// Package postgres talks to a Postgres server on the host: it lists, creates, drops and
// measures databases. It never keeps a login, and its errors never carry one.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kodnificent/berth/internal/hostdb"
)

// MaxName is the longest database name Postgres keeps. A longer one is silently cut by
// the server, so berth must never send one.
const MaxName = 63

const DefaultPort = 5432

// Engine is Postgres for code that works with any host database server.
var Engine = hostdb.Engine{Name: "Postgres", Table: "postgres", MaxName: MaxName, DefaultPort: DefaultPort,
	URLRouting: []string{"dbname", "database", "host", "hostaddr", "port", "service", "servicefile", "options"},
	Open: func(s hostdb.Server, login hostdb.Login) (hostdb.Conn, error) {
		c, err := Open(s, login)
		if err != nil {
			return nil, err
		}
		return c, nil
	}}

const (
	connectTimeout = 10 * time.Second
	// minVersion is Postgres 13, the first with DROP DATABASE WITH (FORCE).
	minVersion = 130000
	duplicate  = "42P04"
)

// Conn is an open connection to one server. It is used from one goroutine at a time.
type Conn struct {
	conn   *pgx.Conn
	server hostdb.Server
	login  hostdb.Login
	// version is the server's server_version_num, platform the build line version() ends
	// with, which names the operating system the server runs on.
	version  int
	platform string
}

// Open connects to s as login, on the maintenance database every server has, and checks
// the server is one berth can work with.
func Open(s hostdb.Server, login hostdb.Login) (*Conn, error) {
	// Everything but the login goes in the connection string, so a PG* variable or a
	// service file can't point berth at another server, and the login never passes through
	// the string the driver quotes in its errors.
	conf := fmt.Sprintf("host='%s' port=%d dbname=postgres sslmode=prefer connect_timeout=%d application_name=berth target_session_attrs=any",
		strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s.Host), s.Port, int(connectTimeout.Seconds()))
	cfg, err := pgx.ParseConfig(conf)
	if err != nil {
		return nil, fmt.Errorf("the Postgres server at %s: %s", s, hostdb.Scrub(err, login))
	}
	cfg.User, cfg.Password = login.User, login.Password
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("can't reach the Postgres server at %s: %s", s, hostdb.Scrub(err, login))
	}
	c := &Conn{conn: conn, server: s, login: login}
	if err := conn.QueryRow(ctx, "SELECT current_setting('server_version_num')::int, version()").Scan(&c.version, &c.platform); err != nil {
		conn.Close(context.Background())
		return nil, c.fail("reading the server version", err)
	}
	if c.version < minVersion {
		conn.Close(context.Background())
		return nil, fmt.Errorf("the Postgres server at %s is older than version 13, which berth needs", s)
	}
	return c, nil
}

func (c *Conn) Close() error { return c.conn.Close(context.Background()) }

// statementTimeout bounds one statement, so a stuck server can't hold berth's locks for
// ever.
const statementTimeout = 2 * time.Minute

func statementContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), statementTimeout)
}

// Databases lists every database on the server apart from Postgres's own and templates.
func (c *Conn) Databases() ([]string, error) {
	ctx, cancel := statementContext()
	defer cancel()
	rows, err := c.conn.Query(ctx, "SELECT datname FROM pg_database WHERE NOT datistemplate AND datname <> 'postgres'")
	if err != nil {
		return nil, c.fail("listing databases", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, c.fail("listing databases", err)
	}
	return names, nil
}

// checkName refuses a name the server would silently cut, which could make a statement
// act on a different database than the record names.
func checkName(name string) error {
	if name == "" || len(name) > MaxName || strings.ContainsRune(name, 0) {
		return fmt.Errorf("database name %q is not one Postgres can hold exactly", name)
	}
	return nil
}

// Create makes the empty database name, leaving one that exists as it is. Postgres has
// no CREATE DATABASE IF NOT EXISTS, so the server's refusal is what says it exists.
func (c *Conn) Create(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	ctx, cancel := statementContext()
	defer cancel()
	_, err := c.conn.Exec(ctx, "CREATE DATABASE "+Quote(name))
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == duplicate {
		return nil
	}
	if err != nil {
		return c.fail("creating database "+name, err)
	}
	return nil
}

// Drop deletes the database name, ending the sessions on it and on no other database.
func (c *Conn) Drop(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	ctx, cancel := statementContext()
	defer cancel()
	if _, err := c.conn.Exec(ctx, "DROP DATABASE IF EXISTS "+Quote(name)+" WITH (FORCE)"); err != nil {
		return c.fail("dropping database "+name, err)
	}
	return nil
}

// Size is how much disk the database name takes.
func (c *Conn) Size(name string) (int64, error) {
	if err := checkName(name); err != nil {
		return 0, err
	}
	ctx, cancel := statementContext()
	defer cancel()
	var size int64
	if err := c.conn.QueryRow(ctx, "SELECT pg_database_size($1::text::name)", name).Scan(&size); err != nil {
		return 0, c.fail("measuring database "+name, err)
	}
	return size, nil
}

// DataDir is where the server keeps its data, which only a superuser or a member of
// pg_read_all_settings may read.
func (c *Conn) DataDir() (string, error) {
	ctx, cancel := statementContext()
	defer cancel()
	var dir string
	if err := c.conn.QueryRow(ctx, "SELECT current_setting('data_directory')").Scan(&dir); err != nil {
		return "", c.fail("reading the data directory", err)
	}
	return dir, nil
}

func (c *Conn) fail(what string, err error) error {
	return fmt.Errorf("%s on the Postgres server at %s: %s", what, c.server, hostdb.Scrub(err, c.login))
}

// Quote writes name as a Postgres identifier, always quoted: an unquoted identifier is
// folded to lowercase, so a mixed-case name would mean a different database.
func Quote(name string) string { return pgx.Identifier{name}.Sanitize() }
