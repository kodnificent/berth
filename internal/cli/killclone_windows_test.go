package cli

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestKillCloneLeavesAProcessOfAnotherExecutable(t *testing.T) {
	src, err := os.Open(self(t))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	other := filepath.Join(t.TempDir(), "other.exe")
	dst, err := os.Create(other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	pid, stdin, exit := startClone(t, other, "a1b2c3")

	if err := killClone("a1b2c3", pid); err != nil {
		t.Fatal(err)
	}

	stdin.Close()
	if code := exitCode(t, exit); code != 0 {
		t.Errorf("the process exited %d, want it left to run to its end", code)
	}
}
