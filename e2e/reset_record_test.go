//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestResetEmptiesAVolumeMainNoLongerDeclares(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	wt, vol := clonedBerth(t, repo, "feat-undeclared")
	writeBerthData(t, vol)
	composeFile := filepath.Join(repo.Dir, "compose.yml")
	main := readString(t, composeFile)
	trimmed := strings.Replace(strings.Replace(main, "    volumes:\n      - cache:/cache\n", "", 1), "  cache:\n", "", 1)
	if trimmed == main {
		t.Fatalf("could not take the cache volume out of main's compose file:\n%s", main)
	}
	writeFile(t, composeFile, trimmed)

	dry := mustBerth(t, repo, wt, "reset", "--dry-run", "--json")
	var plan struct {
		Discarded []struct {
			Volume string `json:"volume"`
		} `json:"discarded"`
	}
	if err := json.Unmarshal([]byte(dry.Stdout), &plan); err != nil {
		t.Fatalf("berth reset --dry-run --json: %v\n%s", err, dry.Stdout)
	}
	if len(plan.Discarded) != 1 || plan.Discarded[0].Volume != vol {
		t.Errorf("reset --dry-run lists %+v as discarded, want only %s:\n%s", plan.Discarded, vol, dry.Stdout)
	}

	mustBerth(t, repo, wt, "reset")
	mustBerth(t, repo, wt, "status", "--wait")

	if e := stateOf(t, repo, wt).volume(t, "cache"); e.Status != "ready" || e.Volume != vol {
		t.Fatalf("after reset with cache gone from main's compose file, cache is %q in %q, want ready in %q: %+v", e.Status, e.Volume, vol, e)
	}
	if left := strings.TrimSpace(inVolume(t, vol, "ls -A /v", true)); left != "" {
		t.Errorf("main's compose file no longer has cache, but the berth's volume still holds:\n%s", left)
	}
}

// A volume berth creates for a clone is in the record before anything slow runs, so
// killing add while it measures the copy never leaves a volume berth doesn't know.
func TestKilledFirstCloneKeepsTheVolumeRecorded(t *testing.T) {
	repo := NewRepo(t)
	src := fillCache(t, repo)
	want := checksum(t, src)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	wt := repo.AddWorktree(t, "feat-measure")

	realDocker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	shim := t.TempDir()
	marker := filepath.Join(shim, "blocked")
	writeFile(t, filepath.Join(shim, "docker"), `#!/bin/sh
if [ "$1" = run ]; then
  for a in "$@"; do
    if [ "$a" = --rm ]; then echo $$ > `+shellQuote(marker)+`; exec sleep 600; fi
  done
fi
exec `+shellQuote(realDocker)+` "$@"
`)
	if err := makeExecutable(filepath.Join(shim, "docker")); err != nil {
		t.Fatal(err)
	}
	env := slices.DeleteFunc(repo.Env(), func(kv string) bool { return strings.HasPrefix(kv, "PATH=") })
	add := exec.Command(berthBin, "add")
	add.Dir, add.Env = wt, append(env, "PATH="+shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	var addOutput bytes.Buffer
	add.Stdout, add.Stderr = &addOutput, &addOutput
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("berth add printed:\n%s", addOutput.String())
		}
	})
	if err := add.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if data, err := os.ReadFile(marker); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				killScriptPid(pid)
			}
		}
	})
	waitFor(t, 60*time.Second, "add to reach the measuring helper", func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
	if err := add.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = add.Wait()

	vol := stateOf(t, repo, wt).ComposeProject + "_cache"
	if !dockerHas("volume", vol) {
		t.Fatalf("add was killed before creating %s, so the test never reached the point it checks", vol)
	}
	if !slices.Contains(removeJSON(t, repo, wt, "--dry-run").Removed, removedItem{"volume", vol}) {
		t.Errorf("after add was killed, remove --dry-run doesn't list the volume %s berth created", vol)
	}

	mustBerth(t, repo, wt, "reset")
	mustBerth(t, repo, wt, "status", "--wait")
	if e := stateOf(t, repo, wt).volume(t, "cache"); e.Status != "ready" || e.Volume != vol {
		t.Fatalf("after reset, cache is %q in %q, want ready in %s: %+v", e.Status, e.Volume, vol, e)
	}
	if got := checksum(t, vol); got != want {
		t.Errorf("after reset the berth's cache differs from main's:\nmain:\n%s\nberth:\n%s", want, got)
	}
}
