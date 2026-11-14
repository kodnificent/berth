package cli

import (
	"bytes"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const rootChildUID, rootChildGID = 4321, 8765

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// openDir is a directory every user can enter, since the dropped proxy must reach the
// state directory inside it.
func openDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for d := dir; d != os.TempDir() && d != "/"; d = filepath.Dir(d) {
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The proxy binds as root, gives root up, and only then makes its authority, so every
// file it leaves is the sudo user's and the process serving is that user's.
func TestAsRootTheProxyBindsThenServesAsTheSudoUser(t *testing.T) {
	needsRoot(t)
	root := openDir(t)
	state, cfg := filepath.Join(root, "state"), filepath.Join(root, "config")
	if err := os.Mkdir(state, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(state, rootChildUID, rootChildGID); err != nil {
		t.Fatal(err)
	}
	httpsPort := freeLoopbackPort(t)
	mustWrite(t, filepath.Join(cfg, "berth", "config.toml"), fmt.Sprintf("[proxy]\nhttp = %d\nhttps = %d\n", freeLoopbackPort(t), httpsPort))
	cmd := exec.Command(os.Args[0], proxyChildCommand)
	cmd.Env = []string{"BERTH_STATE_DIR=" + state, "XDG_CONFIG_HOME=" + cfg, "HOME=" + root,
		"SUDO_UID=" + strconv.Itoa(rootChildUID), "SUDO_GID=" + strconv.Itoa(rootChildGID)}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(out.String(), "serving every berth") {
		if time.Now().After(deadline) {
			t.Fatalf("the proxy did not start serving as root:\n%s", out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		fmt.Sprintf("Uid:\t%d\t%d\t%d\t%d", rootChildUID, rootChildUID, rootChildUID, rootChildUID),
		fmt.Sprintf("Gid:\t%d\t%d\t%d\t%d", rootChildGID, rootChildGID, rootChildGID, rootChildGID),
	} {
		if !strings.Contains(string(status), want) {
			t.Errorf("the serving proxy's status lacks %q:\n%s", want, status)
		}
	}
	files := 0
	filepath.WalkDir(state, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			t.Error(err)
			return nil
		}
		info, _ := d.Info()
		st := info.Sys().(*syscall.Stat_t)
		if st.Uid != rootChildUID || st.Gid != rootChildGID {
			t.Errorf("%s is owned by %d:%d, want the sudo user's %d:%d", path, st.Uid, st.Gid, rootChildUID, rootChildGID)
		}
		files++
		return nil
	})
	if files < 4 {
		t.Errorf("the state directory holds %d entries, want the authority's directory and files in it", files)
	}
}
