package cli

import (
	"os"
	"testing"

	"github.com/kodnificent/berth/internal/privilege"
	"github.com/kodnificent/berth/internal/truststore"
)

// proxyChildCommand runs the test binary as berth proxy, so a test can start it as root.
const proxyChildCommand = "__proxy"

// needsRoot skips a test that needs root, or fails it when BERTH_TEST_ROOT=1 says the run
// is meant to be root's, so a CI step whose tests all skipped can't pass.
func needsRoot(t *testing.T) {
	t.Helper()
	if privilege.IsRoot() {
		return
	}
	if os.Getenv("BERTH_TEST_ROOT") == "1" {
		t.Fatal("BERTH_TEST_ROOT=1 but the test is not running as root")
	}
	t.Skip("only root runs this; CI runs it as root with BERTH_TEST_ROOT=1")
}

func TestAsRootTrustAndUntrustAreRefused(t *testing.T) {
	needsRoot(t)
	t.Setenv("BERTH_STATE_DIR", t.TempDir())
	t.Setenv(truststore.Var, t.TempDir())

	for _, cmd := range []string{"trust", "untrust"} {
		if err := runProxy(&Context{Stdout: os.Stderr, Stderr: os.Stderr}, []string{cmd}); err == nil {
			t.Errorf("berth proxy %s as root was not refused", cmd)
		}
	}
}
