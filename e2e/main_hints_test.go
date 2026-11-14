//go:build e2e

package e2e

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRemoveInMainHintsTheLinkedWorktreeWithTheBerth(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-remove-in-main")
	mustBerth(t, repo, wt, "add")

	res := repo.Berth(t, repo.Dir, "remove", "--json")
	var e hintedError
	if err := json.Unmarshal([]byte(res.Stdout), &e); err != nil || e.Error == nil {
		t.Fatalf("berth remove --json in main: exit %d, want an error envelope: %v\n%s", res.Exit, err, res.Stdout)
	}
	if res.Exit != 1 || e.Error.Code != "main_worktree" {
		t.Fatalf("remove in main: exit %d, code %q, want 1 and main_worktree", res.Exit, e.Error.Code)
	}
	if e.Error.Next == nil {
		t.Fatalf("main_worktree error from remove carries no next hint:\n%s", res.Stdout)
	}
	hints := strings.Join(*e.Error.Next, "\n")
	if !strings.Contains(hints, "berth list") || strings.Contains(hints, "git worktree add") {
		t.Errorf("remove in main: next = %q, want it to point at berth list and not at making a new worktree", *e.Error.Next)
	}
	if berths := list(t, repo); len(berths) != 1 {
		t.Errorf("remove in main changed the berths: %+v", berths)
	}
}
