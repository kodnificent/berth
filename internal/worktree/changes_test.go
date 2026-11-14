package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

var longAgo = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// committedRepo is a repository with a/b.txt committed and deps/ ignored, its files and
// index last modified longAgo.
func committedRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	for _, f := range []struct{ name, data string }{{".gitignore", "deps/\n"}, {"a/b.txt", "b\n"}} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(f.name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f.name), []byte(f.data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t.invalid", "commit", "-qm", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	for _, p := range []string{".", ".gitignore", "a", "a/b.txt", ".git/index"} {
		if err := os.Chtimes(filepath.Join(dir, p), longAgo, longAgo); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDeletingATrackedFileIsAChangeAtTheDeletion(t *testing.T) {
	dir := committedRepo(t)
	start := time.Now().Add(-time.Second)
	if err := os.Remove(filepath.Join(dir, "a", "b.txt")); err != nil {
		t.Fatal(err)
	}
	got, err := LastChange(context.Background(), dir, filepath.Join(dir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Before(start) {
		t.Errorf("after deleting a/b.txt, last change is %s, want the deletion's time or later", got)
	}
}

func TestFilesUnderAnIgnoredDirectoryAreNoChange(t *testing.T) {
	dir := committedRepo(t)
	before, err := LastChange(context.Background(), dir, filepath.Join(dir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "deps", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "deps", "pkg", "index.js"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LastChange(context.Background(), dir, filepath.Join(dir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(before) {
		t.Errorf("after writing only under ignored deps/, last change is %s, want it unchanged at %s", got, before)
	}
}
