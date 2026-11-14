//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A compose project with no name of its own is named by docker compose after the folder
// of its first file. Berth has to call main's project by that name to find main's volumes.
func TestCloneFindsMainsVolumeWhenComposeFileIsInASubfolder(t *testing.T) {
	repo := NewRepo(t)
	sub := repo.Project
	if err := os.Mkdir(filepath.Join(repo.Dir, sub), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"compose.yml", "web"} {
		if err := os.Rename(filepath.Join(repo.Dir, f), filepath.Join(repo.Dir, sub, f)); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(repo.Dir, sub, "compose.yml")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, file, strings.Replace(string(data), "name: "+repo.Project+"\n", "", 1))
	env, err := os.ReadFile(filepath.Join(repo.Dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo.Dir, ".env"), string(env)+"COMPOSE_FILE="+sub+"/compose.yml\n")
	repo.Git(t, "add", "-A")
	repo.Git(t, "commit", "-q", "-m", "compose file in a subfolder")

	if got := composeConfig(t, repo, repo.Dir)["name"]; got != repo.Project {
		t.Fatalf("docker compose names main's project %v, want %s after the subfolder", got, repo.Project)
	}
	src := fillCache(t, repo)
	want := checksum(t, src)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	wt := repo.AddWorktree(t, "feat-sub")
	mustBerth(t, repo, wt, "add", "--wait")

	e := stateOf(t, repo, wt).volume(t, "cache")
	if e.Status != "ready" {
		t.Fatalf("cache is %q after add --wait, want ready: %+v", e.Status, e)
	}
	if got := checksum(t, e.Volume); got != want {
		t.Errorf("berth's cache differs from main's:\nmain:\n%s\nberth:\n%s", want, got)
	}
	cfg := composeConfig(t, repo, wt)
	if name, _ := cfg["name"].(string); !strings.HasPrefix(name, repo.Project+"-") {
		t.Errorf("the berth's project is %q, want it built from main's %s", name, repo.Project)
	}
	vols, _ := cfg["volumes"].(map[string]any)
	if cache, _ := vols["cache"].(map[string]any); cache["name"] != e.Volume {
		t.Errorf("docker compose config in the berth names cache %v, want the cloned volume %s", cache["name"], e.Volume)
	}
}
