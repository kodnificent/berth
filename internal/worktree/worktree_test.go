package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInspect(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	present := filepath.Join(root, "present")
	if err := os.MkdirAll(present, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(present, ".git"), []byte("gitdir: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(root, "missing")

	cases := []struct {
		name   string
		gitdir string
		locked bool
		want   Linked
	}{
		{"present", filepath.Join(present, ".git"), false, Linked{Exists: true, Path: present}},
		{"present, relative gitdir", "../../../present/.git", false, Linked{Exists: true, Path: present}},
		{"missing and unlocked", filepath.Join(missing, ".git"), false, Linked{Exists: true, Path: missing, Prunable: true}},
		{"missing and locked", filepath.Join(missing, ".git"), true, Linked{Exists: true, Path: missing, Locked: true}},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			admin := filepath.Join(root, "repo", "worktrees", string(rune('a'+i)))
			if err := os.MkdirAll(admin, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(admin, "gitdir"), []byte(c.gitdir+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if c.locked {
				if err := os.WriteFile(filepath.Join(admin, "locked"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := Inspect(admin); got != c.want {
				t.Errorf("Inspect = %+v, want %+v", got, c.want)
			}
		})
	}

	if got := Inspect(filepath.Join(root, "repo", "worktrees", "removed")); got.Exists {
		t.Errorf("Inspect of a removed admin dir = %+v, want Exists false", got)
	}
}
