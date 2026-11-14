//go:build e2e

package e2e

import (
	"bufio"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// clonedBerth makes a worktree whose berth clones main's filled cache volume, and returns
// the worktree and the berth's cache volume once the clone is ready.
func clonedBerth(t *testing.T, repo *Repo, name string) (string, string) {
	t.Helper()
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	wt := repo.AddWorktree(t, name)
	mustBerth(t, repo, wt, "add", "--wait")
	e := stateOf(t, repo, wt).volume(t, "cache")
	if e.Status != "ready" || e.Volume == "" {
		t.Fatalf("cache after add --wait is %q in volume %q, want ready in a volume of the berth's own", e.Status, e.Volume)
	}
	return wt, e.Volume
}

func writeBerthData(t *testing.T, volume string) {
	t.Helper()
	inVolume(t, volume, "echo written in the berth > /v/berth-only.txt && echo changed > /v/a/b/c/hello.txt && rm /v/random.bin", false)
}

// After data is written to a berth's clone volume, reset leaves that volume
// matching main's at the time of the reset.
func TestResetMatchesMainAtResetTime(t *testing.T) {
	repo := NewRepo(t)
	src := fillCache(t, repo)
	wt, vol := clonedBerth(t, repo, "feat-reset")
	writeBerthData(t, vol)
	// Main moves on after the berth was made, so a reset to main's data as it was at add fails.
	inVolume(t, src, "echo main moved on > /v/main-new.txt && rm /v/a/link", false)
	want := checksum(t, src)

	mustBerth(t, repo, wt, "reset")
	mustBerth(t, repo, wt, "status", "--wait")

	e := stateOf(t, repo, wt).volume(t, "cache")
	if e.Status != "ready" {
		t.Fatalf("cache after reset and status --wait is %q, want ready: %+v", e.Status, e)
	}
	if got := checksum(t, e.Volume); got != want {
		t.Errorf("after reset the berth's cache differs from main's at the time of the reset:\nmain:\n%s\nberth:\n%s", want, got)
	}
}

type berthIdentity struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Host           string          `json:"host"`
	URL            string          `json:"url"`
	ComposeProject string          `json:"compose_project"`
	Ports          json.RawMessage `json:"ports"`
}

func identityOf(t *testing.T, repo *Repo, dir string) berthIdentity {
	t.Helper()
	res := mustBerth(t, repo, dir, "status", "--json")
	var b berthIdentity
	if err := json.Unmarshal([]byte(res.Stdout), &b); err != nil {
		t.Fatalf("berth status --json: %v\n%s", err, res.Stdout)
	}
	return b
}

func managedBlock(t *testing.T, envPath string) string {
	t.Helper()
	env := readString(t, envPath)
	start := strings.Index(env, "# >>> berth")
	end := strings.Index(env, "# <<< berth")
	if start < 0 || end < start {
		t.Fatalf("%s has no managed berth block:\n%s", envPath, env)
	}
	return env[start:end]
}

// Ports, name, host, compose project and the managed .env block are the same
// before and after reset.
func TestResetKeepsPortsNamesAndEnvBlock(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	wt, vol := clonedBerth(t, repo, "feat-keep")
	writeBerthData(t, vol)
	before := identityOf(t, repo, wt)
	envBefore := managedBlock(t, filepath.Join(wt, ".env"))
	if before.Name == "" || before.Host == "" || before.ComposeProject == "" || len(before.Ports) < 3 {
		t.Fatalf("status before reset reports %+v, want a name, host, compose project and ports", before)
	}

	mustBerth(t, repo, wt, "reset")
	mustBerth(t, repo, wt, "status", "--wait")

	after := identityOf(t, repo, wt)
	if after.ID != before.ID || after.Name != before.Name || after.Host != before.Host || after.URL != before.URL || after.ComposeProject != before.ComposeProject {
		t.Errorf("reset changed the berth's identity:\nbefore: %+v\nafter:  %+v", before, after)
	}
	if string(after.Ports) != string(before.Ports) {
		t.Errorf("reset changed the berth's ports:\nbefore: %s\nafter:  %s", before.Ports, after.Ports)
	}
	if got := managedBlock(t, filepath.Join(wt, ".env")); got != envBefore {
		t.Errorf("reset changed the managed .env block:\nbefore:\n%s\nafter:\n%s", envBefore, got)
	}
	if got := stateOf(t, repo, wt).volume(t, "cache").Volume; got != vol {
		t.Errorf("after reset the berth's cache is volume %q, want the same volume %q", got, vol)
	}
}

// Main's data checksums are unchanged by reset.
func TestResetLeavesSourceUnchanged(t *testing.T) {
	repo := NewRepo(t)
	cache := fillCache(t, repo)
	repo.Compose(t, repo.Dir, "up", "-d", "--wait", "db")
	mysql(t, repo, repo.Dir, "CREATE TABLE kept (v INT); INSERT INTO kept VALUES (1), (2), (3)")
	repo.Compose(t, repo.Dir, "stop", "db")
	db := repo.Project + "_db-data"

	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\ndb-data = \"clone\"\n")
	wt := repo.AddWorktree(t, "feat-source")
	mustBerth(t, repo, wt, "add", "--wait")
	writeBerthData(t, stateOf(t, repo, wt).volume(t, "cache").Volume)
	before := map[string]string{cache: checksum(t, cache), db: checksum(t, db)}

	mustBerth(t, repo, wt, "reset")
	mustBerth(t, repo, wt, "status", "--wait")

	for vol, sum := range before {
		if got := checksum(t, vol); got != sum {
			t.Errorf("reset changed main's volume %s:\nbefore:\n%s\nafter:\n%s", vol, sum, got)
		}
	}
}

// A source service that must pause for reset's copy is announced before it
// pauses, as in add, and runs again once the copy ends.
func TestResetAnnouncesSourcePause(t *testing.T) {
	repo := NewRepo(t)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ndb-data = \"clone\"\n")
	repo.Compose(t, repo.Dir, "up", "-d", "--wait", "db")
	db := serviceContainer(t, repo, repo.Dir, "db")
	wt := repo.AddWorktree(t, "feat-pause")
	mustBerth(t, repo, wt, "add", "--wait")

	events := exec.Command("docker", "events", "--filter", "container="+db, "--filter", "event=pause", "--format", "{{.Action}}")
	stdout, err := events.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := events.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = events.Process.Kill(); _ = events.Wait() }()
	var mu sync.Mutex
	var pausedAt time.Time
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			mu.Lock()
			if pausedAt.IsZero() {
				pausedAt = time.Now()
			}
			mu.Unlock()
		}
	}()

	reset := exec.Command(berthBin, "reset")
	reset.Dir, reset.Env = wt, repo.Env()
	stderr, err := reset.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := reset.Start(); err != nil {
		t.Fatal(err)
	}
	var announcedAt time.Time
	var output strings.Builder
	sc := bufio.NewScanner(stderr)
	for sc.Scan() {
		line := sc.Text()
		output.WriteString(line + "\n")
		if announcedAt.IsZero() && strings.Contains(line, "pause") && strings.Contains(line, "db") {
			announcedAt = time.Now()
		}
	}
	if err := reset.Wait(); err != nil {
		t.Fatalf("berth reset: %v\n%s", err, output.String())
	}
	mustBerth(t, repo, wt, "status", "--wait")

	if announcedAt.IsZero() {
		t.Fatalf("berth reset never said main's db would pause:\n%s", output.String())
	}
	mu.Lock()
	if pausedAt.IsZero() {
		t.Errorf("main's db was never paused during reset; copying a running database's volume needs a pause for a consistent copy")
	} else if pausedAt.Before(announcedAt) {
		t.Errorf("main's db paused at %s, before reset announced it at %s", pausedAt.Format(time.StampMilli), announcedAt.Format(time.StampMilli))
	}
	mu.Unlock()
	if running, paused := containerState(t, db); !running || paused {
		t.Errorf("after reset's copy main's db is running=%v paused=%v, want running and not paused", running, paused)
	}
}

// While any of the berth's containers run, reset deletes nothing, exits 1
// and names them.
func TestResetRefusesWhileContainersRun(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	wt, vol := clonedBerth(t, repo, "feat-running")
	writeBerthData(t, vol)
	want := checksum(t, vol)
	// search doesn't mount cache: any running container of the berth's blocks reset.
	repo.Compose(t, wt, "up", "-d", "--no-deps", "search")
	container := strings.TrimSpace(mustDocker(t, "inspect", "--format", "{{.Name}}", serviceContainer(t, repo, wt, "search")))
	container = strings.TrimPrefix(container, "/")

	res := repo.Berth(t, wt, "reset")
	if res.Exit != 1 {
		t.Errorf("reset with %s running exited %d, want 1\n%s%s", container, res.Exit, res.Stdout, res.Stderr)
	}
	if out := res.Stdout + res.Stderr; !strings.Contains(out, container) && !strings.Contains(out, "search") {
		t.Errorf("reset refused without naming the running container %s:\n%s", container, out)
	}
	if !dockerHas("volume", vol) {
		t.Fatalf("reset deleted the berth's volume %s while %s ran", vol, container)
	}
	if got := checksum(t, vol); got != want {
		t.Errorf("reset changed the berth's cache while %s ran:\nbefore:\n%s\nafter:\n%s", container, want, got)
	}
	if e := stateOf(t, repo, wt).volume(t, "cache"); e.Status != "ready" {
		t.Errorf("after a refused reset cache is %q, want it still ready: %+v", e.Status, e)
	}
	if running, _ := containerState(t, serviceContainer(t, repo, wt, "search")); !running {
		t.Errorf("reset stopped the berth's running container %s", container)
	}
}

// Reset touches only volumes the berth recorded as its own, never another
// volume under its compose project's name, nor another berth's volume whose name starts
// with it.
func TestResetSparesUnrecordedVolumes(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	first, vol := clonedBerth(t, repo, "feat")
	second, secondVol := clonedBerth(t, repo, "feat-2")
	project, secondProject := stateOf(t, repo, first).ComposeProject, stateOf(t, repo, second).ComposeProject
	if !strings.HasPrefix(secondProject, project) {
		t.Fatalf("second berth's project %s doesn't start with the first's %s, so the names don't overlap as intended", secondProject, project)
	}
	writeBerthData(t, vol)
	writeBerthData(t, secondVol)

	labelled := project + "_stray"
	mustDocker(t, "volume", "create", "--label", "com.docker.compose.project="+project, "--label", "com.docker.compose.volume=stray", labelled)
	plain := project + "_plain"
	mustDocker(t, "volume", "create", plain)
	spared := map[string]string{}
	for _, v := range []string{labelled, plain} {
		inVolume(t, v, "echo not created by berth > /v/mine.txt && dd if=/dev/urandom of=/v/data.bin bs=1024 count=64 2>/dev/null", false)
	}
	for _, v := range []string{labelled, plain, secondVol} {
		spared[v] = checksum(t, v)
	}

	dry := mustBerth(t, repo, first, "reset", "--dry-run")
	for v := range spared {
		if strings.Contains(dry.Stdout+dry.Stderr, v) {
			t.Errorf("reset --dry-run lists volume %s, which this berth didn't create:\n%s%s", v, dry.Stdout, dry.Stderr)
		}
	}

	mustBerth(t, repo, first, "reset")
	mustBerth(t, repo, first, "status", "--wait")

	for v, sum := range spared {
		if !dockerHas("volume", v) {
			t.Errorf("reset deleted volume %s, which this berth didn't create", v)
			continue
		}
		if got := checksum(t, v); got != sum {
			t.Errorf("reset changed volume %s, which this berth didn't create:\nbefore:\n%s\nafter:\n%s", v, sum, got)
		}
	}
	if e := stateOf(t, repo, second).volume(t, "cache"); e.Status != "ready" {
		t.Errorf("resetting %s left the other berth's cache %q, want ready: %+v", project, e.Status, e)
	}
}

// --dry-run and -n list what would be discarded and change nothing.
func TestResetDryRunChangesNothing(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	wt, vol := clonedBerth(t, repo, "feat-dry")
	writeBerthData(t, vol)
	want := checksum(t, vol)

	for _, args := range [][]string{{"reset", "--dry-run"}, {"reset", "-n"}, {"reset", "--dry-run", "--json"}} {
		res := mustBerth(t, repo, wt, args...)
		if !strings.Contains(res.Stdout, vol) {
			t.Errorf("berth %v doesn't list the berth's cache volume %s:\n%s", args, vol, res.Stdout)
		}
		if args[len(args)-1] == "--json" && !json.Valid([]byte(res.Stdout)) {
			t.Errorf("berth %v printed invalid JSON:\n%s", args, res.Stdout)
		}
		if !dockerHas("volume", vol) {
			t.Fatalf("berth %v deleted the berth's volume %s", args, vol)
		}
		if got := checksum(t, vol); got != want {
			t.Errorf("berth %v changed the berth's cache:\nbefore:\n%s\nafter:\n%s", args, want, got)
		}
		if e := stateOf(t, repo, wt).volume(t, "cache"); e.Status != "ready" {
			t.Errorf("after berth %v cache is %q, want it still ready: %+v", args, e.Status, e)
		}
	}
}

// Reset returns while the clone runs in the background, and status --wait
// waits for it, as after add.
func TestResetReturnsWhileCloning(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	wt, vol := clonedBerth(t, repo, "feat-bg")
	writeBerthData(t, vol)

	start := time.Now()
	res := berthWithDelay(t, repo, wt, "15s", "reset")
	if res.Exit != 0 {
		t.Fatalf("berth reset: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("berth reset took %s with a 15s clone, want it to return while the clone runs", took)
	}
	if e := stateOf(t, repo, wt).volume(t, "cache"); e.Status != "cloning" {
		t.Fatalf("right after reset, cache is %q, want cloning: %+v", e.Status, e)
	}
	mustBerth(t, repo, wt, "status", "--wait")
	if took := time.Since(start); took < 15*time.Second {
		t.Errorf("status --wait returned %s after reset, before a 15s clone could finish", took)
	}
	if e := stateOf(t, repo, wt).volume(t, "cache"); e.Status != "ready" {
		t.Errorf("after status --wait, cache is %q, want ready: %+v", e.Status, e)
	}
}

// kill -9 during a reset's clone is reported as failed, with recovery text
// naming berth reset, and running reset again recovers. No volume is ever reported ready
// while it is empty or gone.
func TestKilledResetFailsAndResetRecovers(t *testing.T) {
	repo := NewRepo(t)
	src := fillCache(t, repo)
	want := checksum(t, src)
	wt, vol := clonedBerth(t, repo, "feat-kill")
	writeBerthData(t, vol)

	if res := berthWithDelay(t, repo, wt, "120s", "reset"); res.Exit != 0 {
		t.Fatalf("berth reset: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	var pid int
	waitFor(t, 30*time.Second, "reset's clone process to start", func() bool {
		e := stateOf(t, repo, wt).volume(t, "cache")
		if e.Status == "ready" {
			t.Fatalf("cache reported ready while reset's clone runs: %+v", e)
		}
		pid = e.PID
		return e.Status == "cloning" && pid > 0
	})

	if err := killNow(pid); err != nil {
		t.Fatalf("kill -9 %d: %v", pid, err)
	}
	var e stateEntry
	waitFor(t, 10*time.Second, "status to report the killed reset as failed", func() bool {
		e = stateOf(t, repo, wt).volume(t, "cache")
		if e.Status == "ready" {
			t.Fatalf("cache reported ready after reset's clone was killed mid-copy: %+v", e)
		}
		return e.Status == "failed"
	})
	if !strings.Contains(e.Error, "berth reset") {
		t.Errorf("the killed reset's failure says %q, want recovery text naming berth reset", e.Error)
	}
	if res := repo.Berth(t, wt, "status", "--wait"); res.Exit == 0 {
		t.Errorf("status --wait exited 0 with the killed reset's volume not cloned\n%s", res.Stdout)
	}

	mustBerth(t, repo, wt, "reset")
	mustBerth(t, repo, wt, "status", "--wait")
	e = stateOf(t, repo, wt).volume(t, "cache")
	if e.Status != "ready" {
		t.Fatalf("after reset again, cache is %q, want ready: %+v", e.Status, e)
	}
	if got := checksum(t, e.Volume); got != want {
		t.Errorf("after recovery the berth's cache differs from main's:\nmain:\n%s\nberth:\n%s", want, got)
	}
}
