package mysql

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	driver "github.com/go-sql-driver/mysql"
)

// maxSeedStatement is the longest statement Seed holds in memory, and the most the
// driver sends in one.
const maxSeedStatement = driverPacket

// erEmptyQuery is what the server answers when every part of a statement is a versioned
// comment it skips, leaving nothing to run.
const erEmptyQuery = 1065

// SeedOptions are what Seed reports while it runs.
type SeedOptions struct {
	// Progress is called before each statement is sent, so one that runs long shows.
	Progress func()
}

// Seeded is how a load went.
type Seeded struct {
	// Bytes is the target's size once loaded.
	Bytes int64
}

// Seed replaces database target on s with an empty one and runs the SQL read from r in
// it, one statement at a time, as the mysql client would. The statements are the user's
// own and run as written.
func Seed(s Server, login Login, target string, r io.Reader, o SeedOptions) (out Seeded, err error) {
	// No read timeout: one statement of a seed file may run as long as it needs.
	db, err := connect(s, login, 1, 0)
	if err != nil {
		return out, err
	}
	defer db.Close()
	ctx := context.Background()
	defer func() {
		if err != nil {
			err = fmt.Errorf("loading into database %s on the MySQL server at %s: %s", target, s, scrub(err, login))
		}
	}()
	conn, err := db.Conn(ctx)
	if err != nil {
		return out, err
	}
	defer conn.Close()
	if err := execAll(ctx, conn, "DROP DATABASE IF EXISTS "+Quote(target), "CREATE DATABASE "+Quote(target), "USE "+Quote(target)); err != nil {
		return out, err
	}
	scan := newSeedScanner(r)
	if scan.backslash, err = backslashEscapes(ctx, conn); err != nil {
		return out, err
	}
	for {
		stmt, line, err := scan.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out, fmt.Errorf("line %d: %w", line, err)
		}
		if o.Progress != nil {
			o.Progress()
		}
		var serverErr *driver.MySQLError
		if _, err := conn.ExecContext(ctx, stmt); err != nil && !(errors.As(err, &serverErr) && serverErr.Number == erEmptyQuery) {
			return out, fmt.Errorf("line %d: %w", line, err)
		}
		if strings.Contains(strings.ToLower(stmt), "sql_mode") {
			if scan.backslash, err = backslashEscapes(ctx, conn); err != nil {
				return out, err
			}
		}
	}
	if out.Bytes, err = measure(ctx, conn, target); err != nil {
		return out, err
	}
	return out, nil
}

// backslashEscapes reports whether a backslash escapes inside strings in this session.
// A dump from a server with NO_BACKSLASH_ESCAPES sets it and then writes strings ending
// in a backslash, which would otherwise read as never closed.
func backslashEscapes(ctx context.Context, s session) (bool, error) {
	var mode string
	if err := s.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode").Scan(&mode); err != nil {
		return false, err
	}
	return !strings.Contains(strings.ToUpper(mode), "NO_BACKSLASH_ESCAPES"), nil
}

var (
	errStatementTooLong = fmt.Errorf("a statement is longer than %d bytes, the most berth sends in one", maxSeedStatement)
	errNeverClosed      = errors.New("a string, quoted name or comment is never closed")
)

// seedScanner splits a stream of SQL into statements the way the mysql client does: on
// its delimiter, which DELIMITER changes, outside strings, quoted names and comments. It
// holds only the current statement in memory.
type seedScanner struct {
	r         *bufio.Reader
	delim     string
	backslash bool
	line      int
	held      int
}

func newSeedScanner(r io.Reader) *seedScanner {
	br := bufio.NewReaderSize(r, 1<<20)
	if bom, _ := br.Peek(3); bytes.Equal(bom, []byte{0xEF, 0xBB, 0xBF}) {
		_, _ = br.Discard(3)
	}
	return &seedScanner{r: br, delim: ";", backslash: true, line: 1}
}

func (s *seedScanner) read() (byte, error) {
	c, err := s.r.ReadByte()
	if err == nil {
		if c == '\n' {
			s.line++
		}
		if s.held++; s.held > maxSeedStatement {
			return c, errStatementTooLong
		}
	}
	return c, err
}

// next returns the next statement, without its delimiter, and the line it starts on, or
// io.EOF at the end of the stream. Comments before a statement are dropped, so a file
// ending in one sends the server nothing; those inside it, and versioned comments, go
// to the server as written.
func (s *seedScanner) next() (string, int, error) {
	var b []byte
	start := s.line
	for {
		c, err := s.read()
		if errors.Is(err, io.EOF) {
			if len(b) == 0 {
				return "", 0, io.EOF
			}
			return string(b), start, nil
		}
		if err != nil {
			return "", s.line, err
		}
		if len(b) == 0 {
			s.held = 0
			if isSpace(c) {
				continue
			}
			start = s.line
			if c == 'd' || c == 'D' {
				if ok, err := s.delimiterCommand(); err != nil {
					return "", start, err
				} else if ok {
					continue
				}
			}
		}
		switch {
		case c == s.delim[0] && s.atDelimiter():
			if len(b) > 0 {
				return string(b), start, nil
			}
		case c == '\'' || c == '"' || c == '`':
			b, err = s.quoted(append(b, c), c)
		case c == '#' || (c == '-' && s.startsDashComment()):
			var rest []byte
			if rest, err = s.restOfLine(); err == nil && len(b) > 0 {
				b = append(append(b, c), rest...)
			}
		case c == '/' && s.startsPlainComment():
			b, err = s.blockComment(b)
		default:
			b = append(b, c)
		}
		if err != nil {
			return "", start, err
		}
	}
}

func (s *seedScanner) atDelimiter() bool {
	rest, _ := s.r.Peek(len(s.delim) - 1)
	if string(rest) != s.delim[1:] {
		return false
	}
	_, _ = s.r.Discard(len(rest))
	return true
}

// startsDashComment reports whether the - just read starts a -- comment, as the server
// reads one: a second dash, then whitespace, a control character or the end.
func (s *seedScanner) startsDashComment() bool {
	rest, _ := s.r.Peek(2)
	return len(rest) >= 1 && rest[0] == '-' && (len(rest) == 1 || rest[1] <= ' ' || rest[1] == 0x7f)
}

// startsPlainComment reports whether the / just read starts a comment other than a
// versioned one, whose text is code: the mysql client reads strings and delimiters in it.
func (s *seedScanner) startsPlainComment() bool {
	rest, _ := s.r.Peek(3)
	if len(rest) < 1 || rest[0] != '*' {
		return false
	}
	versioned := len(rest) >= 2 && rest[1] == '!' || len(rest) == 3 && rest[1] == 'M' && rest[2] == '!'
	return !versioned
}

// delimiterCommand consumes a DELIMITER line when the byte already read starts one.
func (s *seedScanner) delimiterCommand() (bool, error) {
	rest, _ := s.r.Peek(len("ELIMITER") + 1)
	if len(rest) < 9 || !strings.EqualFold(string(rest[:8]), "ELIMITER") || !isSpace(rest[8]) {
		return false, nil
	}
	_, _ = s.r.Discard(8)
	line, err := s.restOfLine()
	if err != nil {
		return true, err
	}
	fields := strings.Fields(string(line))
	if len(fields) == 0 {
		return true, errors.New("a DELIMITER line names no delimiter")
	}
	s.delim = fields[0]
	return true, nil
}

// quoted appends the rest of a string or quoted name, whose opening quote q is in b. A
// doubled quote needs no handling: it reads as one ending and another beginning.
func (s *seedScanner) quoted(b []byte, q byte) ([]byte, error) {
	for {
		c, err := s.read()
		if errors.Is(err, io.EOF) {
			return b, errNeverClosed
		}
		if err != nil {
			return b, err
		}
		b = append(b, c)
		switch {
		case c == '\\' && q != '`' && s.backslash:
			next, err := s.read()
			if errors.Is(err, io.EOF) {
				return b, errNeverClosed
			}
			if err != nil {
				return b, err
			}
			b = append(b, next)
		case c == q:
			return b, nil
		}
	}
}

// restOfLine reads through the end of the line.
func (s *seedScanner) restOfLine() ([]byte, error) {
	var line []byte
	for {
		c, err := s.read()
		if errors.Is(err, io.EOF) {
			return line, nil
		}
		if err != nil {
			return line, err
		}
		line = append(line, c)
		if c == '\n' {
			return line, nil
		}
	}
}

// blockComment reads through the first */, appending the comment to a statement already
// begun.
func (s *seedScanner) blockComment(b []byte) ([]byte, error) {
	keep := len(b) > 0
	if keep {
		b = append(b, '/')
	}
	var prev byte
	for {
		c, err := s.read()
		if errors.Is(err, io.EOF) {
			return b, errNeverClosed
		}
		if err != nil {
			return b, err
		}
		if keep {
			b = append(b, c)
		}
		if prev == '*' && c == '/' {
			return b, nil
		}
		prev = c
	}
}

func isSpace(c byte) bool { return c == ' ' || (c >= '\t' && c <= '\r') }
