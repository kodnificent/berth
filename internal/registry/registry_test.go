package registry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBerthNeverOwnsMainsDatabase(t *testing.T) {
	m := &HostDB{Prefix: "app_feat__", Databases: []Database{
		{Source: "app_central", Target: "app_feat__central", Strategy: "fresh"},
		{Source: "shop", Target: "shop", Strategy: "none"},
	}}
	for name, want := range map[string]bool{
		"app_feat__central": true,
		"app_feat__42":      true,
		"app_central":       false,
		"shop":              false,
		"app_feat_x__1":     false,
	} {
		if got := m.Owns(name); got != want {
			t.Errorf("Owns(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestPeekTellsAnUnreadableRecordFromAGoneOne(t *testing.T) {
	state := t.TempDir()
	t.Setenv("BERTH_STATE_DIR", state)
	if err := os.MkdirAll(filepath.Join(state, "berths"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "berths", "a1.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	live, unreadable, err := Peek()

	if err != nil || len(live) != 0 || unreadable["a1"] == nil || len(unreadable) != 1 {
		t.Errorf("live %v, unreadable %v, error %v; want a1 alone reported unreadable", live, unreadable, err)
	}
}
