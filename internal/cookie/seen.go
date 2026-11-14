package cookie

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/safefile"
)

// MaxSeen bounds how many cookies berth keeps for one berth, so an app setting endless
// names can't grow the file.
const MaxSeen = 64

const (
	maxName   = 128
	maxDomain = 255
	maxURL    = 512
)

// Seen is a cookie berth proxy saw a berth's app set with a domain wider than the
// berth's host. It holds the cookie's name and domain, never its value.
type Seen struct {
	Name      string    `json:"name"`
	Domain    string    `json:"domain"`
	URL       string    `json:"url"`
	Rewritten bool      `json:"rewritten,omitempty"`
	At        time.Time `json:"at"`
}

func seenPath(id string) (string, error) {
	d, err := registry.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "cookies", id+".json"), nil
}

// Note adds s to list, replacing what list held for a cookie of the same name, and drops
// the oldest when list is full. changed is false when list already said the same.
func Note(list []Seen, s Seen) (out []Seen, changed bool) {
	s.Name, s.Domain, s.URL = Printable(s.Name, maxName), Printable(s.Domain, maxDomain), Printable(s.URL, maxURL)
	for i, old := range list {
		if old.Name == s.Name {
			if old.Domain == s.Domain && old.URL == s.URL && old.Rewritten == s.Rewritten {
				return list, false
			}
			out = append(append(append([]Seen{}, list[:i]...), list[i+1:]...), s)
			return out, true
		}
	}
	out = append(append([]Seen{}, list...), s)
	if len(out) > MaxSeen {
		out = out[len(out)-MaxSeen:]
	}
	return out, true
}

// Clear drops the cookie called name from list, as when the app sets it again within the
// berth's host.
func Clear(list []Seen, name string) (out []Seen, changed bool) {
	name = Printable(name, maxName)
	for i, s := range list {
		if s.Name == name {
			return append(append([]Seen{}, list[:i]...), list[i+1:]...), true
		}
	}
	return list, false
}

// LoadSeen is what berth recorded of berth id's wide cookies, none when nothing was.
func LoadSeen(id string) ([]Seen, error) {
	p, err := seenPath(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Seen
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	if len(list) > MaxSeen {
		list = list[len(list)-MaxSeen:]
	}
	return list, nil
}

// SaveSeen records list as berth id's wide cookies, deleting the file when list is empty.
// The caller holds the registry lock with the berth's record present, so nothing is
// written for a berth already removed.
func SaveSeen(id string, list []Seen) error {
	if len(list) == 0 {
		return Forget(id)
	}
	p, err := seenPath(id)
	if err != nil {
		return err
	}
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return safefile.Write(p, append(data, '\n'), 0o644)
}

// Forget deletes what berth recorded of berth id's cookies.
func Forget(id string) error {
	p, err := seenPath(id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
