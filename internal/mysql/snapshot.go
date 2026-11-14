package mysql

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

type Snapshotted struct {
	Copied
	// Charset and Collation are the source's defaults, which Restore gives the target.
	Charset, Collation string
}

const (
	frameTable   = 't'
	frameRows    = 'r'
	frameView    = 'v'
	frameTrigger = 'g'
)

// Snapshot writes database source as it was at one moment to w, in the form Restore
// reads, through the same consistent read and the same guards as Copy, so it pauses
// nothing of main's that Copy wouldn't. Nothing is created on the server.
func Snapshot(s Server, login Login, source string, w io.Writer, o CopyOptions) (out Snapshotted, err error) {
	db, err := connect(s, login, 2, copyWait)
	if err != nil {
		return out, err
	}
	defer db.Close()
	ctx := context.Background()
	step := "reading database " + source + " for its snapshot"
	defer func() {
		if err != nil {
			err = fmt.Errorf("%s on the MySQL server at %s: %s", step, s, scrub(err, login))
		}
	}()
	err = db.QueryRowContext(ctx, "SELECT default_character_set_name, default_collation_name FROM information_schema.schemata WHERE schema_name = ?", source).Scan(&out.Charset, &out.Collation)
	out.SourceMissing = errors.Is(err, sql.ErrNoRows)
	if err != nil && !out.SourceMissing {
		return out, err
	}
	bw := bufio.NewWriterSize(w, 1<<20)
	if !out.SourceMissing {
		var packet int64
		if err := db.QueryRowContext(ctx, "SELECT @@max_allowed_packet").Scan(&packet); err != nil {
			return out, err
		}
		dest := &snapshotSink{w: bw}
		if err := consistentRead(ctx, db, source, "", dest, newWriter(dest, packet), o, &out.Copied, &step); err != nil {
			return out, err
		}
	}
	if err := bw.Flush(); err != nil {
		return out, err
	}
	return out, nil
}

// snapshotSink keeps what a consistent read reads as frames, for Restore to play back.
// What only the server can decide, such as whether a created table still keeps its rows
// outside the database, is decided there.
type snapshotSink struct {
	w io.Writer
}

func (f *snapshotSink) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	return nil, writeFrame(f.w, frameRows, "", "", query)
}

func (f *snapshotSink) createTable(_ context.Context, table, stmt string) (string, []string, error) {
	if why := definitionOutside(stmt); why != "" {
		return why, nil, nil
	}
	return "", nil, writeFrame(f.w, frameTable, table, "", stmt)
}

func (f *snapshotSink) createViews(_ context.Context, views []viewDef) ([]string, error) {
	for _, v := range views {
		if err := writeFrame(f.w, frameView, v.name, "", v.stmt); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (f *snapshotSink) createTrigger(_ context.Context, name, sqlMode, stmt string) (string, error) {
	return "", writeFrame(f.w, frameTrigger, name, sqlMode, stmt)
}

// A frame is a kind byte and three length-prefixed fields: name, extra and body. Rows
// frames carry only a body, the statement to run.
func writeFrame(w io.Writer, kind byte, name, extra, body string) error {
	if _, err := w.Write([]byte{kind}); err != nil {
		return err
	}
	for _, field := range []string{name, extra, body} {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(field)))
		if _, err := w.Write(n[:]); err != nil {
			return err
		}
		if _, err := io.WriteString(w, field); err != nil {
			return err
		}
	}
	return nil
}

// readFrame reads the next frame, io.EOF at the end of the stream.
func readFrame(r *bufio.Reader) (kind byte, name, extra, body string, err error) {
	kind, err = r.ReadByte()
	if err != nil {
		return 0, "", "", "", err
	}
	fields := [3]*string{&name, &extra, &body}
	for _, field := range fields {
		var n [4]byte
		if _, err := io.ReadFull(r, n[:]); err != nil {
			return 0, "", "", "", truncated(err)
		}
		size := binary.BigEndian.Uint32(n[:])
		if size > driverPacket {
			return 0, "", "", "", fmt.Errorf("a snapshot frame is %d bytes, more than the %d berth ever writes in one", size, driverPacket)
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(r, data); err != nil {
			return 0, "", "", "", truncated(err)
		}
		*field = string(data)
	}
	return kind, name, extra, body, nil
}

func truncated(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

type RestoreOptions struct {
	// Progress is called before each frame is applied.
	Progress func()
}

type Restored struct {
	Bytes int64
	// Notes name what the restore left out, and why.
	Notes []string
}

// Restore replaces database target on s with what Snapshot wrote to r, giving it charset
// and collation (the server's defaults when empty). The guards a copy applies run again
// here against the server as it is now, since only the table as created can be checked,
// and a database named in a view or trigger may have appeared since the snapshot.
func Restore(s Server, login Login, target string, r io.Reader, charset, collation string, o RestoreOptions) (out Restored, err error) {
	db, err := connect(s, login, 1, copyWait)
	if err != nil {
		return out, err
	}
	defer db.Close()
	ctx := context.Background()
	defer func() {
		if err != nil {
			err = fmt.Errorf("restoring database %s on the MySQL server at %s: %s", target, s, scrub(err, login))
		}
	}()
	conn, err := db.Conn(ctx)
	if err != nil {
		return out, err
	}
	defer conn.Close()
	if err := execAll(ctx, conn, targetSession...); err != nil {
		return out, err
	}
	if err := recreate(ctx, conn, target, charset, collation); err != nil {
		return out, err
	}
	others, err := databases(ctx, conn, target)
	if err != nil {
		return out, err
	}
	dest := &databaseSink{session: conn, target: target, login: login}
	var views []viewDef
	skip := false
	flushViews := func() error {
		notes, err := dest.createViews(ctx, views)
		out.Notes = append(out.Notes, notes...)
		views = nil
		return err
	}
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		kind, name, extra, body, err := readFrame(br)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out, err
		}
		if o.Progress != nil {
			o.Progress()
		}
		switch kind {
		case frameTable:
			why, dropped, err := dest.createTable(ctx, name, body)
			if err != nil {
				return out, err
			}
			if skip = why != ""; skip {
				out.Notes = append(out.Notes, fmt.Sprintf("table %s left out: %s; recreate it in post-add", name, why))
			}
			for _, d := range dropped {
				out.Notes = append(out.Notes, fmt.Sprintf("table %s keeps no foreign key into database %s, so the copy never checks against data outside it", name, d))
			}
		case frameRows:
			if !skip {
				if _, err := conn.ExecContext(ctx, body); err != nil {
					return out, err
				}
			}
		case frameView:
			if why := reachesOut(body, "", others); why != "" {
				out.Notes = append(out.Notes, fmt.Sprintf("view %s left out: %s; recreate it in post-add", name, why))
				continue
			}
			views = append(views, viewDef{name, body})
		case frameTrigger:
			if err := flushViews(); err != nil {
				return out, err
			}
			if why := reachesOut(body, extra, others); why != "" {
				out.Notes = append(out.Notes, fmt.Sprintf("trigger %s left out: %s; recreate it in post-add", name, why))
				continue
			}
			note, err := dest.createTrigger(ctx, name, extra, body)
			if note != "" {
				out.Notes = append(out.Notes, note)
			}
			if err != nil {
				return out, err
			}
		default:
			return out, fmt.Errorf("the snapshot holds a frame of unknown kind %q", kind)
		}
	}
	if err := flushViews(); err != nil {
		return out, err
	}
	if out.Bytes, err = measure(ctx, conn, target); err != nil {
		return out, err
	}
	return out, nil
}
