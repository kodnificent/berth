package mysql

import (
	"bytes"
	"strings"
	"testing"
)

func TestLiteralTellsNullFromAnEmptyString(t *testing.T) {
	c := column{name: "note", kind: "varchar", charset: "utf8mb4"}
	var null, empty bytes.Buffer
	c.literal(&null, nil)
	c.literal(&empty, []byte{})
	if null.String() != "NULL" || empty.String() != "_utf8mb4 X''" {
		t.Errorf("NULL writes %q and an empty string %q, want NULL and _utf8mb4 X''", null.String(), empty.String())
	}
}

func TestLiteralKeepsAColumnsOwnCharacterSet(t *testing.T) {
	var b bytes.Buffer
	column{name: "name", kind: "char", charset: "latin1"}.literal(&b, []byte{0xe9})
	if b.String() != "_latin1 X'e9'" {
		t.Errorf("a latin1 é writes %q, want _latin1 X'e9' so the server stores the same byte", b.String())
	}
}

func TestColumnsForFindsATableTheServerNamesInAnotherCase(t *testing.T) {
	cols, err := columnsFor(map[string][]column{"users": {{name: "id", kind: "int"}}}, "Users")
	if err != nil || len(cols) != 1 {
		t.Errorf("columnsFor(Users) with columns listed under users = %v, %v; want its one column", cols, err)
	}
}

func TestColumnsForRefusesATableWithNoColumns(t *testing.T) {
	if cols, err := columnsFor(map[string][]column{"orders": {{name: "id", kind: "int"}}}, "users"); err == nil {
		t.Errorf("columnsFor(users) with no columns listed = %v; want an error rather than copying no rows", cols)
	}
}

func TestReferencedDatabaseFindsEveryWayOfNamingMain(t *testing.T) {
	head := "CREATE TRIGGER `kept_audit` AFTER INSERT ON `kept` FOR EACH ROW "
	cases := map[string]string{
		"plain":                   "INSERT INTO app.audit (what) VALUES (NEW.k)",
		"line comment before dot": "INSERT INTO app -- x\n .audit (what) VALUES (NEW.k)",
		"empty block comment":     "INSERT INTO shop/**/ .audit (what) VALUES (NEW.k)",
		"vertical tab":            "INSERT INTO shop\v.audit (what) VALUES (NEW.k)",
		"hash comment":            "INSERT INTO shop#x\n.audit (what) VALUES (NEW.k)",
		"quoted around a comment": "INSERT INTO `shop`/*x*/ .`audit` (what) VALUES (NEW.k)",
		"procedure call":          "CALL app/**/.p()",
		"no dot after the name":   "INSERT INTO audit SELECT * FROM shop",
		"quote in a comment":      "BEGIN -- it's\n INSERT INTO shop.audit VALUES (1); END",
		"different case":          "INSERT INTO SHOP.audit VALUES (1)",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			db, err := referencedDatabase(head+body, "", []string{"app", "shop"})
			if err != nil || db == "" {
				t.Errorf("referencedDatabase(%q) = %q, %v; want app or shop found", body, db, err)
			}
		})
	}
}

func TestReferencedDatabaseReadsDoubleQuotesAsNamesUnderAnsiQuotes(t *testing.T) {
	db, err := referencedDatabase(`INSERT INTO "shop" . "audit" VALUES (1)`, "ANSI_QUOTES,STRICT_TRANS_TABLES", []string{"shop"})
	if err != nil || db != "shop" {
		t.Errorf("referencedDatabase under ANSI_QUOTES = %q, %v; want shop", db, err)
	}
}

func TestReferencedDatabaseEndsAStringAtABackslashWithoutBackslashEscapes(t *testing.T) {
	stmt := `SET @x = 'a\'; INSERT INTO shop.audit VALUES ('b')`
	db, err := referencedDatabase(stmt, "NO_BACKSLASH_ESCAPES", []string{"shop"})
	if err != nil || db != "shop" {
		t.Errorf("referencedDatabase under NO_BACKSLASH_ESCAPES = %q, %v; want shop, since the string ends before it", db, err)
	}
}

func TestReferencedDatabaseIgnoresANameInsideAString(t *testing.T) {
	db, err := referencedDatabase(`SET NEW.note = 'see shop.audit'`, "", []string{"shop"})
	if err != nil || db != "" {
		t.Errorf("referencedDatabase found %q, %v in a string literal, want nothing", db, err)
	}
}

func TestReferencedDatabaseRefusesWhatItCannotReadThrough(t *testing.T) {
	for _, stmt := range []string{
		"INSERT INTO audit VALUES (1) /* never closed",
		"INSERT INTO audit VALUES ('never closed)",
		"INSERT INTO /*!80000 shop. */ audit VALUES (1)",
		"INSERT INTO /*+ hint */ audit VALUES (1)",
	} {
		if _, err := referencedDatabase(stmt, "", []string{"shop"}); err == nil {
			t.Errorf("referencedDatabase(%q) read it through, want an error so the object is left out", stmt)
		}
	}
}

func TestWithoutForeignDatabasesDropsAKeyIntoAnotherDatabase(t *testing.T) {
	stmt := "CREATE TABLE `orders` (\n  `id` int NOT NULL,\n  `user_id` int NOT NULL,\n  PRIMARY KEY (`id`),\n  CONSTRAINT `own` FOREIGN KEY (`id`) REFERENCES `items` (`id`),\n  CONSTRAINT `out` FOREIGN KEY (`user_id`) REFERENCES `shop`.`users` (`id`)\n) ENGINE=InnoDB"
	want := "CREATE TABLE `orders` (\n  `id` int NOT NULL,\n  `user_id` int NOT NULL,\n  PRIMARY KEY (`id`),\n  CONSTRAINT `own` FOREIGN KEY (`id`) REFERENCES `items` (`id`)\n) ENGINE=InnoDB"
	got, dropped, err := withoutForeignDatabases(stmt)
	if err != nil || got != want || len(dropped) != 1 || dropped[0] != "shop" {
		t.Errorf("withoutForeignDatabases =\n%s\n%v %v\nwant\n%s\n[shop]", got, dropped, err, want)
	}
}

func TestWithoutForeignDatabasesDropsAKeyWhoseNameHoldsANewline(t *testing.T) {
	stmt := "CREATE TABLE `fk` (\n  `id` int NOT NULL,\n  `item` int DEFAULT NULL,\n  PRIMARY KEY (`id`),\n  CONSTRAINT `a\nb` FOREIGN KEY (`item`) REFERENCES `shop`.`items` (`id`)\n) ENGINE=InnoDB"
	got, dropped, err := withoutForeignDatabases(stmt)
	if err != nil || strings.Contains(got, "REFERENCES") || len(dropped) != 1 {
		t.Errorf("withoutForeignDatabases =\n%s\n%v %v\nwant the key named a newline b dropped", got, dropped, err)
	}
}

func TestReferencedDatabaseRefusesANameOutsideASCII(t *testing.T) {
	if db, err := referencedDatabase("INSERT INTO `maİn`.audit VALUES (1)", "", []string{"main"}); err == nil {
		t.Errorf("referencedDatabase read `maİn`.audit as naming %q, want an error, since a folding server reads it as main", db)
	}
}

func TestWithoutForeignDatabasesReadsAPartitionedTable(t *testing.T) {
	stmt := "CREATE TABLE `parts` (\n  `id` int NOT NULL,\n  PRIMARY KEY (`id`)\n) ENGINE=InnoDB\n/*!50100 PARTITION BY RANGE (`id`)\n(PARTITION p0 VALUES LESS THAN (100) ENGINE = InnoDB,\n PARTITION p1 VALUES LESS THAN MAXVALUE ENGINE = InnoDB) */"
	got, dropped, err := withoutForeignDatabases(stmt)
	if err != nil || got != stmt || len(dropped) != 0 {
		t.Errorf("withoutForeignDatabases on a partitioned table = %v, %v; want it unchanged", dropped, err)
	}
}

func TestOutsideOptionFindsAMergeUnion(t *testing.T) {
	stmt := "CREATE TABLE `entries` (\n  `id` int NOT NULL\n) ENGINE=MRG_MyISAM DEFAULT CHARSET=utf8mb4 INSERT_METHOD=LAST UNION=(`entries`)"
	if got, err := outsideOption(stmt); err != nil || got != "UNION" {
		t.Errorf("outsideOption on a MERGE table = %q, %v; want UNION", got, err)
	}
}

func TestOutsideOptionFindsADataDirectoryInAPartition(t *testing.T) {
	stmt := "CREATE TABLE `parts` (\n  `id` int NOT NULL\n) ENGINE=InnoDB\n/*!50100 PARTITION BY HASH (`id`)\n(PARTITION p0 DATA DIRECTORY = '/elsewhere' ENGINE = InnoDB) */"
	if got, err := outsideOption(stmt); err != nil || got != "DIRECTORY" {
		t.Errorf("outsideOption on a partition with a data directory = %q, %v; want DIRECTORY", got, err)
	}
}
