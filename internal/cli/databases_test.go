package cli

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/mysql"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/snapshot"
	"github.com/kodnificent/berth/internal/worktree"
)

func TestListCountsTheBerthsDatabasesButNotMains(t *testing.T) {
	b := registry.Live{Record: registry.Record{ID: "a", MySQL: &registry.HostDB{Databases: []registry.Database{
		{Source: "app_central", Target: "app_a__central", Strategy: "fresh", Bytes: 3 << 20},
		{Source: "shop", Target: "shop", Strategy: "none", Bytes: 9 << 20},
	}}}}
	if got := diskUse([]registry.Live{b})["a"]; got != 3<<20 {
		t.Errorf("disk use = %d, want the berth's own database's %d", got, 3<<20)
	}
}

func TestDoctorNeverPrintsALoginThatNamesADatabase(t *testing.T) {
	main, berth := t.TempDir(), t.TempDir()
	env := "DB_HOST=127.0.0.1\nDB_DATABASE=homestead\nDB_USERNAME=homestead\nDB_PASSWORD=homestead\n"
	for _, dir := range []string{main, berth} {
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rec := registry.Record{MySQL: &registry.HostDB{Host: "127.0.0.1", Port: 3306, EnvPrefix: "DB_", Databases: []registry.Database{
		{Key: "homestead", Source: "homestead", Target: "homestead__feat", Strategy: "fresh", Env: []string{"DB_DATABASE"}},
	}}}
	found := databaseFindings(&worktree.Worktree{Path: berth, Repository: main}, rec)
	var named bool
	for _, f := range found {
		if strings.Contains(f.Location+f.Message, "DB_USERNAME") || strings.Contains(f.Location+f.Message, "DB_PASSWORD") {
			t.Errorf("doctor reports a login key: %s %s", f.Location, f.Message)
		}
		named = named || f.Location == ".env DB_DATABASE"
	}
	if !named {
		t.Errorf("doctor does not report DB_DATABASE still naming main's homestead: %+v", found)
	}
}

func TestChoosingAPrefixWithNoRoomRefusesTheConfig(t *testing.T) {
	cfg, err := config.Parse("/repo/berth.toml", []byte("[state.mysql]\nfrom = \"env:DB_*\"\nprefix = \"env:P\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	source := strings.Repeat("p", 53) + "_"
	d := &dbSetup{cfg: cfg, host: cfg.State.MySQL, engine: mysql.Engine, source: source}
	dbs := []registry.Database{{Key: "central", Source: source + "central", Strategy: "fresh"}}
	taken := []string{source + "f__central"}
	_, _, err = d.choose("f", dbs, taken, nil)
	if apperr.CodeOf(err) != apperr.ConfigInvalid || !strings.Contains(err.Error(), "state.mysql.prefix") {
		t.Errorf("choose with %d characters left for the berth = %v, want config_invalid naming state.mysql.prefix", 64-len(source)-2-len("central"), err)
	}
}

func TestChoosingNamesWithoutAPrefixEnds(t *testing.T) {
	cfg, err := config.Parse("/repo/berth.toml", []byte("[state.mysql]\nfrom = \"env:DB_*\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	d := &dbSetup{cfg: cfg, host: cfg.State.MySQL, engine: mysql.Engine}
	dbs := []registry.Database{
		{Key: "company_analytics_warehouse_events", Source: "company_analytics_warehouse_events", Strategy: "fresh"},
		{Key: "company_analytics_warehouse_orders", Source: "company_analytics_warehouse_orders", Strategy: "fresh"},
	}
	done := make(chan string)
	go func() {
		seg, _, _ := d.choose(strings.Repeat("feature-", 25), dbs, nil, nil)
		done <- seg
	}()
	select {
	case seg := <-done:
		if seg == "" {
			t.Errorf("choose gave no segment")
		}
	case <-timeAfter():
		t.Fatal("choose did not return for two long databases sharing a stem")
	}
}

func TestRemoveKeepsTheBerthsEnvWhileADatabaseIsLeft(t *testing.T) {
	t.Setenv("BERTH_STATE_DIR", t.TempDir())
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("DB_USERNAME=u\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := registry.Record{ID: "a", Files: []string{".env"}, MySQL: &registry.HostDB{Host: "127.0.0.1", Port: port, EnvPrefix: "DB_", Prefix: "app_a__"}}
	td := &teardown{rec: rec, dir: dir, tracked: map[string]bool{},
		items: []item{{kindDatabase, "app_a__central"}, {kindFile, ".env"}},
		db:    &dbTeardown{mysql.Server{Host: "127.0.0.1", Port: port}, mysql.Login{User: "u"}}}
	reg, err := registry.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	if _, err := td.execute(reg); err == nil {
		t.Fatal("execute with the server gone succeeded, want an error")
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); err != nil {
		t.Errorf("the berth's .env, which holds the login a retry needs, is gone: %v", err)
	}
}

func timeAfter() <-chan time.Time { return time.After(10 * time.Second) }

func TestExactNameInAnotherBerthsRecordIsPassedOver(t *testing.T) {
	cfg, err := config.Parse("/repo/berth.toml", []byte("[state.mysql]\nfrom = \"env:DB_*\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	server := mysql.Server{Host: "127.0.0.1", Port: 3306}
	d := &dbSetup{cfg: cfg, host: cfg.State.MySQL, engine: mysql.Engine, server: server}
	other := registry.Record{MySQL: &registry.HostDB{Host: "localhost", Port: 3306, Databases: []registry.Database{
		{Source: "myapp", Target: "myapp__feat_x", Strategy: "fresh"},
	}}}
	dbs := []registry.Database{{Key: "myapp", Source: "myapp", Strategy: "fresh"}}
	seg, _, err := d.choose("feat-x", dbs, nil, []registry.Record{other})
	if err != nil || seg == "feat_x" {
		t.Errorf("choose with myapp__feat_x in another berth's record = %q, %v; want another segment", seg, err)
	}
}

func TestMainEnvSyntaxErrorNamesTheLineButNotTheValue(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, ".env")
	if err := os.WriteFile(path, []byte("DB_HOST=127.0.0.1\nDB_PASSWORD=\"hunter2secret\nDB_USERNAME=app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readMainEnv(&worktree.Worktree{Repository: repo}, mysql.Engine)
	if err == nil {
		t.Fatal("readMainEnv accepted an unterminated quoted value")
	}
	if msg := err.Error(); strings.Contains(msg, "hunter2") || !strings.Contains(msg, path) || !strings.Contains(msg, "line 2") {
		t.Errorf("readMainEnv's error is %q, want the file and line 2 and never the value", msg)
	}
}

func TestCopyRefusesToHoldATableAddDidNotAnnounce(t *testing.T) {
	err := refuseUnannounced("app_central", []string{"legacy_log"}, []string{"legacy_log", "sessions"}, "berth reset")
	if err == nil || !strings.Contains(err.Error(), "sessions") || !strings.Contains(err.Error(), "berth reset") {
		t.Errorf("holding sessions, which add never announced, gave %v; want a refusal naming it and berth reset", err)
	}
}

func TestResetUsesTheSeedFileAndCommandTheConfigNamesNow(t *testing.T) {
	rec := registry.Record{MySQL: &registry.HostDB{Databases: []registry.Database{
		{Key: "tenant", Strategy: config.File, File: "old.sql"},
		{Key: "reports", Strategy: config.Command, Command: "./old.sh"},
		{Key: "moved", Strategy: config.File, File: "keep.sql"},
	}}}
	cfg := &config.Config{}
	cfg.State.MySQL = &config.HostDB{Databases: map[string]any{
		"tenant":  map[string]any{"from": "file:new.sql"},
		"reports": map[string]any{"command": "./new.sh"},
		"moved":   "fresh",
	}}

	refreshDatabaseSources(&rec, cfg)

	if got := rec.MySQL.Databases[0].File; got != "new.sql" {
		t.Errorf("seed file = %q, want the config's new.sql", got)
	}
	if got := rec.MySQL.Databases[1].Command; got != "./new.sh" {
		t.Errorf("command = %q, want the config's ./new.sh", got)
	}
	if got := rec.MySQL.Databases[2].File; got != "keep.sql" {
		t.Errorf("a database whose strategy changed keeps its recorded file, got %q", got)
	}
}

func TestSnapshotRefusesASourceOrServerOtherThanTheBerths(t *testing.T) {
	set := &snapshot.Set{Manifest: snapshot.Manifest{Host: "127.0.0.1", Port: 3306, Databases: []snapshot.Database{{Key: "central", Source: "app_central"}}}}
	server := mysql.Server{Host: "localhost", Port: 3306}
	if why := snapshotRefusal(set, "central", "app_central", server); why != "" {
		t.Errorf("the same source on the same server was refused: %s", why)
	}
	if why := snapshotRefusal(set, "central", "other_central", server); why == "" {
		t.Errorf("a snapshot of another source wasn't refused")
	}
	if why := snapshotRefusal(set, "central", "app_central", mysql.Server{Host: "db.internal", Port: 3306}); why == "" {
		t.Errorf("a snapshot from another server wasn't refused")
	}
	if why := snapshotRefusal(nil, "central", "app_central", server); why == "" {
		t.Errorf("no snapshot at all wasn't refused")
	}
}

func TestRemoveRefusesToDropADatabaseTheRecordDoesNotOwn(t *testing.T) {
	t.Setenv("BERTH_STATE_DIR", t.TempDir())
	rec := registry.Record{ID: "a", Postgres: &registry.HostDB{Host: "127.0.0.1", Port: 1, EnvPrefix: "DB_", Prefix: "app_a__",
		Databases: []registry.Database{{Key: "central", Source: "app_central", Target: "app_a__central", Strategy: "fresh"}}}}
	td := &teardown{rec: rec, tracked: map[string]bool{}, items: []item{{kindDatabase, "app_central"}},
		db: &dbTeardown{mysql.Server{Host: "127.0.0.1", Port: 1}, mysql.Login{User: "u"}}}
	reg, err := registry.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	_, err = td.execute(reg)
	if err == nil || !strings.Contains(err.Error(), "not recorded as berth a's own") {
		t.Errorf("dropping main's app_central from berth a's teardown gave %v, want a refusal before connecting", err)
	}
}

func TestDoctorReportsAURLThatIsNotTheBerthsOwnDatabase(t *testing.T) {
	main, berth := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(main, ".env"), []byte("DATABASE_URL=postgres://u:hunter2@127.0.0.1:5432/myapp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(berth, ".env"), []byte("DATABASE_URL=postgres://u:hunter2@127.0.0.1:5432/reports\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := registry.Record{Postgres: &registry.HostDB{Host: "127.0.0.1", Port: 5432, URLKey: "DATABASE_URL", Databases: []registry.Database{
		{Key: "myapp", Source: "myapp", Target: "myapp__feat", Strategy: "fresh", URL: true},
	}}}
	var reported bool
	for _, f := range databaseFindings(&worktree.Worktree{Path: berth, Repository: main}, rec) {
		if strings.Contains(f.Message, "hunter2") {
			t.Errorf("doctor prints the URL's password: %s", f.Message)
		}
		reported = reported || f.Level == problem && f.Location == ".env DATABASE_URL"
	}
	if !reported {
		t.Error("doctor does not report DATABASE_URL naming reports instead of the berth's myapp__feat")
	}
}
