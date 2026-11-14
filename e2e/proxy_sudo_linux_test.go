//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// sudoAllowed says whether this run may start the proxy with sudo: only when the workflow
// says so with BERTH_E2E_SUDO=1 and sudo needs no password, so a developer's machine
// never runs it and nothing ever prompts.
func sudoAllowed(t *testing.T) {
	t.Helper()
	if os.Getenv("BERTH_E2E_SUDO") != "1" {
		t.Skip("starts berth proxy with sudo; set BERTH_E2E_SUDO=1 where sudo needs no password, as CI does")
	}
	if err := exec.Command("sudo", "-n", "true").Run(); err != nil {
		t.Skipf("BERTH_E2E_SUDO=1 but sudo -n true fails (%v), so sudo would need a password", err)
	}
}

// berthUnder is the berth process sudo started, a descendant of the sudo process.
func berthUnder(sudoPid int) int {
	parents := map[int]int{}
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		fields := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
		if len(fields) > 1 {
			parents[pid], _ = strconv.Atoi(fields[1])
		}
	}
	for pid := range parents {
		cmdline, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if !strings.HasPrefix(string(cmdline), berthBin+"\x00proxy") {
			continue
		}
		for p := parents[pid]; p > 1; p = parents[p] {
			if p == sudoPid {
				return pid
			}
		}
	}
	return 0
}

func TestProxyUnderSudoServesAsTheUserAndLeavesOnlyTheUsersFiles(t *testing.T) {
	sudoAllowed(t)
	repo := NewRepo(t)
	httpPort := 700 + rand.IntN(150)
	httpsPort := httpPort + 150
	writeFile(t, repo.userConfig(), fmt.Sprintf("[proxy]\nhttp = %d\nhttps = %d\n", httpPort, httpsPort))
	proxyRepoConfig(t, repo, "")
	featX, s := addProxiedBerth(t, repo, "feat-x")
	serveFixtureApp(t, s.port(t, "web", 80).HostPort, map[string]http.HandlerFunc{
		"/login": setCookie("wide_session=v; Domain=.myapp.localhost; Path=/"),
	})

	p := &runningProxy{port: httpPort, out: &syncBuffer{}, exited: make(chan struct{})}
	cmd := exec.Command("sudo", append(append([]string{"-n", "env"}, repo.Env()...), berthBin, "proxy")...)
	cmd.Dir = repo.Dir
	cmd.Stdout, cmd.Stderr = p.out, p.out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { cmd.Wait(); close(p.exited) }()
	berthPid := 0
	t.Cleanup(func() {
		if berthPid > 0 {
			syscall.Kill(berthPid, syscall.SIGTERM)
		}
		select {
		case <-p.exited:
		case <-time.After(10 * time.Second):
			t.Errorf("berth proxy under sudo did not stop")
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		if c, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(httpsPort)), 200*time.Millisecond); err == nil {
			c.Close()
			if berthPid = berthUnder(cmd.Process.Pid); berthPid > 0 && strings.Contains(p.out.String(), "serving every berth") {
				break
			}
		}
		if !p.running() || time.Now().After(deadline) {
			t.Fatalf("berth proxy under sudo is not serving on ports %d and %d:\n%s", httpPort, httpsPort, p.out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", berthPid))
	if err != nil {
		t.Fatal(err)
	}
	uid, gid := os.Getuid(), os.Getgid()
	for _, want := range []string{fmt.Sprintf("Uid:\t%d\t%d\t%d\t%d", uid, uid, uid, uid), fmt.Sprintf("Gid:\t%d\t%d\t%d\t%d", gid, gid, gid, gid)} {
		if !strings.Contains(string(status), want) {
			t.Errorf("the serving proxy's status lacks %q:\n%s", want, status)
		}
	}

	tp := &tlsProxy{runningProxy: p, httpsPort: httpsPort, ca: loadAuthority(t, repo.StateDir)}
	tp.mustGetTLS(t, tp.client(nil), "https://feat-x.myapp.localhost/login")
	waitForCookieFinding(t, repo, featX, "wide_session")
	if left := stateFilesNaming(t, repo, "wide_session"); len(left) == 0 {
		t.Fatalf("the proxy recorded no cookie under %s", repo.StateDir)
	}

	filepath.WalkDir(repo.StateDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			t.Error(err)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			t.Error(err)
			return nil
		}
		if st := info.Sys().(*syscall.Stat_t); int(st.Uid) != uid {
			t.Errorf("%s is owned by uid %d, want the invoking user's %d", path, st.Uid, uid)
		}
		return nil
	})
}
