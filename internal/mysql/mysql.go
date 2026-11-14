// Package mysql talks to a MySQL server on the host: it lists, creates, drops, measures
// and copies databases. It never keeps a login, and its errors never carry one.
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	driver "github.com/go-sql-driver/mysql"

	"github.com/kodnificent/berth/internal/hostdb"
)

func init() {
	// The driver logs connection trouble to stderr on its own; berth reports it instead.
	_ = driver.SetLogger(log.New(io.Discard, "", 0))
}

// MaxName is the longest database name MySQL accepts.
const MaxName = 64

const DefaultPort = 3306

type (
	Server = hostdb.Server
	Login  = hostdb.Login
)

// Engine is MySQL for code that works with any host database server.
var Engine = hostdb.Engine{Name: "MySQL", Table: "mysql", MaxName: MaxName, DefaultPort: DefaultPort,
	URLRouting: []string{"database", "dbname", "host", "port", "socket", "unix_socket"},
	Open: func(s Server, login Login) (hostdb.Conn, error) {
		c, err := Open(s, login)
		if err != nil {
			return nil, err
		}
		return c, nil
	}}

// Conn is an open connection to one server.
type Conn struct {
	db     *sql.DB
	server Server
	login  Login
}

const timeout = 10 * time.Second

// Open connects to s as login and checks the server answers.
func Open(s Server, login Login) (*Conn, error) {
	db, err := connect(s, login, 1, time.Minute)
	if err != nil {
		return nil, err
	}
	return &Conn{db: db, server: s, login: login}, nil
}

// connect opens a pool of at most conns connections to s, each waiting at most wait for
// the server to answer a read or take a write.
func connect(s Server, login Login, conns int, wait time.Duration) (*sql.DB, error) {
	cfg := driver.NewConfig()
	cfg.User, cfg.Passwd = login.User, login.Password
	cfg.Net, cfg.Addr = "tcp", s.String()
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = timeout, wait, wait
	cfg.TLSConfig = "preferred"
	connector, err := driver.NewConnector(cfg)
	if err != nil {
		return nil, fmt.Errorf("the MySQL server at %s: %s", s, scrub(err, login))
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(conns)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("can't reach the MySQL server at %s: %s", s, scrub(err, login))
	}
	return db, nil
}

func (c *Conn) Close() error { return c.db.Close() }

// Databases lists every database on the server apart from MySQL's own.
func (c *Conn) Databases() ([]string, error) {
	rows, err := c.db.Query("SHOW DATABASES")
	if err != nil {
		return nil, c.fail("listing databases", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, c.fail("listing databases", err)
		}
		if !system[strings.ToLower(name)] {
			out = append(out, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, c.fail("listing databases", err)
	}
	return out, nil
}

var system = map[string]bool{"mysql": true, "sys": true, "information_schema": true, "performance_schema": true}

// Create makes the empty database name, leaving one that exists as it is.
func (c *Conn) Create(name string) error {
	if _, err := c.db.Exec("CREATE DATABASE IF NOT EXISTS " + Quote(name)); err != nil {
		return c.fail("creating database "+name, err)
	}
	return nil
}

// Drop deletes the database name and everything in it.
func (c *Conn) Drop(name string) error {
	if _, err := c.db.Exec("DROP DATABASE IF EXISTS " + Quote(name)); err != nil {
		return c.fail("dropping database "+name, err)
	}
	return nil
}

// Size is how much data and index the database name holds, as it stands now.
func (c *Conn) Size(name string) (int64, error) {
	ctx := context.Background()
	conn, err := c.db.Conn(ctx)
	if err != nil {
		return 0, c.fail("measuring database "+name, err)
	}
	defer conn.Close()
	n, err := measure(ctx, conn, name)
	if err != nil {
		return 0, c.fail("measuring database "+name, err)
	}
	return n, nil
}

// measure is the size of database db on s. The server's own figures lag a bulk load, by
// up to a day for what it caches and for a moment for InnoDB's statistics, so each table
// is analyzed first, which a table refusing doesn't make the size wrong enough to fail.
func measure(ctx context.Context, s session, db string) (int64, error) {
	tables, _, err := listTables(ctx, s, db)
	if err != nil {
		return 0, err
	}
	for _, t := range tables {
		if !t.View {
			_, _ = s.ExecContext(ctx, "ANALYZE LOCAL TABLE "+Quote(db)+"."+Quote(t.Name))
		}
	}
	if tables, _, err = listTables(ctx, s, db); err != nil {
		return 0, err
	}
	var size int64
	for _, t := range tables {
		size += t.Bytes
	}
	return size, nil
}

func (c *Conn) fail(what string, err error) error {
	return fmt.Errorf("%s on the MySQL server at %s: %s", what, c.server, scrub(err, c.login))
}

// Quote writes name as a MySQL identifier.
func Quote(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// scrub takes the login out of a driver error.
var scrub = hostdb.Scrub
