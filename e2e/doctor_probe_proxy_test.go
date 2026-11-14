//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"testing"
)

// The proxy environment is read once per process, so only a fresh berth process shows
// whether the probe ignores it.
func TestDoctorProbeIgnoresProxyVariables(t *testing.T) {
	repo := NewRepo(t)
	b := newProbedBerth(t, repo, "")
	app := serveFixtureApp(t, b.Port, nil)

	dead := "http://127.0.0.1:9"
	cmd := exec.Command(berthBin, "doctor", "--json", "--probe")
	cmd.Dir = b.Dir
	cmd.Env = append(repo.Env(),
		"HTTP_PROXY="+dead, "HTTPS_PROXY="+dead, "ALL_PROXY="+dead,
		"http_proxy="+dead, "https_proxy="+dead, "all_proxy="+dead,
		"NO_PROXY=", "no_proxy=",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	_ = cmd.Run()

	var d probeResult
	if err := json.Unmarshal(stdout.Bytes(), &d); err != nil || d.Findings == nil {
		t.Fatalf("berth doctor --json --probe: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	d.Raw = stdout.String()
	app.expectRequest(t, b.hostAndPort(), "/")
	if fs := d.with("probe"); len(fs) != 0 {
		t.Errorf("the probe went through the proxy variables instead of the loopback address: %+v", fs)
	}
}
