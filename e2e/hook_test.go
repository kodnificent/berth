//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type postAddStatus struct {
	Command  string `json:"command"`
	Status   string `json:"status"`
	ExitCode *int   `json:"exit_code"`
	Output   string `json:"output"`
	Error    string `json:"error"`
}

type hookStatus struct {
	ID      string         `json:"id"`
	PostAdd *postAddStatus `json:"post_add"`
	State   []stateEntry   `json:"state"`
}

func hookStatusOf(t *testing.T, repo *Repo, dir string) hookStatus {
	t.Helper()
	res := mustBerth(t, repo, dir, "status", "--json")
	var s hookStatus
	if err := json.Unmarshal([]byte(res.Stdout), &s); err != nil {
		t.Fatalf("berth status --json: %v\n%s", err, res.Stdout)
	}
	return s
}

// waitPostAdd waits for the berth's post-add to finish and returns how it ended.
func waitPostAdd(t *testing.T, repo *Repo, dir string) postAddStatus {
	t.Helper()
	var last hookStatus
	waitFor(t, 60*time.Second, "post-add to finish", func() bool {
		last = hookStatusOf(t, repo, dir)
		return last.PostAdd != nil && (last.PostAdd.Status == "ok" || last.PostAdd.Status == "failed")
	})
	return *last.PostAdd
}

// gitWith runs git in dir with extra environment on top of the repo's own, and returns
// its result and how long it took. Its output goes through pipes, as it does for any
// tool that reads git's output, so it returns only when every process holding them has
// closed them.
func gitWith(t *testing.T, repo *Repo, env []string, dir string, args ...string) (Result, time.Duration) {
	t.Helper()
	saved := repo.env
	repo.env = append(append([]string{}, repo.env...), env...)
	defer func() { repo.env = saved }()
	start := time.Now()
	res, err := repo.run(dir, "git", args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return res, time.Since(start)
}

func hookWorktree(t *testing.T, repo *Repo, env []string, name string) string {
	t.Helper()
	path := filepath.Join(filepath.Dir(repo.Dir), "worktrees", name)
	res, _ := gitWith(t, repo, env, repo.Dir, "worktree", "add", "-q", "-b", name, path)
	if res.Exit != 0 {
		t.Fatalf("git worktree add: exit %d\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	return path
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// jsonText is s as it appears inside a JSON string, such as a Windows path with its
// backslashes doubled.
func jsonText(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

func lines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// After berth hook install, git worktree add creates a berth for the new
// worktree with no other command.
func TestHookCreatesBerthOnWorktreeAdd(t *testing.T) {
	repo := NewRepo(t)
	mustBerth(t, repo, repo.Dir, "hook", "install")

	wt := hookWorktree(t, repo, nil, "feat-hook")
	got := berthsAt(list(t, repo), wt)
	if len(got) != 1 {
		t.Fatalf("after git worktree add with the hook installed, berth list has %d berths for %s, want 1", len(got), wt)
	}
	if s, _ := status(t, repo, wt); s.ID != got[0].ID {
		t.Errorf("berth status in the new worktree reports %s, want %s", s.ID, got[0].ID)
	}
	if _, err := os.Stat(filepath.Join(wt, "compose.berth.yml")); err != nil {
		t.Errorf("the hook's berth has no compose.berth.yml: %v", err)
	}

	again := mustBerth(t, repo, repo.Dir, "hook", "install")
	if again.Exit != 0 {
		t.Errorf("installing the hook a second time: exit %d\n%s", again.Exit, again.Stderr)
	}
}

// A normal git checkout or git switch in any worktree doesn't run add.
func TestCheckoutAndSwitchDoNotAdd(t *testing.T) {
	repo := NewRepo(t)
	runs := filepath.Join(t.TempDir(), "post-add-runs")
	writeFile(t, repo.privateConfig(), fmt.Sprintf("[hooks]\npost-add = %s\n", tomlString("echo run >> "+shellQuote(runs))))
	plain := repo.AddWorktree(t, "feat-before-hook")
	mustBerth(t, repo, repo.Dir, "hook", "install")
	berthed := hookWorktree(t, repo, nil, "feat-berthed")
	waitPostAdd(t, repo, berthed)

	repo.Git(t, "-C", plain, "switch", "-q", "-c", "feat-switched")
	repo.Git(t, "-C", plain, "checkout", "-q", "-b", "feat-checked-out")
	repo.Git(t, "-C", plain, "checkout", "-q", "feat-before-hook")
	repo.Git(t, "switch", "-q", "-c", "main-topic")
	repo.Git(t, "checkout", "-q", "main")
	repo.Git(t, "-C", berthed, "switch", "-q", "-c", "feat-berthed-2")
	repo.Git(t, "-C", berthed, "checkout", "-q", "feat-berthed")

	if got := berthsAt(list(t, repo), plain); len(got) != 0 {
		t.Errorf("git switch and git checkout in a worktree without a berth created %d berths, want none", len(got))
	}
	if got := list(t, repo); len(got) != 1 {
		t.Errorf("after checkouts and switches there are %d berths, want only the one git worktree add made", len(got))
	}
	if got := lines(t, runs); len(got) != 1 {
		t.Errorf("post-add ran %d times, want once, for git worktree add only", len(got))
	}
}

// Running git worktree add and then berth add gives one berth, as if add
// had run once.
func TestHookThenAddGivesOneBerth(t *testing.T) {
	repo := NewRepo(t)
	runs := filepath.Join(t.TempDir(), "post-add-runs")
	writeFile(t, repo.privateConfig(), fmt.Sprintf("[hooks]\npost-add = %s\n", tomlString("sleep 2; echo run >> "+shellQuote(runs))))
	mustBerth(t, repo, repo.Dir, "hook", "install")
	wt := hookWorktree(t, repo, nil, "feat-twice")
	hooked, _ := status(t, repo, wt)

	// Runs while the hook's post-add is still sleeping, then again once it is done.
	mustBerth(t, repo, wt, "add")
	if pa := waitPostAdd(t, repo, wt); pa.Status != "ok" {
		t.Fatalf("post-add ended %q, want ok: %+v", pa.Status, pa)
	}
	once, before := status(t, repo, wt)
	if once.ID != hooked.ID {
		t.Errorf("berth add after the hook reports berth %s, want the hook's %s", once.ID, hooked.ID)
	}
	mustBerth(t, repo, wt, "add")
	time.Sleep(3 * time.Second)
	if _, after := status(t, repo, wt); after != before {
		t.Errorf("berth status --json changed after another berth add:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if got := berthsAt(list(t, repo), wt); len(got) != 1 {
		t.Errorf("berth list has %d berths for the worktree, want 1", len(got))
	}
	if got := lines(t, runs); len(got) != 1 {
		t.Errorf("post-add ran %d times across the hook and two berth adds, want once", len(got))
	}
}

// git worktree add returns while state is still cloning; a caller reading
// git's output isn't blocked until the clone finishes.
func TestWorktreeAddReturnsWhileCloning(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n[hooks]\npost-add = \"sleep 1\"\n")
	mustBerth(t, repo, repo.Dir, "hook", "install")

	path := filepath.Join(filepath.Dir(repo.Dir), "worktrees", "feat-bg-hook")
	res, took := gitWith(t, repo, []string{cloneDelay + "=15s"}, repo.Dir, "worktree", "add", "-b", "feat-bg-hook", path)
	if res.Exit != 0 {
		t.Fatalf("git worktree add: exit %d\n%s", res.Exit, res.Stderr)
	}
	if took > 10*time.Second {
		t.Fatalf("git worktree add took %s with a 15s clone, want it to return while the clone runs", took)
	}
	if e := stateOf(t, repo, path).volume(t, "cache"); e.Status != "cloning" {
		t.Fatalf("right after git worktree add, cache is %q, want cloning: %+v", e.Status, e)
	}
	mustBerth(t, repo, path, "add", "--wait")
	if e := stateOf(t, repo, path).volume(t, "cache"); e.Status != "ready" {
		t.Errorf("after the clone, cache is %q, want ready: %+v", e.Status, e)
	}
	waitPostAdd(t, repo, path)
}

// Post-add runs once, after every clone is ready, in the worktree, with
// BERTH_PATH, BERTH_SOURCE and BERTH_CHANGED_FILES set.
func TestPostAddRunsAfterClonesWithEnvironment(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	out := t.TempDir()
	script := fmt.Sprintf(shPwd+` >> %[1]s/pwd
printf '%%s' "$BERTH_PATH" > %[1]s/path
printf '%%s' "$BERTH_SOURCE" > %[1]s/source
printf '%%s' "$BERTH_CHANGED_FILES" > %[1]s/changed
[ -n "${BERTH_CHANGED_FILES+set}" ] && echo set > %[1]s/changed-set
%[2]s status --json > %[1]s/status.json`, shellQuote(out), shellQuote(berthBin))
	writeFile(t, repo.privateConfig(), fmt.Sprintf("[compose.volumes]\ncache = \"clone\"\n[hooks]\npost-add = %s\n", tomlString(script)))

	wt := repo.AddWorktree(t, "feat-post-add")
	writeFile(t, filepath.Join(wt, "README.md"), "changed on the branch\n")
	repo.Git(t, "-C", wt, "commit", "-q", "-am", "branch change")
	res := berthWithDelay(t, repo, wt, "5s", "add")
	if res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s", res.Exit, res.Stderr)
	}
	if pa := waitPostAdd(t, repo, wt); pa.Status != "ok" {
		t.Fatalf("post-add ended %q, want ok: %+v", pa.Status, pa)
	}

	if got := lines(t, filepath.Join(out, "pwd")); len(got) != 1 || got[0] != wt {
		t.Errorf("post-add ran in %q, want once in the worktree %s", got, wt)
	}
	if got := readString(t, filepath.Join(out, "path")); got != wt {
		t.Errorf("BERTH_PATH=%q, want %s", got, wt)
	}
	if got := readString(t, filepath.Join(out, "source")); got != repo.Dir {
		t.Errorf("BERTH_SOURCE=%q, want the main worktree %s", got, repo.Dir)
	}
	if _, err := os.Stat(filepath.Join(out, "changed-set")); err != nil {
		t.Errorf("BERTH_CHANGED_FILES is not set")
	}
	changed := strings.Split(readString(t, filepath.Join(out, "changed")), "\n")
	if !contains(changed, "README.md") {
		t.Errorf("BERTH_CHANGED_FILES=%q, want it to list README.md, which the branch changed", changed)
	}
	if contains(changed, "compose.yml") {
		t.Errorf("BERTH_CHANGED_FILES=%q lists compose.yml, which matches main", changed)
	}

	var seen hookStatus
	if err := json.Unmarshal([]byte(readString(t, filepath.Join(out, "status.json"))), &seen); err != nil {
		t.Fatalf("status from inside post-add: %v", err)
	}
	for _, e := range seen.State {
		if e.Status != "ready" {
			t.Errorf("while post-add ran, volume %s was %q, want every clone ready first", e.Name, e.Status)
		}
	}
	if len(seen.State) == 0 {
		t.Errorf("while post-add ran, status reported no clone state")
	}

	mustBerth(t, repo, wt, "add")
	time.Sleep(2 * time.Second)
	if got := lines(t, filepath.Join(out, "pwd")); len(got) != 1 {
		t.Errorf("post-add ran %d times after a second add, want once", len(got))
	}
}

// A failing post-add is reported by berth status with its exit code and
// the tail of its output; the berth itself stays usable.
func TestFailingPostAddIsReported(t *testing.T) {
	repo := NewRepo(t)
	script := `for i in $(seq 1 200); do echo "line $i"; done; echo "composer exploded" >&2; exit 3`
	writeFile(t, repo.privateConfig(), fmt.Sprintf("[hooks]\npost-add = %s\n", tomlString(script)))
	wt := repo.AddWorktree(t, "feat-fail")
	add := mustBerth(t, repo, wt, "add")
	if add.Exit != 0 {
		t.Fatalf("berth add exited %d with a failing post-add, want 0", add.Exit)
	}

	pa := waitPostAdd(t, repo, wt)
	if pa.Status != "failed" {
		t.Fatalf("post-add is %q, want failed: %+v", pa.Status, pa)
	}
	if pa.ExitCode == nil || *pa.ExitCode != 3 {
		t.Errorf("post-add exit code %v, want 3", pa.ExitCode)
	}
	if !strings.Contains(pa.Output, "composer exploded") || !strings.Contains(pa.Output, "line 200") {
		t.Errorf("post-add output doesn't end with what the hook printed last:\n%s", pa.Output)
	}
	if strings.Contains(pa.Output, "line 1\n") {
		t.Errorf("post-add output is the whole output, want only its tail:\n%s", pa.Output)
	}

	text := mustBerth(t, repo, wt, "status")
	if !strings.Contains(text.Stdout, "post-add") || !strings.Contains(text.Stdout, "exit 3") || !strings.Contains(text.Stdout, "composer exploded") {
		t.Errorf("berth status doesn't report the failed post-add with its exit code and output:\n%s", text.Stdout)
	}

	env := mustBerth(t, repo, wt, "env")
	if !strings.Contains(env.Stdout, "COMPOSE_PROJECT_NAME=") {
		t.Errorf("berth env after a failed post-add has no COMPOSE_PROJECT_NAME:\n%s", env.Stdout)
	}
	cfg := composeConfig(t, repo, wt)
	if name, _ := cfg["name"].(string); name == "" || name == repo.Project {
		t.Errorf("docker compose config after a failed post-add reports project %q, want the berth's own", name)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// berth hook uninstall removes berth's own hook, so git worktree add stops creating
// berths; it refuses a hook berth didn't write and does nothing when there is none.
func TestHookUninstall(t *testing.T) {
	repo := NewRepo(t)
	hookPath := filepath.Join(repo.Dir, ".git", "hooks", "post-checkout")
	mustBerth(t, repo, repo.Dir, "hook", "install")

	var dry struct {
		Hook    string `json:"hook"`
		Changed bool   `json:"changed"`
		DryRun  bool   `json:"dry_run"`
	}
	res := mustBerth(t, repo, repo.Dir, "hook", "uninstall", "-n", "--json")
	if err := json.Unmarshal([]byte(res.Stdout), &dry); err != nil || !dry.Changed || !dry.DryRun || dry.Hook != hookPath {
		t.Errorf("hook uninstall -n --json = %s, want changed and dry_run for %s", res.Stdout, hookPath)
	}
	if _, err := os.Stat(hookPath); err != nil {
		t.Fatalf("a dry run removed the hook: %v", err)
	}

	mustBerth(t, repo, repo.Dir, "hook", "uninstall")
	wt := hookWorktree(t, repo, nil, "feat-unhooked")
	if got := berthsAt(list(t, repo), wt); len(got) != 0 {
		t.Errorf("git worktree add after hook uninstall created %d berths, want none", len(got))
	}
	if again := mustBerth(t, repo, repo.Dir, "hook", "uninstall"); !strings.Contains(again.Stdout, "no post-checkout hook") {
		t.Errorf("uninstalling with no hook: %q, want it to say there is none", again.Stdout)
	}

	writeFile(t, hookPath, "#!/bin/sh\necho mine\n")
	if res := repo.Berth(t, repo.Dir, "hook", "uninstall"); res.Exit == 0 {
		t.Errorf("uninstalling a hook berth didn't write exited 0")
	}
	if got := strings.Join(lines(t, hookPath), "\n"); got != "#!/bin/sh\necho mine" {
		t.Errorf("someone else's hook was changed:\n%s", got)
	}
}

// berth status --wait returns once clones and post-add have finished, which add --wait
// doesn't wait for, and exits 1 when post-add failed.
func TestStatusWaitWaitsForClonesAndPostAdd(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	marker := filepath.Join(t.TempDir(), "post-add-done")
	writeFile(t, repo.privateConfig(), fmt.Sprintf("[compose.volumes]\ncache = \"clone\"\n[hooks]\npost-add = %s\n", tomlString("sleep 3 && touch "+shellQuote(marker))))
	wt := repo.AddWorktree(t, "feat-wait")
	if res := berthWithDelay(t, repo, wt, "4s", "add"); res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s", res.Exit, res.Stderr)
	}

	start := time.Now()
	res := mustBerth(t, repo, wt, "status", "--wait", "--json")
	if took := time.Since(start); took < 4*time.Second {
		t.Errorf("status --wait returned after %s, before the clone and post-add could finish", took)
	}
	var s hookStatus
	if err := json.Unmarshal([]byte(res.Stdout), &s); err != nil {
		t.Fatalf("berth status --wait --json: %v\n%s", err, res.Stdout)
	}
	if s.PostAdd == nil || s.PostAdd.Status != "ok" {
		t.Errorf("after status --wait, post-add is %+v, want ok", s.PostAdd)
	}
	if len(s.State) != 1 || s.State[0].Status != "ready" {
		t.Errorf("after status --wait, state is %+v, want cache ready", s.State)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("post-add hadn't finished when status --wait returned: %v", err)
	}
	if res := mustBerth(t, repo, wt, "status", "--wait"); res.Exit != 0 {
		t.Errorf("status --wait with nothing left to wait for: exit %d", res.Exit)
	}

	failing := NewRepo(t)
	writeFile(t, failing.privateConfig(), "[hooks]\npost-add = \"sleep 1; exit 3\"\n")
	wt = failing.AddWorktree(t, "feat-wait-fail")
	mustBerth(t, failing, wt, "add")
	res = failing.Berth(t, wt, "status", "--wait", "--json")
	var f struct {
		PostAdd *postAddStatus `json:"post_add"`
		Error   *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &f); err != nil {
		t.Fatalf("berth status --wait --json: %v\n%s", err, res.Stdout)
	}
	if res.Exit != 1 || f.Error == nil || f.PostAdd == nil || f.PostAdd.Status != "failed" {
		t.Errorf("status --wait after a failing post-add: exit %d, %s; want exit 1 with the status and an error", res.Exit, res.Stdout)
	}
}
