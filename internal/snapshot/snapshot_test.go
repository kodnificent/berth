package snapshot

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("BERTH_STATE_DIR", t.TempDir())
	return filepath.Join(t.TempDir(), "repo")
}

type taken struct {
	gen *Generation
	db  Database
}

func take(t *testing.T, repo, data string) taken {
	t.Helper()
	gen, err := Begin(repo)
	if err != nil {
		t.Fatal(err)
	}
	w, name, err := gen.Create("central")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, data); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return taken{gen, Database{Key: "central", Source: "app_central", File: name, TakenAt: time.Unix(1, 0)}}
}

func commit(t *testing.T, repo string, tk taken) {
	t.Helper()
	if err := tk.gen.Commit(Manifest{Databases: []Database{tk.db}}); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, repo string) string {
	t.Helper()
	set, ok, err := Open(repo, []string{"central"})
	if err != nil || !ok {
		t.Fatalf("Open: ok %v err %v", ok, err)
	}
	defer set.Close()
	r, err := set.Reader("central")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSnapshotsAreOnlyReadableByTheUser(t *testing.T) {
	repo := newRepo(t)
	commit(t, repo, take(t, repo, "rows"))
	root, _ := filepath.Glob(filepath.Join(os.Getenv("BERTH_STATE_DIR"), "snapshots", "*"))
	if len(root) != 1 {
		t.Fatalf("snapshot directories: %v", root)
	}
	err := filepath.WalkDir(root[0], func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return expectPrivate(t, path, d)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPreviousSnapshotReadsUntilTheNewOneIsCommitted(t *testing.T) {
	repo := newRepo(t)
	commit(t, repo, take(t, repo, "first"))

	next := take(t, repo, "second")
	if got := read(t, repo); got != "first" {
		t.Errorf("before commit, the snapshot reads %q, want the previous one", got)
	}
	commit(t, repo, next)
	if got := read(t, repo); got != "second" {
		t.Errorf("after commit, the snapshot reads %q, want the new one", got)
	}
}

func TestBeginRemovesAGenerationAKilledRunLeftBehind(t *testing.T) {
	repo := newRepo(t)
	commit(t, repo, take(t, repo, "kept"))
	killed := take(t, repo, "partial")
	partial := filepath.Join(killed.gen.dir, killed.gen.name)

	commit(t, repo, take(t, repo, "kept"))

	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Errorf("the killed run's generation %s is still there (%v)", partial, err)
	}
	if got := read(t, repo); got != "kept" {
		t.Errorf("after recovering, the snapshot reads %q", got)
	}
}

func TestAbortLeavesThePreviousSnapshotAndRemovesItsOwnFiles(t *testing.T) {
	repo := newRepo(t)
	commit(t, repo, take(t, repo, "kept"))
	gen := take(t, repo, "partial").gen
	partial := filepath.Join(gen.dir, gen.name)

	gen.Abort()

	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Errorf("Abort left %s (%v)", partial, err)
	}
	if got := read(t, repo); got != "kept" {
		t.Errorf("after Abort, the snapshot reads %q", got)
	}
}

func TestDatabaseMissingFromTheManifestReadsAsNoSnapshot(t *testing.T) {
	repo := newRepo(t)
	commit(t, repo, take(t, repo, "rows"))

	set, ok, err := Open(repo, []string{"reports"})
	if err != nil || !ok {
		t.Fatalf("Open: ok %v err %v", ok, err)
	}
	defer set.Close()

	if _, err := set.Reader("reports"); err == nil {
		t.Errorf("Reader of a database the snapshot doesn't hold gave no error")
	}
}

func TestSnapshotsOfDifferentRepositoriesDoNotMix(t *testing.T) {
	repo := newRepo(t)
	commit(t, repo, take(t, repo, "mine"))
	other := filepath.Join(filepath.Dir(repo), "other")

	if _, ok, err := Open(other, []string{"central"}); err != nil || ok {
		t.Errorf("Open from another repository: ok %v err %v", ok, err)
	}
}

func TestManifestOfAnotherFormatReadsAsNoSnapshot(t *testing.T) {
	repo := newRepo(t)
	tk := take(t, repo, "rows")
	commit(t, repo, tk)
	if err := os.WriteFile(filepath.Join(tk.gen.dir, manifestName), []byte(`{"format": 99}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := Load(repo); err != nil || ok {
		t.Errorf("Load of another format: ok %v err %v", ok, err)
	}
}

func TestAReaderOpenedBeforeAReplacementKeepsReadingItsGeneration(t *testing.T) {
	repo := newRepo(t)
	commit(t, repo, take(t, repo, "first"))
	set, ok, err := Open(repo, []string{"central"})
	if err != nil || !ok {
		t.Fatalf("Open: ok %v err %v", ok, err)
	}
	defer set.Close()

	next := take(t, repo, "second")
	commit(t, repo, next)
	if err := next.gen.RemoveReplaced(); err != nil {
		t.Fatal(err)
	}

	r, err := set.Reader("central")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	if err != nil || string(data) != "first" {
		t.Errorf("the set opened before the replacement read %q, %v; want its own generation", data, err)
	}
}
