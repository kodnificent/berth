package privilege

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootWithoutASudoUserIsRefused(t *testing.T) {
	for _, env := range []map[string]string{{}, {"SUDO_UID": "0", "SUDO_GID": "0"}, {"SUDO_UID": "x", "SUDO_GID": "20"}, {"SUDO_UID": "501"}} {
		if _, _, err := sudoUser(func(k string) string { return env[k] }); err == nil {
			t.Errorf("sudo variables %v name a user to become", env)
		}
	}
}

func TestAStateDirectoryOfAnotherUserIsRefused(t *testing.T) {
	dir := t.TempDir()

	err := stateOwner(dir, 501, func(os.FileInfo) (int, bool) { return 502, true })

	if err == nil {
		t.Errorf("a state directory owned by 502 was accepted for 501")
	}
}

func TestASetuidRootBerthIsRefused(t *testing.T) {
	if err := rootByItself(501); err == nil {
		t.Errorf("effective root over real uid 501 was accepted")
	}
}

func TestASudoUserIsBecomeAsSudoNamesIt(t *testing.T) {
	env := map[string]string{"SUDO_UID": "501", "SUDO_GID": "20"}

	uid, gid, err := sudoUser(func(k string) string { return env[k] })

	if uid != 501 || gid != 20 || err != nil {
		t.Errorf("SUDO_UID=501 SUDO_GID=20 give %d, %d, %v; want 501, 20 and no error", uid, gid, err)
	}
}

func TestAStateDirectoryOfTheSudoUserIsAccepted(t *testing.T) {
	err := stateOwner(t.TempDir(), 501, func(os.FileInfo) (int, bool) { return 501, true })

	if err != nil {
		t.Errorf("a state directory owned by 501 was refused for 501: %v", err)
	}
}

// A state directory that isn't there yet is made after the drop, by the user, so it can
// only be the user's.
func TestAStateDirectoryNotYetMadeIsAccepted(t *testing.T) {
	err := stateOwner(filepath.Join(t.TempDir(), "berth"), 501, func(os.FileInfo) (int, bool) { return 0, true })

	if err != nil {
		t.Errorf("a state directory not yet made was refused: %v", err)
	}
}

func TestRootThroughAndThroughIsAccepted(t *testing.T) {
	if err := rootByItself(0); err != nil {
		t.Errorf("real uid 0 was refused: %v", err)
	}
}
