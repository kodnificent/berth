package mysql

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func scanAll(t *testing.T, s *seedScanner) []string {
	t.Helper()
	var out []string
	for {
		stmt, _, err := s.next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("scanning: %v", err)
		}
		out = append(out, stmt)
	}
}

func scanString(t *testing.T, file string) []string {
	t.Helper()
	return scanAll(t, newSeedScanner(strings.NewReader(file)))
}

func wantStatements(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d statements %q, want %d %q", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("statement %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestScannerSplitsOutsideStringsCommentsAndNames(t *testing.T) {
	got := scanString(t, "-- a; comment\n# another; one\n/* block; */\nINSERT INTO t VALUES ('a;b', \"c;d\", `e;f`);\nSELECT 1;")
	wantStatements(t, got, "INSERT INTO t VALUES ('a;b', \"c;d\", `e;f`)", "SELECT 1")
}

func TestScannerDoesNotSplitOnEscapedOrDoubledQuotes(t *testing.T) {
	got := scanString(t, `INSERT INTO t VALUES ('it\'s;', 'it''s;', 'back\\');SELECT 2;`)
	wantStatements(t, got, `INSERT INTO t VALUES ('it\'s;', 'it''s;', 'back\\')`, "SELECT 2")
}

func TestScannerHonoursNoBackslashEscapes(t *testing.T) {
	s := newSeedScanner(strings.NewReader(`INSERT INTO t VALUES ('C:\');SELECT 2;`))
	s.backslash = false
	wantStatements(t, scanAll(t, s), `INSERT INTO t VALUES ('C:\')`, "SELECT 2")
}

func TestScannerHonoursDelimiterChanges(t *testing.T) {
	got := scanString(t, "DELIMITER ;;\nCREATE TRIGGER a BEFORE INSERT ON t FOR EACH ROW BEGIN SET @x = 1; END;;\nDELIMITER ;\nSELECT 2;\n")
	wantStatements(t, got, "CREATE TRIGGER a BEFORE INSERT ON t FOR EACH ROW BEGIN SET @x = 1; END", "SELECT 2")
}

func TestScannerPassesVersionedCommentsThroughUnchanged(t *testing.T) {
	first := "/*!40101 SET @OLD_SQL_MODE=@@SQL_MODE, SQL_MODE='NO_AUTO_VALUE_ON_ZERO' */"
	trigger := "/*!50003 CREATE*/ /*!50017 DEFINER=`root`@`%`*/ /*!50003 TRIGGER `t` BEFORE INSERT ON `k` FOR EACH ROW SET NEW.v = 1 */"
	got := scanString(t, first+";\nDELIMITER ;;\n"+trigger+";;\nDELIMITER ;\n")
	wantStatements(t, got, first, trigger)
}

func TestScannerKeepsACommentInsideAStatement(t *testing.T) {
	got := scanString(t, "SELECT 1 /* a */ + -- b\n 2;\n")
	wantStatements(t, got, "SELECT 1 /* a */ + -- b\n 2")
}

func TestScannerDropsAFileEndingInAComment(t *testing.T) {
	wantStatements(t, scanString(t, "SELECT 1;\n-- Dump completed\n"), "SELECT 1")
}

func TestScannerReportsTheLineAStatementStartsOn(t *testing.T) {
	s := newSeedScanner(strings.NewReader("\n-- comment\n\nSELECT\n1;\nSELECT 2;\n"))
	if _, line, err := s.next(); err != nil || line != 4 {
		t.Fatalf("first statement: line %d err %v, want line 4", line, err)
	}
	if _, line, err := s.next(); err != nil || line != 6 {
		t.Fatalf("second statement: line %d err %v, want line 6", line, err)
	}
}

func TestScannerFailsOnATruncatedString(t *testing.T) {
	s := newSeedScanner(strings.NewReader("INSERT INTO t VALUES ('never closed);\n"))
	if _, _, err := s.next(); !errors.Is(err, errNeverClosed) {
		t.Fatalf("got %v, want errNeverClosed", err)
	}
}

func TestScannerHoldsNoMoreThanOneBoundedStatement(t *testing.T) {
	s := newSeedScanner(strings.NewReader("SELECT '" + strings.Repeat("x", maxSeedStatement+1) + "';\n"))
	if _, _, err := s.next(); !errors.Is(err, errStatementTooLong) {
		t.Fatalf("got %v, want errStatementTooLong", err)
	}
}

func TestScannerStripsAByteOrderMark(t *testing.T) {
	wantStatements(t, scanString(t, "\xef\xbb\xbfSELECT 1;\n"), "SELECT 1")
}
