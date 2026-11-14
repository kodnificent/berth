//go:build e2e

package e2e

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestHumanOutputAddWaitJSONPrintsOnlyTheErrorWhenACloneFails(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	wt := repo.AddWorktree(t, "feat-json-fail")
	add := exec.Command(berthBin, "add", "--wait", "--json")
	add.Dir, add.Env = wt, append(repo.Env(), cloneDelay+"=120s")
	var stdout strings.Builder
	add.Stdout = &stdout
	if err := add.Start(); err != nil {
		t.Fatal(err)
	}
	var pid int
	waitFor(t, 30*time.Second, "the clone process to start", func() bool {
		var s stateStatus
		if json.Unmarshal([]byte(repo.Berth(t, wt, "status", "--json").Stdout), &s) != nil {
			return false
		}
		for _, e := range s.State {
			if e.Name == "cache" && e.PID > 0 && e.Status == "cloning" {
				pid = e.PID
			}
		}
		return pid > 0
	})
	if err := killNow(pid); err != nil {
		t.Fatalf("kill -9 %d: %v", pid, err)
	}
	_ = add.Wait()
	if code := add.ProcessState.ExitCode(); code != 4 {
		t.Errorf("berth add --wait --json exited %d after its clone failed, want 4", code)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(stdout.String()), &body); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout.String())
	}
	if _, ok := body["error"]; !ok || len(body) != 1 {
		t.Errorf("stdout holds more than the error envelope:\n%s", stdout.String())
	}
}
