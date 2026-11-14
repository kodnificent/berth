//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const pluginDir = "../plugins/claude"

// Windows gets more because the script's process starts under Git for Windows' sh don't
// fit in what berth's 0.7 s leaves of 1 s. Record 0048 has the numbers.
func sessionStartBudget() time.Duration {
	if runtime.GOOS == "windows" {
		return 2 * time.Second
	}
	return time.Second
}

type sessionStart struct {
	Context string
	Emitted bool
	Exit    int
	Took    time.Duration
	Stdout  string
	Stderr  string
}

func (s sessionStart) contextLines() []string {
	var out []string
	for _, l := range strings.Split(s.Context, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// runSessionStart runs the plugin's SessionStart hook as Claude Code does: in the
// project directory, with the session's JSON on stdin. The time taken runs until the
// hook's stdout closes, since Claude Code cannot use the context before then.
func runSessionStart(t *testing.T, env []string, dir string) sessionStart {
	t.Helper()
	hook, err := filepath.Abs(filepath.Join(pluginDir, "hooks", "session-start"))
	if err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.Abs(pluginDir)
	input, _ := json.Marshal(map[string]string{
		"session_id":      "berth-e2e",
		"transcript_path": filepath.Join(t.TempDir(), "transcript.jsonl"),
		"cwd":             dir,
		"hook_event_name": "SessionStart",
		"source":          "startup",
	})
	cmd := scriptCommand(hook)
	cmd.Dir = dir
	cmd.Env = append(env, "CLAUDE_PROJECT_DIR="+dir, "CLAUDE_PLUGIN_ROOT="+root)
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 5 * time.Second
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %s: %v", hook, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var runErr error
	select {
	case runErr = <-done:
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		<-done
		t.Fatalf("the SessionStart hook had not returned after 30s\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	res := sessionStart{Took: time.Since(start), Stdout: stdout.String(), Stderr: stderr.String()}
	if ee, ok := runErr.(*exec.ExitError); ok {
		res.Exit = ee.ExitCode()
	} else if runErr != nil && !strings.Contains(runErr.Error(), "WaitDelay") {
		t.Fatalf("running %s: %v", hook, runErr)
	}
	if strings.TrimSpace(res.Stdout) == "" {
		return res
	}
	var out struct {
		HookSpecificOutput *struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil || out.HookSpecificOutput == nil {
		t.Fatalf("the SessionStart hook printed something other than a hookSpecificOutput object (%v):\n%s", err, res.Stdout)
	}
	if out.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Fatalf("hookEventName = %q, want SessionStart", out.HookSpecificOutput.HookEventName)
	}
	res.Emitted = true
	res.Context = out.HookSpecificOutput.AdditionalContext
	return res
}

// pathWithoutBerth is PATH with every directory holding a berth executable dropped, so
// the developer's own install never stands in for the one under test.
func pathWithoutBerth() string {
	var keep []string
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if fi, err := os.Stat(filepath.Join(d, "berth")); err == nil && !fi.IsDir() {
			continue
		}
		keep = append(keep, d)
	}
	return strings.Join(keep, string(os.PathListSeparator))
}

func hookEnv(repo *Repo, pathPrefix ...string) []string {
	path := strings.Join(append(pathPrefix, pathWithoutBerth()), string(os.PathListSeparator))
	return append(repo.Env(), "PATH="+path)
}

func withBerth(repo *Repo) []string { return hookEnv(repo, filepath.Dir(berthBin)) }

func requireOK(t *testing.T, s sessionStart) {
	t.Helper()
	if s.Exit != 0 {
		t.Fatalf("the SessionStart hook exited %d, want 0\nstdout:\n%s\nstderr:\n%s", s.Exit, s.Stdout, s.Stderr)
	}
}

func TestSessionStartGivesTheBerthsURLAndEveryPort(t *testing.T) {
	repo := NewRepo(t)
	writeFile(t, repo.privateConfig(), `[berth]
url = "http://{host}:{port.web}"

[compose.ports]
"web:80" = "assign"
"db:3306" = "assign"

[ports]
vite = { env = "VITE_PORT" }
`)
	wt := repo.AddWorktree(t, "feat-session-ports")
	mustBerth(t, repo, wt, "add")
	s := portsStatusOf(t, repo, wt)
	if s.URL == "" || len(s.hostPorts()) < 3 {
		t.Fatalf("status gives url %q and host ports %v, want a URL and three assigned ports", s.URL, s.hostPorts())
	}

	res := runSessionStart(t, withBerth(repo), wt)
	requireOK(t, res)
	if !strings.Contains(res.Context, s.URL) {
		t.Errorf("the context does not contain the berth's URL %s:\n%s", s.URL, res.Context)
	}
	for _, p := range s.hostPorts() {
		if !regexp.MustCompile(`\b` + strconv.Itoa(p) + `\b`).MatchString(res.Context) {
			t.Errorf("the context does not contain assigned port %d:\n%s", p, res.Context)
		}
	}
}

func TestSessionStartWithNoBerthSaysSoAndGivesAdd(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-session-none")
	res := runSessionStart(t, withBerth(repo), wt)
	requireOK(t, res)
	lower := strings.ToLower(res.Context)
	if !regexp.MustCompile(`\bno berth\b`).MatchString(lower) {
		t.Errorf("the context does not say this worktree has no berth:\n%s", res.Context)
	}
	if !strings.Contains(res.Context, "berth add") {
		t.Errorf("the context does not give berth add:\n%s", res.Context)
	}
}

func TestSessionStartWithNothingToReportAddsAtMostOneLine(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T) ([]string, string)
	}{
		{"main worktree", func(t *testing.T) ([]string, string) {
			repo := NewRepo(t)
			return withBerth(repo), repo.Dir
		}},
		{"outside a git repository", func(t *testing.T) ([]string, string) {
			repo := NewRepo(t)
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			return withBerth(repo), dir
		}},
		{"berth not installed", func(t *testing.T) ([]string, string) {
			repo := NewRepo(t)
			wt := repo.AddWorktree(t, "feat-session-no-binary")
			mustBerth(t, repo, wt, "add")
			return hookEnv(repo), wt
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env, dir := c.setup(t)
			res := runSessionStart(t, env, dir)
			requireOK(t, res)
			if n := len(res.contextLines()); n > 1 {
				t.Errorf("the context has %d lines, want at most one:\n%s", n, res.Context)
			}
		})
	}
}

func TestSessionStartReturnsWithinASecondWhileCloning(t *testing.T) {
	repo, wt := cloningBerth(t, "feat-session-clone")
	res := runSessionStart(t, withBerth(repo), wt)
	requireOK(t, res)
	if res.Took > sessionStartBudget() {
		t.Errorf("the SessionStart hook took %s while a clone ran, want at most %s", res.Took, sessionStartBudget())
	}
	if e := stateOf(t, repo, wt).volume(t, "cache"); e.Status != "cloning" {
		t.Fatalf("cache is %q after the hook ran, want still cloning, so the timing proved nothing", e.Status)
	}
}

// The hook keeps its own deadline however long berth takes.
func TestSessionStartSaysBerthIsBusyPastItsDeadline(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, repo *Repo) []string
	}{
		{"registry lock held", func(t *testing.T, repo *Repo) []string {
			f, err := os.OpenFile(filepath.Join(repo.StateDir, "lock"), os.O_CREATE|os.O_RDWR, 0o644)
			if err != nil {
				t.Fatal(err)
			}
			if err := lockLikeBerth(f); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { f.Close() })
			return withBerth(repo)
		}},
		{"slow berth", func(t *testing.T, repo *Repo) []string {
			shim := t.TempDir()
			script := fmt.Sprintf("#!/bin/sh\nsleep 10\nexec %s \"$@\"\n", shellQuote(berthBin))
			if err := os.WriteFile(filepath.Join(shim, "berth"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := makeExecutable(filepath.Join(shim, "berth")); err != nil {
				t.Fatal(err)
			}
			return hookEnv(repo, shim)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := NewRepo(t)
			wt := repo.AddWorktree(t, "feat-session-busy")
			mustBerth(t, repo, wt, "add")
			res := runSessionStart(t, c.setup(t, repo), wt)
			requireOK(t, res)
			if res.Took > sessionStartBudget() {
				t.Errorf("the SessionStart hook took %s while berth was busy, want at most %s", res.Took, sessionStartBudget())
			}
			lines := res.contextLines()
			if len(lines) != 1 || !strings.Contains(strings.ToLower(lines[0]), "busy") {
				t.Errorf("the context is %q, want one line saying berth is busy", res.Context)
			}
		})
	}
}

// registryRecords reads every berth record. A key naming the berth's last use is
// dropped because the hook running counts as use and may move it.
func registryRecords(t *testing.T, repo *Repo) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(repo.StateDir, "berths", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	lastUse := regexp.MustCompile(`(?i)(^|_)(last_?)?used?(_|$)`)
	out := map[string]string{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var rec map[string]any
		if err := json.Unmarshal(data, &rec); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		for k := range rec {
			if lastUse.MatchString(k) {
				delete(rec, k)
			}
		}
		norm, _ := json.Marshal(rec)
		out[filepath.Base(p)] = string(norm)
	}
	return out
}

func berthIDs(t *testing.T, repo *Repo) []string {
	t.Helper()
	var ids []string
	for _, b := range list(t, repo) {
		ids = append(ids, b.ID)
	}
	sort.Strings(ids)
	return ids
}

func TestSessionStartChangesNoBerth(t *testing.T) {
	repo := NewRepo(t)
	withOne := repo.AddWorktree(t, "feat-session-has")
	mustBerth(t, repo, withOne, "add")
	without := repo.AddWorktree(t, "feat-session-hasnt")
	records, ids := registryRecords(t, repo), berthIDs(t, repo)

	for _, dir := range []string{withOne, without, repo.Dir} {
		requireOK(t, runSessionStart(t, withBerth(repo), dir))
	}

	if got := berthIDs(t, repo); strings.Join(got, ",") != strings.Join(ids, ",") {
		t.Errorf("berths after the hook ran = %v, want %v", got, ids)
	}
	after := registryRecords(t, repo)
	if len(after) != len(records) {
		t.Errorf("registry holds %d records after the hook ran, want %d", len(after), len(records))
	}
	for name, before := range records {
		if after[name] != before {
			t.Errorf("record %s changed beyond its last use:\nbefore %s\nafter  %s", name, before, after[name])
		}
	}
}

// The wiring Claude Code needs to run the hook at all.
func TestPluginWiresSessionStartToTheHook(t *testing.T) {
	var manifest map[string]any
	data, err := os.ReadFile(filepath.Join(pluginDir, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatalf("reading plugin.json: %v", err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("plugin.json is not JSON: %v\n%s", err, data)
	}
	if name, _ := manifest["name"].(string); name == "" {
		t.Errorf("plugin.json has no name:\n%s", data)
	}

	var hooks struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	data, err = os.ReadFile(filepath.Join(pluginDir, "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("reading hooks.json: %v", err)
	}
	if err := json.Unmarshal(data, &hooks); err != nil {
		t.Fatalf("hooks.json is not JSON: %v\n%s", err, data)
	}
	wired := false
	for _, group := range hooks.Hooks["SessionStart"] {
		for _, h := range group.Hooks {
			wired = wired || (h.Type == "command" && strings.Contains(h.Command, "hooks/session-start"))
		}
	}
	if !wired {
		t.Errorf("hooks.json runs no command hooks/session-start on SessionStart:\n%s", data)
	}
}
