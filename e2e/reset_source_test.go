//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

func TestResetEmptiesAVolumeMainNoLongerHas(t *testing.T) {
	repo := NewRepo(t)
	src := fillCache(t, repo)
	wt, vol := clonedBerth(t, repo, "feat-gone")
	writeBerthData(t, vol)
	mustDocker(t, "volume", "rm", src)

	mustBerth(t, repo, wt, "reset")
	mustBerth(t, repo, wt, "status", "--wait")

	if e := stateOf(t, repo, wt).volume(t, "cache"); e.Status != "ready" || e.Volume != vol {
		t.Fatalf("after reset with main's cache gone, cache is %q in volume %q, want ready in %q: %+v", e.Status, e.Volume, vol, e)
	}
	if left := strings.TrimSpace(inVolume(t, vol, "ls -A /v", true)); left != "" {
		t.Errorf("main has no cache volume, but the berth's still holds:\n%s", left)
	}
	if dockerHas("volume", src) {
		t.Errorf("reset created main's volume %s, which main didn't have", src)
	}
}
