// Package lastuse keeps when each berth was last used by a berth command, one file per
// berth beside the registry, so recording a use never rewrites the berth's record.
package lastuse

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/safefile"
)

func path(id string) (string, error) {
	d, err := registry.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "lastuse", id), nil
}

// Record notes that berth id was used at at. The caller holds the registry lock and has
// found the berth's record, so a use is never recorded for a berth being removed.
func Record(id string, at time.Time) error {
	p, err := path(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-"+id+"-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(at.UTC().Format(time.RFC3339Nano) + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return safefile.Replace(tmp.Name(), p)
}

// Load is when a berth command last used berth id; ok is false when none recorded it,
// as for a berth made before berth kept last use.
func Load(id string) (at time.Time, ok bool) {
	p, err := path(id)
	if err != nil {
		return time.Time{}, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return time.Time{}, false
	}
	at, err = time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	return at, err == nil
}

// Forget deletes what berth recorded of berth id's use.
func Forget(id string) error {
	p, err := path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
