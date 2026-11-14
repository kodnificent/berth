package safefile

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReplaceSucceedsOnceAReaderClosesTheFile(t *testing.T) {
	dir := t.TempDir()
	path, tmp := filepath.Join(dir, "state.json"), filepath.Join(dir, "state.json.tmp")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmp, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(100*time.Millisecond, func() { reader.Close() })

	if err := Replace(tmp, path); err != nil {
		t.Fatalf("Replace with a reader that closes the file soon after: %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "new" {
		t.Errorf("after Replace the file holds %q, want new", data)
	}
}
