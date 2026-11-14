package cli

import (
	"strings"
	"testing"

	"github.com/kodnificent/berth/internal/hook"
	"github.com/kodnificent/berth/internal/volume"
)

func TestFailedCloneGetsNoHintToUseTheURL(t *testing.T) {
	state := []stateJSON{{Name: "db", Status: volume.Failed}}
	if next := statusHints("http://feat.localhost", nil, state, nil, false); len(next) != 0 {
		t.Errorf("next = %q, want no hint to use a berth whose clone failed", next)
	}
}

func TestFailedPostAddGetsNoHintToUseTheURL(t *testing.T) {
	postAdd := &postAddJSON{Status: hook.Failed}
	if next := statusHints("http://feat.localhost", nil, nil, postAdd, false); len(next) != 0 {
		t.Errorf("next = %q, want no hint to use a berth whose post-add failed", next)
	}
}

func TestCloningInsideHookSaysSoWithoutWait(t *testing.T) {
	state := []stateJSON{{Name: "db", Status: volume.Cloning}}
	next := statusHints("http://feat.localhost", nil, state, nil, true)
	if len(next) != 1 || !strings.Contains(next[0], "db is still cloning") || strings.Contains(next[0], "--wait") {
		t.Errorf("next = %q, want one hint that db is still cloning, without --wait", next)
	}
}
