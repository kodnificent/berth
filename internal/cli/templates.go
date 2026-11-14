package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/hostdb"
	"github.com/kodnificent/berth/internal/postgres"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/volume"
)

// endEarlierCopy bounds the wait for an earlier template copy's session to go once ended.
const endEarlierCopy = 30 * time.Second

// prepareTemplate measures a template copy's source for the disk warning and says, before
// the copy starts, what it does to main: new connections to the source wait for it, and
// with disconnect its sessions there end first. ok is false when main has no such
// database, so there is nothing to copy or announce.
func prepareTemplate(stderr io.Writer, cfg *config.Config, conn *postgres.Conn, e *volume.Entry) (size int64, ok bool) {
	exists, err := conn.Exists(e.Source)
	if err != nil {
		fmt.Fprintf(stderr, "berth: could not measure database %s before copying it: %v\n", e.Source, err)
	} else if !exists {
		fmt.Fprintf(stderr, "berth: main has no database %s yet, so the berth's %s starts empty\n", e.Source, e.Target)
		return 0, false
	}
	if host, table := cfg.HostDB(); host != nil && table == postgres.Engine.Table {
		e.Disconnect = host.Disconnects()
	}
	if e.Disconnect {
		fmt.Fprintf(stderr, "berth: berth will end every other session on main's database %s just before copying it, since [state.postgres] sets disconnect = true: their open transactions roll back, and main's app reconnects as it would after a server restart\n", e.Source)
	}
	fmt.Fprintf(stderr, "berth: new connections to main's database %s will pause while the server copies it, since Postgres holds them back until a template copy ends, and go through as soon as it does\n", e.Source)
	if err != nil {
		return 0, false
	}
	size, err = conn.Size(e.Source)
	return size, err == nil
}

// templateDatabase makes e's target as a copy of its source, made by the server itself.
// The session running the copy is saved on e before the copy starts, so whoever finds
// this process dead can end it and drop what it made.
func templateDatabase(server hostdb.Server, login hostdb.Login, e *volume.Entry, delay time.Duration, save func()) error {
	retry := "berth add"
	if e.Reset {
		retry = "berth reset"
	}
	conn, err := postgres.Open(server, login)
	if err != nil {
		return err
	}
	defer conn.Close()
	if e.CopySession != nil {
		gone, err := conn.EndBackend(backendOf(e.CopySession), endEarlierCopy)
		if err != nil {
			return err
		}
		if !gone {
			return fmt.Errorf("an earlier copy into %s is still running on the server and berth could not end it, so nothing was copied; run %s again once it has ended", e.Target, retry)
		}
		e.CopySession = nil
		save()
	}
	e.SourceMissing, e.Method, e.Notes, e.Bytes = false, "", nil, 0
	if err := conn.Drop(e.Target); err != nil {
		return err
	}
	exists, err := conn.Exists(e.Source)
	if err != nil {
		return err
	}
	if !exists {
		e.SourceMissing = true
		return conn.Create(e.Target)
	}
	out, err := conn.CopyFromTemplate(e.Source, e.Target, postgres.CopyOptions{
		Disconnect: e.Disconnect,
		Retry:      retry,
		Started: func(b postgres.Backend) {
			e.CopySession = &volume.Session{PID: b.PID, Started: b.Started}
			save()
		},
	})
	if !out.Unsettled {
		e.CopySession = nil
	}
	if err != nil {
		return err
	}
	e.Method = out.Method
	if out.Fallback != "" {
		e.Notes = []string{out.Fallback}
	}
	if n, err := conn.Size(e.Target); err == nil {
		e.Bytes = n
	}
	if delay > 0 {
		save()
		time.Sleep(delay)
	}
	return nil
}

func backendOf(s *volume.Session) postgres.Backend {
	return postgres.Backend{PID: s.PID, Started: s.Started}
}

// settleCopies finishes what a template copy whose clone process died left on the server:
// it ends the copy's session, which would otherwise keep main's new connections waiting
// until the server finished a copy nobody wants, and once that session is gone drops
// whatever it made. The login comes from the berth's own .env, as for remove, so status
// never reads main's. Anything it can't settle now is left on the entry for next time.
func settleCopies(rec registry.Record, st *volume.State) {
	m := rec.Postgres
	if m == nil {
		return
	}
	var pending []*volume.Entry
	for i := range st.Databases {
		if e := &st.Databases[i]; e.CopySession != nil && e.Status == volume.Failed {
			pending = append(pending, e)
		}
	}
	if len(pending) == 0 {
		return
	}
	own, _ := readEnvFile(filepath.Join(rec.Worktree, ".env"))
	login, ok := locationOf(rec, m).Login(own, environ())
	if !ok {
		return
	}
	conn, err := postgres.Open(recordedServer(m), login)
	if err != nil {
		return
	}
	defer conn.Close()
	changed := false
	for _, e := range pending {
		if gone, err := conn.EndBackend(backendOf(e.CopySession), endEarlierCopy); err != nil || !gone {
			continue
		}
		if m.Owns(e.Target) {
			if err := conn.Drop(e.Target); err != nil {
				continue
			}
		}
		e.CopySession, changed = nil, true
	}
	if changed {
		_ = volume.Save(rec.ID, *st)
	}
}

// endCopySessions ends the session of every template copy st still names.
func endCopySessions(conn hostdb.Conn, st volume.State) {
	pg, ok := conn.(*postgres.Conn)
	if !ok {
		return
	}
	for _, e := range st.Databases {
		if e.CopySession != nil {
			_, _ = pg.EndBackend(backendOf(e.CopySession), endEarlierCopy)
		}
	}
}
