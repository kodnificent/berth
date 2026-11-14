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

// How a template copy's files were made, as add reports it.
const (
	FullCopy    = "full copy"
	CopyOnWrite = "copy-on-write"
	// FileClone is a clone the server was configured to make with copy_file_range, which
	// shares blocks only on some filesystems, on a server whose filesystem berth can't see.
	FileClone = "file clone, copy-on-write only where the server's filesystem has it"
)

const (
	inUse         = "55006"
	internalError = "XX000"
)

// Session is another session on a database, as far as the login may see it.
type Session struct {
	PID         int
	Role        string
	Application string
	// Address is the client's address, "local" for a Unix socket and empty for a server
	// process with no client.
	Address string
	Kind    string
	// Visible says the login may see the session's details, Own that it is the login's
	// own role, and CanEnd that the login may end it.
	Visible bool
	Own     bool
	CanEnd  bool
}

func (s Session) String() string {
	app := s.Application
	if app == "" {
		app = "an unnamed application"
	}
	switch {
	case !s.Visible:
		return fmt.Sprintf("%s under role %q, whose address the login can't see", app, s.Role)
	case s.Kind != "client backend":
		return fmt.Sprintf("a %s", s.Kind)
	}
	where := "from " + s.Address
	switch s.Address {
	case "local":
		where = "over a local socket"
	case "":
		where = "with no client address"
	}
	if s.Own {
		return fmt.Sprintf("%s %s", app, where)
	}
	return fmt.Sprintf("%s %s under role %q", app, where, s.Role)
}

func describeSessions(sessions []Session) string {
	out := make([]string, len(sessions))
	for i, s := range sessions {
		out[i] = s.String()
	}
	return strings.Join(out, "; ")
}

// Sessions lists every session on database db but this one. Autovacuum is left out, since
// CREATE DATABASE stops it itself; a login that is not a superuser sees its workers with
// no role and no type, and every other process that can be on a database has a role, so
// a row without one is taken for autovacuum.
func (c *Conn) Sessions(db string) ([]Session, error) {
	ctx, cancel := statementContext()
	defer cancel()
	rows, err := c.conn.Query(ctx, `SELECT a.pid, COALESCE(a.usename::text, ''), COALESCE(a.application_name, ''),
  CASE WHEN a.client_port = -1 THEN 'local' ELSE COALESCE(host(a.client_addr), '') END,
  COALESCE(a.backend_type, ''),
  a.usesysid IS NOT NULL AND (pg_has_role(a.usesysid, 'USAGE') OR pg_has_role('pg_read_all_stats', 'USAGE')),
  a.usesysid IS NOT NULL AND a.usename = current_user,
  COALESCE((SELECT rolsuper FROM pg_roles WHERE rolname = current_user), false)
    OR (a.usesysid IS NOT NULL AND NOT COALESCE((SELECT rolsuper FROM pg_roles r WHERE r.oid = a.usesysid), true)
      AND (pg_has_role(a.usesysid, 'USAGE') OR pg_has_role('pg_signal_backend', 'USAGE')))
FROM pg_stat_activity a
WHERE a.datname = $1 AND a.pid <> pg_backend_pid() AND a.usesysid IS NOT NULL AND a.backend_type IS DISTINCT FROM 'autovacuum worker'
ORDER BY a.pid`, db)
	if err != nil {
		return nil, c.fail("listing the sessions on database "+db, err)
	}
	sessions, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Session, error) {
		var s Session
		err := r.Scan(&s.PID, &s.Role, &s.Application, &s.Address, &s.Kind, &s.Visible, &s.Own, &s.CanEnd)
		return s, err
	})
	if err != nil {
		return nil, c.fail("listing the sessions on database "+db, err)
	}
	return sessions, nil
}

// Backend names one server session: pids are reused, so its start time goes with it.
type Backend struct {
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
}

// Backend is this connection's own session on the server.
func (c *Conn) Backend() (Backend, error) {
	ctx, cancel := statementContext()
	defer cancel()
	var b Backend
	if err := c.conn.QueryRow(ctx, "SELECT pid, backend_start FROM pg_stat_activity WHERE pid = pg_backend_pid()").Scan(&b.PID, &b.Started); err != nil {
		return Backend{}, c.fail("reading berth's own session", err)
	}
	return b, nil
}

// EndBackend ends session b if it is still there and waits up to wait for it to go. gone
// says it has.
func (c *Conn) EndBackend(b Backend, wait time.Duration) (gone bool, err error) {
	ctx, cancel := statementContext()
	defer cancel()
	alive := func() (bool, error) {
		var n int
		err := c.conn.QueryRow(ctx, "SELECT COUNT(*) FROM pg_stat_activity WHERE pid = $1 AND backend_start = $2", b.PID, b.Started).Scan(&n)
		return n > 0, err
	}
	if _, err := c.conn.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE pid = $1 AND backend_start = $2", b.PID, b.Started); err != nil {
		return false, c.fail("ending an earlier copy's session", err)
	}
	deadline := time.Now().Add(wait)
	for {
		on, err := alive()
		if err != nil {
			return false, c.fail("checking an earlier copy's session", err)
		}
		if !on {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Exists reports whether the server has database name.
func (c *Conn) Exists(name string) (bool, error) {
	ctx, cancel := statementContext()
	defer cancel()
	var ok bool
	if err := c.conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&ok); err != nil {
		return false, c.fail("looking for database "+name, err)
	}
	return ok, nil
}

// copyPlan is how a CREATE DATABASE … TEMPLATE copies the files on one server.
type copyPlan struct {
	// fileCopy adds STRATEGY FILE_COPY, which Postgres 15 brought; before it, file copy
	// was the only way.
	fileCopy bool
	// method is the file_copy_method berth sets for the session, empty to keep the
	// server's own.
	method string
	// report is how add reports the copy, empty when only the disk can tell: then dir is
	// the server's data directory, measured either side of the copy.
	report string
	dir    string
}

// Filesystems of a data directory berth can see.
const (
	btrfs   = "btrfs"
	xfs     = "xfs"
	otherFS = "other"
)

// planCopy chooses the copy for a server of version, built for platform. clonable says
// file_copy_method takes clone there, configured is its value now, and local the
// filesystem of the server's data directory, empty when berth can't see it. On macOS a
// clone the filesystem can't make fails rather than copying, and btrfs always shares
// blocks, so those report copy-on-write. Elsewhere copy_file_range copies in full where
// it can't share them, as on XFS made without reflink, so berth asks for a clone only on
// XFS, where it can measure what happened, and keeps the server's own method otherwise.
func planCopy(version int, platform string, clonable bool, configured, local string) copyPlan {
	switch {
	case version < 150000:
		return copyPlan{report: FullCopy}
	case version < 180000 || !clonable:
		return copyPlan{fileCopy: true, report: FullCopy}
	case strings.Contains(platform, "darwin"), local == btrfs:
		return copyPlan{fileCopy: true, method: "clone", report: CopyOnWrite}
	case local == xfs:
		return copyPlan{fileCopy: true, method: "clone"}
	case local == "" && configured == "clone":
		return copyPlan{fileCopy: true, report: FileClone}
	}
	return copyPlan{fileCopy: true, report: FullCopy}
}

// smallestMeasured is the least a copy must hold for the disk it took to tell a clone
// from a full copy, against what else the server writes meanwhile.
const smallestMeasured = 32 << 20

// measuredMethod is how a clone asked for on a filesystem that may not share blocks went:
// size is the copy's, used the free disk it took.
func measuredMethod(size, used int64, measured bool) string {
	switch {
	case !measured || size < smallestMeasured:
		return FileClone
	case used >= size/2:
		return FullCopy
	}
	return CopyOnWrite
}

func (p copyPlan) statement(source, target string) string {
	q := "CREATE DATABASE " + Quote(target) + " TEMPLATE " + Quote(source)
	if p.fileCopy {
		q += " STRATEGY FILE_COPY"
	}
	return q
}

// plan reads what the server allows and chooses the copy.
func (c *Conn) plan() (copyPlan, error) {
	if c.version < 180000 {
		return planCopy(c.version, c.platform, false, "", ""), nil
	}
	ctx, cancel := statementContext()
	defer cancel()
	var configured string
	var clonable bool
	err := c.conn.QueryRow(ctx, "SELECT setting, 'clone' = ANY(enumvals) AND context = 'user' FROM pg_settings WHERE name = 'file_copy_method'").Scan(&configured, &clonable)
	if errors.Is(err, pgx.ErrNoRows) {
		return planCopy(c.version, c.platform, false, "", ""), nil
	}
	if err != nil {
		return copyPlan{}, c.fail("reading file_copy_method", err)
	}
	var dir, local string
	if c.server.Loopback() {
		var pid int
		if err := c.conn.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err == nil {
			dir, local, _ = localDataDir(pid, c.server.Port)
		}
	}
	p := planCopy(c.version, c.platform, clonable, configured, local)
	p.dir = dir
	return p, nil
}

// CopyOptions are the hooks a template copy calls as it goes.
type CopyOptions struct {
	// Started is called with the session that runs the copy, before the server begins it.
	Started func(Backend)
	// Disconnect ends the sessions on the source the login may end before each try, and
	// tries again should more connect before the copy takes hold of the source.
	Disconnect bool
	// Retry is the command that copies again, for messages.
	Retry string
}

// CopyResult is how a template copy went.
type CopyResult struct {
	Method string
	// Fallback is why the server copied in full after a clone failed, empty otherwise.
	Fallback string
	// Unsettled says the server may still be running the copy, or have finished it, after
	// an error that came from the connection rather than the server.
	Unsettled bool
}

// refusal kinds: sessions berth won't end, sessions it may not end, and sessions that kept
// coming back as it ended them.
const (
	quiet = iota
	unendable
	reconnected
)

// sessionsRefusal says why a copy of source was refused over sessions on it. ended says
// berth had already ended some of main's sessions there on an earlier try.
func sessionsRefusal(source string, sessions []Session, kind int, ended bool, retry string) string {
	list := describeSessions(sessions)
	switch {
	case kind == reconnected:
		return fmt.Sprintf("main's database %s had sessions open again each time berth ended them, so it was not copied: %s; run %s when main's app is quieter", source, list, retry)
	case kind == unendable && ended:
		return fmt.Sprintf("main's database %s has sessions the login has no right to end: %s; berth had already ended main's other sessions there, then stopped and copied nothing; close them and run %s", source, list, retry)
	case kind == unendable:
		return fmt.Sprintf("main's database %s has sessions the login has no right to end: %s; berth ended none of main's sessions and copied nothing; close them and run %s", source, list, retry)
	}
	return fmt.Sprintf("main's database %s has other sessions open: %s; Postgres copies a database only while nothing else is connected to it, so berth copied nothing and left them as they were; close them and run %s, or set disconnect = true under [state.postgres] to have berth end them first", source, list, retry)
}

// CopyFromTemplate makes database target as a copy of source, made by the server. target
// must not exist. A copy holds back new connections to source until it ends.
func (c *Conn) CopyFromTemplate(source, target string, opts CopyOptions) (CopyResult, error) {
	if err := checkName(source); err != nil {
		return CopyResult{}, err
	}
	if err := checkName(target); err != nil {
		return CopyResult{}, err
	}
	p, err := c.plan()
	if err != nil {
		return CopyResult{}, err
	}
	ctx := context.Background()
	// A copy of a large database legitimately outlasts a statement timeout set for the role.
	if _, err := c.conn.Exec(ctx, "SET statement_timeout = 0"); err != nil {
		return CopyResult{}, c.fail("preparing the copy", err)
	}
	if c.version >= 140000 {
		// The server then abandons the copy soon after berth's process dies, rather than
		// finishing it and holding main's new connections back until it does.
		_, _ = c.conn.Exec(ctx, "SET client_connection_check_interval = '1s'")
	}
	first := quiet
	if opts.Disconnect {
		first = unendable
	}
	if err := c.refuseSessions(source, first, opts.Retry); err != nil {
		return CopyResult{}, err
	}
	b, err := c.Backend()
	if err != nil {
		return CopyResult{}, err
	}
	opts.Started(b)
	res := CopyResult{Method: p.report}
	tries := 1
	if opts.Disconnect {
		tries = 3
	}
	ended := false
	for try := 1; ; try++ {
		if opts.Disconnect {
			n, err := c.endSessions(source, ended, opts.Retry)
			if err != nil {
				return res, err
			}
			ended = ended || n > 0
		}
		before, measured := freeBytes(p.dir)
		err = c.create(ctx, p, source, target)
		var pg *pgconn.PgError
		switch {
		case err == nil:
			if p.report == "" {
				after, ok := freeBytes(p.dir)
				size, sizeErr := c.Size(target)
				res.Method = measuredMethod(size, before-after, measured && ok && sizeErr == nil)
			}
			return res, nil
		case !errors.As(err, &pg):
			res.Unsettled = true
			return res, c.fail("copying database "+source+" into "+target, err)
		case pg.Code == inUse && try < tries:
			continue
		case pg.Code == inUse:
			kind := quiet
			if opts.Disconnect {
				kind = reconnected
			}
			if refused := c.refuseSessions(source, kind, opts.Retry); refused != nil {
				return res, refused
			}
		case p.method == "clone" && (pg.Code == internalError || strings.HasPrefix(pg.Code, "58")):
			p.method, res.Method = "copy", FullCopy
			res.Fallback = fmt.Sprintf("the server could not clone the files, so it copied them in full (%s)", hostdb.Scrub(err, c.login))
			if err = c.create(ctx, p, source, target); err == nil {
				return res, nil
			}
			if !errors.As(err, &pg) {
				res.Unsettled = true
			}
		}
		return res, c.fail("copying database "+source+" into "+target, err)
	}
}

func (c *Conn) create(ctx context.Context, p copyPlan, source, target string) error {
	if p.method != "" {
		if _, err := c.conn.Exec(ctx, "SET file_copy_method = "+p.method); err != nil {
			return err
		}
	}
	_, err := c.conn.Exec(ctx, p.statement(source, target))
	return err
}

// refuseSessions fails when other sessions are on source: any at all, or with unendable
// only those the login may not end.
func (c *Conn) refuseSessions(source string, kind int, retry string) error {
	sessions, err := c.Sessions(source)
	if err != nil {
		return err
	}
	var refused []Session
	for _, s := range sessions {
		if kind != unendable || !s.CanEnd {
			refused = append(refused, s)
		}
	}
	if len(refused) == 0 {
		return nil
	}
	return c.refusal(sessionsRefusal(source, refused, kind, false, retry))
}

// refusal is msg without the login, which an application name could happen to hold.
func (c *Conn) refusal(msg string) error {
	return errors.New(hostdb.Scrub(errors.New(msg), c.login))
}

// endSessions ends every session on source, each one the login may end, and refuses
// before ending any when one is not. ended says earlier tries ended some, for the
// refusal. n is how many it ended.
func (c *Conn) endSessions(source string, ended bool, retry string) (n int, err error) {
	sessions, err := c.Sessions(source)
	if err != nil {
		return 0, err
	}
	var pids []int32
	var refused []Session
	for _, s := range sessions {
		if s.CanEnd {
			pids = append(pids, int32(s.PID))
		} else {
			refused = append(refused, s)
		}
	}
	if len(refused) > 0 {
		return 0, c.refusal(sessionsRefusal(source, refused, unendable, ended, retry))
	}
	if len(pids) == 0 {
		return 0, nil
	}
	ctx, cancel := statementContext()
	defer cancel()
	// The database goes with each pid, so one reused by a session elsewhere since the list
	// was read is never ended.
	if _, err := c.conn.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid = ANY($2::int[])", source, pids); err != nil {
		return 0, c.fail("ending main's sessions on database "+source, err)
	}
	return len(pids), nil
}
