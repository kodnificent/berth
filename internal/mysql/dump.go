package mysql

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Table is one table or view of a database, as the server describes it.
type Table struct {
	Name   string
	View   bool
	Engine string
	// Bytes is the data and index the server counts for the table, Rows its estimate of
	// the rows in it.
	Bytes int64
	Rows  int64
}

// session is one connection to the server, so session settings hold for every query.
type session interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Tables lists database db's tables and views. ok is false when the server has no
// database db.
func (c *Conn) Tables(db string) (tables []Table, ok bool, err error) {
	ctx := context.Background()
	conn, err := c.db.Conn(ctx)
	if err != nil {
		return nil, false, c.fail("reading database "+db, err)
	}
	defer conn.Close()
	tables, ok, err = listTables(ctx, conn, db)
	if err != nil {
		return nil, false, c.fail("reading database "+db, err)
	}
	return tables, ok, nil
}

// DataDir is the directory the server keeps its data in, on the server's machine.
func (c *Conn) DataDir() (string, error) {
	var dir string
	if err := c.db.QueryRow("SELECT @@datadir").Scan(&dir); err != nil {
		return "", c.fail("reading the data directory", err)
	}
	return dir, nil
}

func listTables(ctx context.Context, s session, db string) ([]Table, bool, error) {
	var name string
	err := s.QueryRowContext(ctx, "SELECT schema_name FROM information_schema.schemata WHERE schema_name = ?", db).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	// The server caches table sizes for a day by default, which would count a table
	// filled since as empty. Servers before 8.0 have no cache and no such setting.
	_, _ = s.ExecContext(ctx, "SET SESSION information_schema_stats_expiry = 0")
	rows, err := s.QueryContext(ctx, "SELECT table_name, table_type, COALESCE(engine, ''), COALESCE(data_length, 0) + COALESCE(index_length, 0), COALESCE(table_rows, 0) FROM information_schema.tables WHERE table_schema = ? ORDER BY table_name", db)
	if err != nil {
		return nil, true, err
	}
	defer rows.Close()
	var out []Table
	for rows.Next() {
		var t Table
		var kind string
		if err := rows.Scan(&t.Name, &kind, &t.Engine, &t.Bytes, &t.Rows); err != nil {
			return nil, true, err
		}
		t.View = kind == "VIEW"
		out = append(out, t)
	}
	return out, true, rows.Err()
}

// localEngines are the engines that keep a table's rows in its own database on the
// server. A table of any other engine, such as MERGE or FEDERATED, reads and writes rows
// kept elsewhere, so copying it would reach into another database or another server.
var localEngines = map[string]bool{"innodb": true, "myisam": true, "memory": true, "archive": true, "csv": true, "aria": true}

// Copyable reports whether a copy takes t: every view, and a table whose engine keeps
// its rows locally.
func (t Table) Copyable() bool { return t.View || localEngines[strings.ToLower(t.Engine)] }

// Held names the tables a consistent copy holds writes to while it reads them: every
// table it copies whose engine gives no consistent snapshot, which only InnoDB does.
func Held(tables []Table) []string {
	var out []string
	for _, t := range tables {
		if !t.View && t.Copyable() && !strings.EqualFold(t.Engine, "InnoDB") {
			out = append(out, t.Name)
		}
	}
	return out
}

// CopyOptions are what Copy reports while it runs.
type CopyOptions struct {
	// Holding is called with the tables whose writes are about to be held, before they
	// are. An error stops the copy before anything of main's is held.
	Holding func(tables []string) error
	// Progress is called as rows reach the target, with how much of the source's size,
	// as Tables counts it, they stand for.
	Progress func(copied int64)
	// Done is called once every row, view and trigger is in, before the snapshot ends.
	Done func()
}

// Copied is how a copy went.
type Copied struct {
	// SourceMissing is true when the server had no source database, so the target was
	// left empty.
	SourceMissing bool
	// Bytes is the target's size once copied.
	Bytes int64
	// Held names the tables whose writes were held for the copy, HeldFor for how long.
	Held    []string
	HeldFor time.Duration
	// Notes name what of the source the copy left out, and why.
	Notes []string
}

// copyWait bounds how long any one read or write of a copy may wait on the server,
// which includes waiting for a write in progress on main before a table can be held.
const copyWait = 10 * time.Minute

// Copy replaces database target on s with a copy of database source as it was at one
// moment: its tables with their rows, its views and its triggers. Rows are read in one
// consistent snapshot, from a read-only session, so main's InnoDB tables keep taking
// writes; a table of another engine is held against writes only from the moment the
// snapshot is taken until its own rows are read. The target is dropped first, so
// whatever it held is lost.
func Copy(s Server, login Login, source, target string, o CopyOptions) (out Copied, err error) {
	if source == target {
		return out, fmt.Errorf("database %s would be copied onto itself", source)
	}
	db, err := connect(s, login, 3, copyWait)
	if err != nil {
		return out, err
	}
	defer db.Close()
	ctx := context.Background()
	step := "copying database " + source + " into " + target
	defer func() {
		if err != nil {
			err = fmt.Errorf("%s on the MySQL server at %s: %s", step, s, scrub(err, login))
		}
	}()

	write, err := db.Conn(ctx)
	if err != nil {
		return out, err
	}
	defer write.Close()
	if err := execAll(ctx, write, targetSession...); err != nil {
		return out, err
	}
	var charset, collation string
	err = write.QueryRowContext(ctx, "SELECT default_character_set_name, default_collation_name FROM information_schema.schemata WHERE schema_name = ?", source).Scan(&charset, &collation)
	out.SourceMissing = errors.Is(err, sql.ErrNoRows)
	if err != nil && !out.SourceMissing {
		return out, err
	}
	if err := recreate(ctx, write, target, charset, collation); err != nil {
		return out, err
	}
	if out.SourceMissing {
		return out, nil
	}
	var packet int64
	if err := write.QueryRowContext(ctx, "SELECT @@max_allowed_packet").Scan(&packet); err != nil {
		return out, err
	}
	dest := &databaseSink{session: write, target: target, login: login}
	if err := consistentRead(ctx, db, source, target, dest, newWriter(write, packet), o, &out, &step); err != nil {
		return out, err
	}
	if out.Bytes, err = measure(ctx, write, target); err != nil {
		return out, err
	}
	return out, nil
}

// targetSession readies the session that writes a copy, as mysqldump's output does.
var targetSession = []string{
	"SET SESSION sql_mode = 'NO_AUTO_VALUE_ON_ZERO'",
	"SET SESSION foreign_key_checks = 0",
	"SET SESSION unique_checks = 0",
	"SET SESSION time_zone = '+00:00'",
}

// recreate drops database target, makes it empty with charset and collation (the
// server's defaults when empty) and makes it the session's current one.
func recreate(ctx context.Context, s session, target, charset, collation string) error {
	create := "CREATE DATABASE " + Quote(target)
	if charset != "" {
		create += " CHARACTER SET " + charset + " COLLATE " + collation
	}
	return execAll(ctx, s, "DROP DATABASE IF EXISTS "+Quote(target), create, "USE "+Quote(target))
}

// execer is what writes statements: a session, or a snapshot file.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// sink is where a consistent read puts what it reads: a database on the server for Copy,
// a snapshot file for Snapshot. Rows arrive through ExecContext as the statements that
// write them, in order, right after their table.
type sink interface {
	execer
	// createTable creates table from stmt, the definition as the server printed it. why is
	// not empty when the table was left out instead; dropped names the databases its
	// foreign keys into were removed.
	createTable(ctx context.Context, table, stmt string) (why string, dropped []string, err error)
	createViews(ctx context.Context, views []viewDef) ([]string, error)
	// createTrigger creates trigger name; note says why it was left out.
	createTrigger(ctx context.Context, name, sqlMode, stmt string) (note string, err error)
}

// databaseSink writes into database target on the server through one session.
type databaseSink struct {
	session
	target string
	login  Login
}

func (d *databaseSink) createTable(ctx context.Context, table, stmt string) (string, []string, error) {
	if _, err := d.ExecContext(ctx, stmt); err != nil {
		return "", nil, err
	}
	why, err := createdOutside(ctx, d, d.target, table)
	if err != nil {
		return "", nil, err
	}
	if why != "" {
		_, err := d.ExecContext(ctx, "DROP TABLE "+Quote(table))
		return why, nil, err
	}
	left, err := foreignKeysOut(ctx, d, d.target, table)
	if err != nil {
		return "", nil, err
	}
	var dropped []string
	for _, fk := range left {
		if _, err := d.ExecContext(ctx, "ALTER TABLE "+Quote(table)+" DROP FOREIGN KEY "+Quote(fk.name)); err != nil {
			return "", nil, err
		}
		dropped = append(dropped, fk.db)
	}
	return "", dropped, nil
}

func (d *databaseSink) createViews(ctx context.Context, views []viewDef) ([]string, error) {
	return createViews(ctx, d, views, d.login), nil
}

func (d *databaseSink) createTrigger(ctx context.Context, name, sqlMode, stmt string) (string, error) {
	if err := execAll(ctx, d, "SET SESSION sql_mode = "+quoteString(sqlMode)); err != nil {
		return "", err
	}
	var note string
	if _, err := d.ExecContext(ctx, stmt); err != nil {
		note = fmt.Sprintf("trigger %s left out: %s; recreate it in post-add", name, scrub(err, d.login))
	}
	return note, execAll(ctx, d, "SET SESSION sql_mode = 'NO_AUTO_VALUE_ON_ZERO'")
}

// consistentRead reads database source as it was at one moment and hands what it reads to
// s: tables with their rows, then views, then triggers. except is left out of the
// databases a view or trigger must not name, as the copy's own. On success out.Bytes is
// the size of what was read, as Tables counts it.
func consistentRead(ctx context.Context, db *sql.DB, source, except string, s sink, w *writer, o CopyOptions, out *Copied, step *string) error {
	read, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer read.Close()
	if err := execAll(ctx, read,
		"SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ",
		"SET SESSION TRANSACTION READ ONLY"); err != nil {
		return err
	}
	if err := readSession(ctx, read, source); err != nil {
		return err
	}
	listed, _, err := listTables(ctx, read, source)
	if err != nil {
		return err
	}
	var tables []Table
	for _, t := range listed {
		if t.Copyable() {
			tables = append(tables, t)
			continue
		}
		out.Notes = append(out.Notes, fmt.Sprintf("table %s left out: its %s engine keeps its rows outside this database, so a copy would read or write them there; recreate it in post-add", t.Name, t.Engine))
	}
	columns, err := listColumns(ctx, read, source)
	if err != nil {
		return err
	}
	triggers, err := listTriggers(ctx, read, source)
	if err != nil {
		return err
	}
	others, err := databases(ctx, read, except)
	if err != nil {
		return err
	}
	if note, err := uncopied(ctx, read, source); err != nil {
		return err
	} else if note != "" {
		out.Notes = append(out.Notes, note)
	}

	// A table without a snapshot is read through the session that holds it. MyISAM queues
	// a new reader behind a waiting writer, so another session reading it would wait for
	// a write that waits for the lock this copy holds.
	out.Held = Held(tables)
	isHeld := func(t Table) bool { return slices.Contains(out.Held, t.Name) }
	var lock *sql.Conn
	var heldAt time.Time
	release := func() error {
		if lock == nil {
			return nil
		}
		_, err := lock.ExecContext(ctx, "UNLOCK TABLES")
		lock.Close()
		lock = nil
		out.HeldFor = time.Since(heldAt)
		return err
	}
	defer release()
	if len(out.Held) > 0 {
		if lock, err = db.Conn(ctx); err != nil {
			return err
		}
		if err := readSession(ctx, lock, source); err != nil {
			return err
		}
		var names []string
		for _, t := range out.Held {
			names = append(names, Quote(t)+" READ")
		}
		if o.Holding != nil {
			if err := o.Holding(out.Held); err != nil {
				return err
			}
		}
		heldAt = time.Now()
		saved := *step
		*step = "holding writes to " + strings.Join(out.Held, ", ") + " in database " + source
		if _, err := lock.ExecContext(ctx, "LOCK TABLES "+strings.Join(names, ", ")); err != nil {
			return err
		}
		*step = saved
	}
	// Taken while every held table is held, so they and the snapshot are one moment.
	// Rolling back to the savepoint after each table ends the snapshot's metadata lock on
	// it, as mysqldump does, so a migration on main never waits for the whole copy, and
	// everything queued behind the migration never waits either.
	if err := execAll(ctx, read, "START TRANSACTION WITH CONSISTENT SNAPSHOT", "SAVEPOINT berth"); err != nil {
		return err
	}
	defer read.ExecContext(ctx, "ROLLBACK")
	unlockRead := func() error {
		_, err := read.ExecContext(ctx, "ROLLBACK TO SAVEPOINT berth")
		return err
	}

	var done int64
	copyTable := func(from session, t Table) error {
		original, err := showCreate(ctx, from, "SHOW CREATE TABLE "+Quote(t.Name), 1)
		if err != nil {
			return err
		}
		stmt := original
		// The definition is read when the table is reached, and main may have changed the
		// table since it was listed, so what decides is the table as created, checked
		// below whatever its definition's text looked like.
		stmt, dropped, err := withoutForeignDatabases(stmt)
		if err != nil {
			stmt, dropped = original, nil
		}
		why, more, err := s.createTable(ctx, t.Name, stmt)
		if err != nil {
			return err
		}
		if why != "" {
			out.Notes = append(out.Notes, fmt.Sprintf("table %s left out: %s; recreate it in post-add", t.Name, why))
			done += t.Bytes
			return nil
		}
		for _, db := range append(dropped, more...) {
			out.Notes = append(out.Notes, fmt.Sprintf("table %s keeps no foreign key into database %s, so the copy never checks against data outside it", t.Name, db))
		}
		cols, err := columnsFor(columns, t.Name)
		if err != nil {
			return err
		}
		err = w.copyRows(ctx, from, t.Name, cols, func(rows int64) {
			if o.Progress != nil && t.Rows > 0 {
				o.Progress(done + int64(float64(t.Bytes)*min(float64(rows)/float64(t.Rows), 1)))
			}
		})
		done += t.Bytes
		return err
	}
	var views []Table
	for _, t := range tables {
		if t.View {
			views = append(views, t)
		} else if isHeld(t) {
			if err := copyTable(lock, t); err != nil {
				return err
			}
		}
	}
	if err := release(); err != nil {
		return err
	}
	for _, t := range tables {
		if t.View || isHeld(t) {
			continue
		}
		if err := copyTable(read, t); err != nil {
			return err
		}
		if err := unlockRead(); err != nil {
			return err
		}
	}
	if o.Progress != nil {
		o.Progress(done)
	}
	defs, notes, err := readViews(ctx, read, views, others)
	out.Notes = append(out.Notes, notes...)
	if err != nil {
		return err
	}
	notes, err = s.createViews(ctx, defs)
	out.Notes = append(out.Notes, notes...)
	if err != nil {
		return err
	}
	if err := unlockRead(); err != nil {
		return err
	}
	for _, name := range triggers {
		row, err := showRow(ctx, read, "SHOW CREATE TRIGGER "+Quote(name))
		if err != nil {
			return err
		}
		if err := unlockRead(); err != nil {
			return err
		}
		if len(row) < 3 {
			return fmt.Errorf("SHOW CREATE TRIGGER %s gave %d columns", name, len(row))
		}
		stmt := withoutDefiner(row[2])
		if why := reachesOut(stmt, row[1], others); why != "" {
			out.Notes = append(out.Notes, fmt.Sprintf("trigger %s left out: %s; recreate it in post-add", name, why))
			continue
		}
		note, err := s.createTrigger(ctx, name, row[1], stmt)
		if note != "" {
			out.Notes = append(out.Notes, note)
		}
		if err != nil {
			return err
		}
	}
	if o.Done != nil {
		o.Done()
	}
	if _, err := read.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	out.Bytes = done
	return nil
}

// readSession readies a session that reads db: values come back as the bytes the server
// stores, in each column's own character set, and are written back with that set named,
// so no conversion can change them.
func readSession(ctx context.Context, s session, db string) error {
	return execAll(ctx, s,
		"SET SESSION time_zone = '+00:00'",
		"SET SESSION sql_mode = ''",
		"SET SESSION net_write_timeout = 3600",
		"SET SESSION character_set_results = NULL",
		"USE "+Quote(db))
}

func execAll(ctx context.Context, s session, stmts ...string) error {
	for _, q := range stmts {
		if _, err := s.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// column is one stored column of a table, the only kind a copy writes: generated
// columns are computed again by the target.
type column struct {
	name, kind, charset string
}

func listColumns(ctx context.Context, s session, db string) (map[string][]column, error) {
	rows, err := s.QueryContext(ctx, "SELECT table_name, column_name, data_type, COALESCE(character_set_name, ''), extra FROM information_schema.columns WHERE table_schema = ? ORDER BY table_name, ordinal_position", db)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]column{}
	for rows.Next() {
		var table, extra string
		var c column
		if err := rows.Scan(&table, &c.name, &c.kind, &c.charset, &extra); err != nil {
			return nil, err
		}
		extra = strings.ToUpper(extra)
		if strings.Contains(extra, "GENERATED") && !strings.Contains(extra, "DEFAULT_GENERATED") {
			continue
		}
		c.kind = strings.ToLower(c.kind)
		out[table] = append(out[table], c)
	}
	return out, rows.Err()
}

func listTriggers(ctx context.Context, s session, db string) ([]string, error) {
	rows, err := s.QueryContext(ctx, "SELECT trigger_name FROM information_schema.triggers WHERE trigger_schema = ? ORDER BY event_object_table, event_manipulation, action_timing, action_order", db)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

var (
	numeric  = map[string]bool{"tinyint": true, "smallint": true, "mediumint": true, "int": true, "integer": true, "bigint": true, "decimal": true, "numeric": true, "float": true, "double": true, "real": true, "year": true}
	temporal = map[string]bool{"date": true, "datetime": true, "timestamp": true, "time": true}
)

// textCharset is the character set c's values are text in, "" for bytes.
func (c column) textCharset() string {
	if c.kind == "json" {
		return "utf8mb4"
	}
	if c.charset == "binary" {
		return ""
	}
	return c.charset
}

// literal writes v, as the server returned it for c, as a value the server reads back
// to the same stored bytes.
func (c column) literal(b *bytes.Buffer, v []byte) {
	switch {
	case v == nil:
		b.WriteString("NULL")
	case numeric[c.kind]:
		b.Write(v)
	case temporal[c.kind]:
		b.WriteByte('\'')
		b.Write(v)
		b.WriteByte('\'')
	default:
		if charset := c.textCharset(); charset != "" {
			b.WriteString("_" + charset + " ")
		}
		b.WriteString("X'")
		b.WriteString(hex.EncodeToString(v))
		b.WriteByte('\'')
	}
}

// writer writes rows into the target through one session, within both the server's
// max_allowed_packet and the driver's own limit on what it sends.
type writer struct {
	s execer
	// batch is the size a multi-row INSERT grows to before it is sent.
	batch int
	// row is the most one row may take written out in full; past it, its large values go
	// to the server in chunks of chunk bytes first.
	row, chunk int
}

// driverPacket is the most the driver sends in one statement, whatever the server allows.
const driverPacket = 64 << 20

func newWriter(s execer, packet int64) *writer {
	p := int(min(packet, driverPacket))
	return &writer{s: s, batch: min(1<<20, p/4), row: p / 2, chunk: min(16<<20, p/4)}
}

// inlineMax is the largest value a row too big to write in full still writes inline.
const inlineMax = 64 << 10

// copyRows reads every row of table from read and writes it to the same table in the
// target, in statements of about w.batch bytes. copied is told how many rows are in so
// far.
func (w *writer) copyRows(ctx context.Context, read session, table string, cols []column, copied func(rows int64)) error {
	names := make([]string, len(cols))
	exprs := make([]string, len(cols))
	for i, c := range cols {
		names[i] = Quote(c.name)
		exprs[i] = names[i]
		if c.kind == "float" {
			// The server prints a FLOAT to 6 significant digits, which loses bits a double,
			// written back into the FLOAT, keeps.
			exprs[i] = "(" + names[i] + " + 0e0)"
		}
	}
	list := strings.Join(names, ", ")
	rows, err := read.QueryContext(ctx, "SELECT "+strings.Join(exprs, ", ")+" FROM "+Quote(table))
	if err != nil {
		return err
	}
	defer rows.Close()
	vals := make([]sql.RawBytes, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	head := "INSERT INTO " + Quote(table) + " (" + list + ") VALUES "
	var buf bytes.Buffer
	var n, pending int64
	flush := func() error {
		if pending == 0 {
			return nil
		}
		if _, err := w.s.ExecContext(ctx, buf.String()); err != nil {
			return err
		}
		buf.Reset()
		n, pending = n+pending, 0
		copied(n)
		return nil
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		size := 0
		for _, v := range vals {
			size += 2*len(v) + 32
		}
		if size > w.row {
			if err := flush(); err != nil {
				return err
			}
			if err := w.largeRow(ctx, head, cols, vals); err != nil {
				return err
			}
			n++
			copied(n)
			continue
		}
		if pending == 0 {
			buf.WriteString(head)
		} else {
			buf.WriteByte(',')
		}
		buf.WriteByte('(')
		for i, v := range vals {
			if i > 0 {
				buf.WriteByte(',')
			}
			cols[i].literal(&buf, v)
		}
		buf.WriteByte(')')
		pending++
		if buf.Len() >= w.batch {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return flush()
}

// largeRow writes one row too big to send whole: each large value is built up in a
// session variable a chunk at a time, as raw bytes, then read as the column's character
// set in the INSERT, so a character split between chunks is whole again.
func (w *writer) largeRow(ctx context.Context, head string, cols []column, vals []sql.RawBytes) error {
	var b bytes.Buffer
	b.WriteString(head)
	b.WriteByte('(')
	var used []string
	for i, v := range vals {
		if i > 0 {
			b.WriteByte(',')
		}
		if len(v) <= inlineMax {
			cols[i].literal(&b, v)
			continue
		}
		name := fmt.Sprintf("@berth_value_%d", i)
		used = append(used, name)
		for start := 0; start < len(v); start += w.chunk {
			part := "X'" + hex.EncodeToString(v[start:min(start+w.chunk, len(v))]) + "'"
			stmt := "SET " + name + " = " + part
			if start > 0 {
				stmt = "SET " + name + " = CONCAT(" + name + ", " + part + ")"
			}
			if _, err := w.s.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		if charset := cols[i].textCharset(); charset != "" {
			b.WriteString("CONVERT(" + name + " USING " + charset + ")")
		} else {
			b.WriteString(name)
		}
	}
	b.WriteByte(')')
	_, err := w.s.ExecContext(ctx, b.String())
	for _, name := range used {
		if _, freeErr := w.s.ExecContext(ctx, "SET "+name+" = NULL"); err == nil {
			err = freeErr
		}
	}
	return err
}

// columnsFor is table's columns from columns. The server may report a table's name in
// another case in its column list than in its table list, as with lower_case_table_names
// set to 2, so a name found only in another case is used when it is the only one; a
// table with no columns at all is refused, since copying it would copy no rows.
func columnsFor(columns map[string][]column, table string) ([]column, error) {
	if cols, ok := columns[table]; ok && len(cols) > 0 {
		return cols, nil
	}
	var found []column
	matches := 0
	for name, cols := range columns {
		if strings.EqualFold(name, table) {
			found, matches = cols, matches+1
		}
	}
	if matches == 1 && len(found) > 0 {
		return found, nil
	}
	return nil, fmt.Errorf("the server lists no columns of table %s that berth could copy, so berth stopped rather than copy it empty", table)
}

// viewDef is a view's name and CREATE statement.
type viewDef struct{ name, stmt string }

// readViews reads what creates views in the copy. A view that names another database on
// the server is left out and named in notes.
func readViews(ctx context.Context, read session, views []Table, others []string) (defs []viewDef, notes []string, err error) {
	for _, v := range views {
		stmt, err := showCreate(ctx, read, "SHOW CREATE VIEW "+Quote(v.Name), 1)
		if err != nil {
			return defs, notes, err
		}
		stmt = withoutDefiner(stmt)
		if why := reachesOut(stmt, "", others); why != "" {
			notes = append(notes, fmt.Sprintf("view %s left out: %s; recreate it in post-add", v.Name, why))
			continue
		}
		defs = append(defs, viewDef{v.Name, stmt})
	}
	return defs, notes, nil
}

// createViews creates views through s, each as often as needed until every view it
// selects from exists, since the server lists them by name rather than by dependency. A
// view that still can't be created, such as one calling a routine the copy leaves out,
// is left out and named in the notes returned.
func createViews(ctx context.Context, s execer, views []viewDef, login Login) (notes []string) {
	pending := views
	for len(pending) > 0 {
		var left []viewDef
		failed := map[string]error{}
		for _, v := range pending {
			if _, err := s.ExecContext(ctx, v.stmt); err != nil {
				left = append(left, v)
				failed[v.name] = err
			}
		}
		if len(left) == len(pending) {
			for _, v := range left {
				notes = append(notes, fmt.Sprintf("view %s left out: %s; recreate it in post-add", v.name, scrub(failed[v.name], login)))
			}
			return notes
		}
		pending = left
	}
	return notes
}

// reachesOut says why stmt, read under sqlMode, might reach a database other than the
// copy's own: it names one of others, or it can't be read through to be sure it names
// none. It is "" when stmt stays inside the copy. A view or trigger in a database can
// name any database, so a name that only looks like one counts too: a false match only
// leaves an object out, while a missed one would point the copy at main.
func reachesOut(stmt, sqlMode string, others []string) string {
	db, err := referencedDatabase(stmt, sqlMode, others)
	switch {
	case err != nil:
		return fmt.Sprintf("berth can't read it through to check it names no other database (%v)", err)
	case db != "":
		return fmt.Sprintf("it names database %s, so in the copy it would reach data outside it", db)
	}
	return ""
}

// createdOutside says why table, just created in db, might keep rows or files outside
// db, or "" when it can't: its engine must be one that keeps rows locally, and its
// definition, as the server now prints it, must set nothing that places them elsewhere.
func createdOutside(ctx context.Context, s session, db, table string) (string, error) {
	var engine string
	err := s.QueryRowContext(ctx, "SELECT COALESCE(engine, '') FROM information_schema.tables WHERE table_schema = ? AND table_name = ?", db, table).Scan(&engine)
	if errors.Is(err, sql.ErrNoRows) {
		return "berth couldn't find it as created to check where it keeps its rows", nil
	}
	if err != nil {
		return "", err
	}
	if !localEngines[strings.ToLower(engine)] {
		return fmt.Sprintf("main changed it during the copy to the %s engine, which keeps its rows outside this database", engine), nil
	}
	stmt, err := showCreate(ctx, s, "SHOW CREATE TABLE "+Quote(table), 1)
	if err != nil {
		return "", err
	}
	return definitionOutside(stmt), nil
}

// definitionOutside says why a table with definition stmt might keep rows or files
// outside its database, or "" when it can't, from the definition's own text.
func definitionOutside(stmt string) string {
	option, err := outsideOption(stmt)
	switch {
	case err != nil:
		return fmt.Sprintf("berth can't read its definition through to check where it keeps its rows (%v)", err)
	case option != "":
		return fmt.Sprintf("its definition sets %s, which can keep its rows or files outside this database", option)
	}
	return ""
}

type foreignKey struct{ name, db string }

// foreignKeysOut lists table's foreign keys in db that reference another database.
func foreignKeysOut(ctx context.Context, s session, db, table string) ([]foreignKey, error) {
	rows, err := s.QueryContext(ctx, "SELECT constraint_name, unique_constraint_schema FROM information_schema.referential_constraints WHERE constraint_schema = ? AND table_name = ? AND unique_constraint_schema <> constraint_schema", db, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []foreignKey
	for rows.Next() {
		var fk foreignKey
		if err := rows.Scan(&fk.name, &fk.db); err != nil {
			return nil, err
		}
		out = append(out, fk)
	}
	return out, rows.Err()
}

// databases lists every database on the server apart from MySQL's own and except.
func databases(ctx context.Context, s session, except string) ([]string, error) {
	rows, err := s.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if !system[strings.ToLower(name)] && name != except {
			out = append(out, name)
		}
	}
	return out, rows.Err()
}

// uncopied says what of db's a copy never takes: its stored routines and events.
func uncopied(ctx context.Context, s session, db string) (string, error) {
	var routines, events int
	if err := s.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.routines WHERE routine_schema = ?", db).Scan(&routines); err != nil {
		return "", err
	}
	if err := s.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.events WHERE event_schema = ?", db).Scan(&events); err != nil {
		return "", err
	}
	var what []string
	if routines > 0 {
		what = append(what, plural(routines, "stored routine"))
	}
	if events > 0 {
		what = append(what, plural(events, "event"))
	}
	if len(what) == 0 {
		return "", nil
	}
	return fmt.Sprintf("main's %s has %s, which berth doesn't copy; recreate them in post-add if the app needs them", db, strings.Join(what, " and ")), nil
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

func showCreate(ctx context.Context, s session, query string, col int) (string, error) {
	row, err := showRow(ctx, s, query)
	if err != nil {
		return "", err
	}
	if col >= len(row) {
		return "", fmt.Errorf("%s gave %d columns", query, len(row))
	}
	return row[col], nil
}

// showRow is the first row query returns, every column as text.
func showRow(ctx context.Context, s session, query string) ([]string, error) {
	rows, err := s.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%s returned nothing", query)
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	out := make([]string, len(cols))
	for i, v := range vals {
		out[i] = v.String
	}
	return out, nil
}

var definer = regexp.MustCompile("DEFINER=`(?:[^`]|``)*`@`(?:[^`]|``)*` ")

// withoutDefiner drops the DEFINER clause from a CREATE statement the server printed,
// so the copy is defined by the user berth connects as, which can always create it,
// rather than by main's definer, which only an administrator may name.
func withoutDefiner(stmt string) string {
	loc := definer.FindStringIndex(stmt)
	if loc == nil {
		return stmt
	}
	return stmt[:loc[0]] + stmt[loc[1]:]
}

func quoteString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}
