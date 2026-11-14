//go:build !windows

package privilege

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// The drop changes the whole process, so it runs in a child of the test binary. Its uid
// and gid differ, so a swap of the two shows.
const childUID, childGID = 4321, 8765

func TestMain(m *testing.M) {
	if os.Getenv("BERTH_PRIVILEGE_CHILD") == "saved-root" {
		fmt.Printf("setup=%v verify=%v\n", keepSavedRoot(childUID, childGID), verifyDropped(childUID, childGID))
		os.Exit(0)
	}
	if os.Getenv("BERTH_PRIVILEGE_CHILD") == "1" {
		dropped, err := Drop()
		fmt.Printf("dropped=%v err=%v uid=%d euid=%d gid=%d egid=%d setuid0=%v own=%v roots=%v\n", dropped, err, os.Getuid(), os.Geteuid(), os.Getgid(), os.Getegid(),
			syscall.Setuid(0) == nil, StateOwned(os.Getenv("BERTH_CHILD_OWN"), os.Getuid()) == nil, StateOwned(os.Getenv("BERTH_CHILD_ROOTS"), os.Getuid()) == nil)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// needsRoot skips a test that needs root, or fails it when BERTH_TEST_ROOT=1 says the run
// is meant to be root's, so a CI step whose tests all skipped can't pass.
func needsRoot(t *testing.T) {
	t.Helper()
	if IsRoot() {
		return
	}
	if os.Getenv("BERTH_TEST_ROOT") == "1" {
		t.Fatal("BERTH_TEST_ROOT=1 but the test is not running as root")
	}
	t.Skip("only root runs this; CI runs it as root with BERTH_TEST_ROOT=1")
}

func child(t *testing.T, env ...string) string {
	t.Helper()
	return childWith(t, "1", env...)
}

func childWith(t *testing.T, mode string, env ...string) string {
	t.Helper()
	// The dropped child must reach both directories to tell their owners apart.
	base := t.TempDir()
	for d := base; d != os.TempDir() && d != "/"; d = filepath.Dir(d) {
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	own, roots := filepath.Join(base, "own"), filepath.Join(base, "roots")
	for _, d := range []string{own, roots} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chown(own, childUID, childGID); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0])
	cmd.Env = append([]string{"BERTH_PRIVILEGE_CHILD=" + mode, "BERTH_CHILD_OWN=" + own, "BERTH_CHILD_ROOTS=" + roots}, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestAsRootTheProxyBecomesTheSudoUserForGood(t *testing.T) {
	needsRoot(t)

	got := child(t, fmt.Sprintf("SUDO_UID=%d", childUID), fmt.Sprintf("SUDO_GID=%d", childGID))

	want := fmt.Sprintf("dropped=true err=<nil> uid=%d euid=%d gid=%d egid=%d setuid0=false own=true roots=false", childUID, childUID, childGID, childGID)
	if got != want {
		t.Errorf("after the drop: %s\nwant %s", got, want)
	}
}

func TestAsRootWithoutASudoUserTheProxyStaysPutAndFails(t *testing.T) {
	needsRoot(t)

	got := child(t)

	if !strings.HasPrefix(got, "dropped=false err=berth proxy is running as root") {
		t.Errorf("root with no SUDO_UID: %s, want a refusal", got)
	}
}

func TestAsRootADropThatLeavesRootRecoverableIsCaught(t *testing.T) {
	needsRoot(t)
	if runtime.GOOS != "linux" {
		t.Skip("setresuid, which keeps a saved uid, is Linux's")
	}

	got := childWith(t, "saved-root")

	if !strings.HasPrefix(got, "setup=<nil> verify=") || strings.HasSuffix(got, "verify=<nil>") {
		t.Errorf("with root kept as the saved uid: %s; want the check to fail", got)
	}
}

func TestAsRootIdsThatWereNeverChangedAreCaught(t *testing.T) {
	needsRoot(t)

	if err := verifyDropped(childUID, childGID); err == nil {
		t.Errorf("the check passed for a process that is still root")
	}
}
