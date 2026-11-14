//go:build e2e

package e2e

import (
	"bufio"
	"encoding/json"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// cloneDelay makes the clone process wait this long before copying, standing in for a
// volume large enough to take that long. Fixture volumes are kept small on purpose.
const cloneDelay = "BERTH_TEST_CLONE_DELAY"

type stateEntry struct {
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Volume       string   `json:"volume"`
	Strategy     string   `json:"strategy"`
	Status       string   `json:"status"`
	PID          int      `json:"pid"`
	Paused       []string `json:"paused"`
	PauseSeconds *float64 `json:"pause_seconds"`
	Error        string   `json:"error"`
}

type stateStatus struct {
	ID             string       `json:"id"`
	ComposeProject string       `json:"compose_project"`
	State          []stateEntry `json:"state"`
}

func stateOf(t *testing.T, repo *Repo, dir string) stateStatus {
	t.Helper()
	res := mustBerth(t, repo, dir, "status", "--json")
	var s stateStatus
	if err := json.Unmarshal([]byte(res.Stdout), &s); err != nil {
		t.Fatalf("berth status --json: %v\n%s", err, res.Stdout)
	}
	if s.State == nil {
		t.Fatalf("berth status --json has no state list:\n%s", res.Stdout)
	}
	return s
}

func (s stateStatus) volume(t *testing.T, key string) stateEntry {
	t.Helper()
	for _, e := range s.State {
		if e.Kind == "volume" && e.Name == key {
			return e
		}
	}
	t.Fatalf("status reports no state for volume %s: %+v", key, s.State)
	return stateEntry{}
}

// mainVolume is the Docker name of main's volume key, created the way main's compose
// would create it when it doesn't exist yet.
func mainVolume(t *testing.T, repo *Repo, key string) string {
	t.Helper()
	name := repo.Project + "_" + key
	if exec.Command("docker", "volume", "inspect", name).Run() != nil {
		out, err := exec.Command("docker", "volume", "create",
			"--label", "com.docker.compose.project="+repo.Project,
			"--label", "com.docker.compose.volume="+key, name).CombinedOutput()
		if err != nil {
			t.Fatalf("creating main's volume %s: %v\n%s", name, err, out)
		}
	}
	return name
}

// inVolume runs a shell script in a throwaway container with volume mounted at /v.
func inVolume(t *testing.T, volume, script string, readOnly bool) string {
	t.Helper()
	mount := volume + ":/v"
	if readOnly {
		mount += ":ro"
	}
	out, err := exec.Command("docker", "run", "--rm", "-v", mount, "busybox:1.36.1", "sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("script in volume %s: %v\n%s", volume, err, out)
	}
	return string(out)
}

// fillCache gives main's cache volume nested directories, an owner and mode other than
// the default, a symlink, a hard link and about a megabyte of random data.
func fillCache(t *testing.T, repo *Repo) string {
	t.Helper()
	vol := mainVolume(t, repo, "cache")
	inVolume(t, vol, `set -e
mkdir -p /v/a/b/c /v/empty
echo hello > /v/a/b/c/hello.txt
echo secret > /v/a/secret && chmod 600 /v/a/secret && chown 1000:1000 /v/a/secret
ln -s b/c/hello.txt /v/a/link
ln /v/a/b/c/hello.txt /v/a/hard
dd if=/dev/urandom of=/v/random.bin bs=1024 count=1024 2>/dev/null
touch -d '2001-02-03 04:05:06' /v/a/b/c/hello.txt`, false)
	return vol
}

// checksum describes every entry in a volume: path, type, mode, owner, size and content
// hash of files, link targets, and which files share an inode.
func checksum(t *testing.T, volume string) string {
	t.Helper()
	return inVolume(t, volume, `cd /v && find . | sort | while read -r f; do
  if [ -L "$f" ]; then echo "$f link $(readlink "$f")"
  elif [ -d "$f" ]; then echo "$f dir $(stat -c '%a %u:%g' "$f")"
  else echo "$f file $(stat -c '%a %u:%g %s %Y %h' "$f") $(md5sum < "$f")"; fi
done`, true)
}

func berthWithDelay(t *testing.T, repo *Repo, dir string, delay string, args ...string) Result {
	t.Helper()
	return repo.BerthWith(t, []string{cloneDelay + "=" + delay}, dir, args...)
}

// waitFor polls cond every 200ms until it holds or the timeout passes.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func containerState(t *testing.T, id string) (running, paused bool) {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "--format", "{{.State.Running}} {{.State.Paused}}", id).Output()
	if err != nil {
		t.Fatalf("docker inspect %s: %v", id, err)
	}
	f := strings.Fields(string(out))
	return f[0] == "true", f[1] == "true"
}

func serviceContainer(t *testing.T, repo *Repo, dir, service string) string {
	t.Helper()
	id := strings.TrimSpace(repo.Compose(t, dir, "ps", "-q", service))
	if id == "" {
		t.Fatalf("service %s has no container in %s", service, dir)
	}
	return id
}

func mysql(t *testing.T, repo *Repo, dir, sql string) string {
	t.Helper()
	return strings.TrimSpace(repo.Compose(t, dir, "exec", "-T", "db", "mysql", "-uroot", "-pfixture", "-N", "-B", "app", "-e", sql))
}

// After cloning, each clone volume in the berth holds the same data as the
// source's volume at the moment of the copy, and it is the volume the berth's compose
// project uses.
func TestCloneVolumeHoldsSourceData(t *testing.T) {
	repo := NewRepo(t)
	src := fillCache(t, repo)
	want := checksum(t, src)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	wt := repo.AddWorktree(t, "feat-clone")
	mustBerth(t, repo, wt, "add", "--wait")

	s := stateOf(t, repo, wt)
	e := s.volume(t, "cache")
	if e.Status != "ready" {
		t.Fatalf("cache is %q after add --wait, want ready: %+v", e.Status, e)
	}
	if e.Volume == "" || e.Volume == src {
		t.Fatalf("status names the berth's cache volume %q, want one of its own, not main's %q", e.Volume, src)
	}
	vols, _ := composeConfig(t, repo, wt)["volumes"].(map[string]any)
	cache, _ := vols["cache"].(map[string]any)
	if cache["name"] != e.Volume {
		t.Errorf("docker compose config in the berth names cache %v, want the cloned volume %s", cache["name"], e.Volume)
	}
	if got := checksum(t, e.Volume); got != want {
		t.Errorf("berth's cache differs from main's:\nmain:\n%s\nberth:\n%s", want, got)
	}
}

// berth add returns while a large clone is still running, and berth status
// --json reports each volume's state as cloning, ready or failed.
func TestAddReturnsWhileCloning(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	wt := repo.AddWorktree(t, "feat-bg")

	start := time.Now()
	res := berthWithDelay(t, repo, wt, "15s", "add")
	if res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s", res.Exit, res.Stderr)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("berth add took %s with a 15s clone, want it to return while the clone runs", took)
	}
	if e := stateOf(t, repo, wt).volume(t, "cache"); e.Status != "cloning" {
		t.Fatalf("right after add, cache is %q, want cloning: %+v", e.Status, e)
	}
	mustBerth(t, repo, wt, "add", "--wait")
	if e := stateOf(t, repo, wt).volume(t, "cache"); e.Status != "ready" {
		t.Fatalf("after the clone, cache is %q, want ready: %+v", e.Status, e)
	}
}

// Cloning a database volume while main's database receives writes gives a
// berth database that passes an integrity check.
func TestCloneUnderWriteLoadPassesIntegrityCheck(t *testing.T) {
	repo := NewRepo(t)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ndb-data = \"clone\"\n")
	repo.Compose(t, repo.Dir, "up", "-d", "--wait", "db")
	mysql(t, repo, repo.Dir, `CREATE TABLE a (id BIGINT PRIMARY KEY AUTO_INCREMENT, pad CHAR(200) NOT NULL DEFAULT 'padding');
CREATE TABLE b (id BIGINT PRIMARY KEY AUTO_INCREMENT, pad CHAR(200) NOT NULL DEFAULT 'padding')`)

	// One session committing pairs of rows as fast as it can, for as long as the test runs.
	writer := exec.Command("docker", "compose", "exec", "-T", "db", "sh", "-c",
		`yes "START TRANSACTION; INSERT INTO a () VALUES (); INSERT INTO b () VALUES (); COMMIT;" | mysql -uroot -pfixture app`)
	writer.Dir, writer.Env = repo.Dir, repo.Env()
	if err := writer.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Process.Kill(); _ = writer.Wait() }()
	count := func(dir, table string) int { return atoi(t, mysql(t, repo, dir, "SELECT COUNT(*) FROM "+table)) }
	waitFor(t, time.Minute, "main's writer to commit rows", func() bool { return count(repo.Dir, "a") > 200 })

	wt := repo.AddWorktree(t, "feat-load")
	mustBerth(t, repo, wt, "add", "--wait")
	if after := count(repo.Dir, "a"); after <= 200 {
		t.Fatalf("main stopped taking writes: %d rows", after)
	}

	repo.Compose(t, wt, "up", "-d", "--wait", "db")
	// What mysqlcheck --all-databases does, which the mysql image doesn't ship: CHECK TABLE
	// on every table.
	tables := strings.Fields(mysql(t, repo, wt, "SELECT CONCAT('`', table_schema, '`.`', table_name, '`') FROM information_schema.tables WHERE engine = 'InnoDB'"))
	if len(tables) < 2 {
		t.Fatalf("berth's database lists InnoDB tables %v, want at least a and b", tables)
	}
	check := mysql(t, repo, wt, "CHECK TABLE "+strings.Join(tables, ", "))
	for _, line := range strings.Split(check, "\n") {
		if f := strings.Split(line, "\t"); len(f) != 4 || f[2] != "status" || f[3] != "OK" {
			t.Errorf("CHECK TABLE in the berth: %s", line)
		}
	}
	a, b := count(wt, "a"), count(wt, "b")
	t.Logf("berth holds %d pairs of rows; main kept writing during the copy", a)
	if a == 0 || a != b {
		t.Errorf("berth holds %d rows in a and %d in b; every transaction wrote one to each, so a torn copy shows as a mismatch", a, b)
	}
}

// Main's data is unchanged by cloning: checksums of main's volumes are the
// same before and after.
func TestCloneLeavesSourceUnchanged(t *testing.T) {
	repo := NewRepo(t)
	cache := fillCache(t, repo)
	repo.Compose(t, repo.Dir, "up", "-d", "--wait", "db")
	mysql(t, repo, repo.Dir, "CREATE TABLE kept (v INT); INSERT INTO kept VALUES (1), (2), (3)")
	repo.Compose(t, repo.Dir, "stop", "db")
	db := repo.Project + "_db-data"
	before := map[string]string{cache: checksum(t, cache), db: checksum(t, db)}

	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\ndb-data = \"clone\"\n")
	wt := repo.AddWorktree(t, "feat-source")
	mustBerth(t, repo, wt, "add", "--wait")
	s := stateOf(t, repo, wt)
	inVolume(t, s.volume(t, "cache").Volume, "echo changed > /v/a/b/c/hello.txt && rm /v/random.bin", false)

	for vol, sum := range before {
		if got := checksum(t, vol); got != sum {
			t.Errorf("main's volume %s changed:\nbefore:\n%s\nafter:\n%s", vol, sum, got)
		}
	}
}

// If a source service must pause for the copy, add says so before the
// pause, the service is running again when the copy ends, and status reports how long
// the pause lasted.
func TestSourcePauseIsAnnouncedAndBrief(t *testing.T) {
	repo := NewRepo(t)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ndb-data = \"clone\"\n")
	repo.Compose(t, repo.Dir, "up", "-d", "--wait", "db")
	db := serviceContainer(t, repo, repo.Dir, "db")
	wt := repo.AddWorktree(t, "feat-pause")

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

	add := exec.Command(berthBin, "add", "--wait")
	add.Dir, add.Env = wt, repo.Env()
	stderr, err := add.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := add.Start(); err != nil {
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
	if err := add.Wait(); err != nil {
		t.Fatalf("berth add --wait: %v\n%s", err, output.String())
	}

	if announcedAt.IsZero() {
		t.Fatalf("berth add never said main's db would pause:\n%s", output.String())
	}
	// Docker reports a paused container with a health check as unhealthy, so anything
	// gated on service_healthy sees it; the announcement says so.
	if !strings.Contains(output.String(), "unhealthy") {
		t.Errorf("the pause announcement doesn't say main's db, which has a health check, reads unhealthy:\n%s", output.String())
	}
	mu.Lock()
	if pausedAt.IsZero() {
		t.Errorf("main's db was never paused; cloning a running database's volume needs a pause for a consistent copy")
	} else if pausedAt.Before(announcedAt) {
		t.Errorf("main's db paused at %s, before add announced it at %s", pausedAt.Format(time.StampMilli), announcedAt.Format(time.StampMilli))
	}
	mu.Unlock()

	if running, paused := containerState(t, db); !running || paused {
		t.Errorf("after the copy main's db is running=%v paused=%v, want running and not paused", running, paused)
	}
	e := stateOf(t, repo, wt).volume(t, "db-data")
	if e.Status != "ready" {
		t.Fatalf("db-data is %q, want ready: %+v", e.Status, e)
	}
	if len(e.Paused) != 1 || e.Paused[0] != "db" {
		t.Errorf("status reports paused services %v, want [db]", e.Paused)
	}
	if e.PauseSeconds == nil || *e.PauseSeconds <= 0 {
		t.Errorf("status reports pause_seconds %v, want how long db was paused", e.PauseSeconds)
	}
}

// If the clone process is killed with kill -9, berth status reports the
// volume as failed within seconds, never cloning forever, and running berth add again
// finishes the clone. A source container paused for the copy is running again.
func TestKilledCloneFailsAndAddRecovers(t *testing.T) {
	repo := NewRepo(t)
	src := fillCache(t, repo)
	want := checksum(t, src)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	// Built first, since run prints a build's log to stdout ahead of the container's name.
	repo.Compose(t, repo.Dir, "build", "web")
	user := strings.TrimSpace(repo.Compose(t, repo.Dir, "run", "-d", "--no-deps", "web", "sleep", "600"))
	wt := repo.AddWorktree(t, "feat-kill")

	if res := berthWithDelay(t, repo, wt, "120s", "add"); res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s", res.Exit, res.Stderr)
	}
	e := stateOf(t, repo, wt).volume(t, "cache")
	if e.Status != "cloning" || e.PID <= 0 {
		t.Fatalf("right after add, cache is %q with pid %d, want cloning with the clone process's pid", e.Status, e.PID)
	}
	waitFor(t, 30*time.Second, "main's container on the cache volume to pause for the copy", func() bool {
		_, paused := containerState(t, user)
		return paused
	})

	if err := killNow(e.PID); err != nil {
		t.Fatalf("kill -9 %d: %v", e.PID, err)
	}
	killed := time.Now()
	waitFor(t, 10*time.Second, "status to report the killed clone as failed", func() bool {
		return stateOf(t, repo, wt).volume(t, "cache").Status == "failed"
	})
	t.Logf("failed reported %s after kill -9", time.Since(killed).Round(time.Millisecond))
	if running, paused := containerState(t, user); !running || paused {
		t.Errorf("after the clone was killed main's container is running=%v paused=%v, want running and not paused", running, paused)
	}

	mustBerth(t, repo, wt, "add", "--wait")
	e = stateOf(t, repo, wt).volume(t, "cache")
	if e.Status != "ready" {
		t.Fatalf("after add again, cache is %q, want ready: %+v", e.Status, e)
	}
	if got := checksum(t, e.Volume); got != want {
		t.Errorf("after recovery the berth's cache differs from main's:\nmain:\n%s\nberth:\n%s", want, got)
	}
}

// berth add --wait returns only when every clone has finished, and exits 4
// if any failed.
func TestAddWaitWaitsAndExitsFourOnFailure(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")

	t.Run("slow", func(t *testing.T) {
		wt := repo.AddWorktree(t, "feat-wait-slow")
		start := time.Now()
		if res := berthWithDelay(t, repo, wt, "5s", "add", "--wait"); res.Exit != 0 {
			t.Fatalf("berth add --wait: exit %d\n%s", res.Exit, res.Stderr)
		}
		if took := time.Since(start); took < 5*time.Second {
			t.Errorf("berth add --wait returned after %s, before a 5s clone could finish", took)
		}
		if e := stateOf(t, repo, wt).volume(t, "cache"); e.Status != "ready" {
			t.Fatalf("right after add --wait, cache is %q, want ready: %+v", e.Status, e)
		}
	})

	t.Run("failed", func(t *testing.T) {
		wt := repo.AddWorktree(t, "feat-wait-fail")
		add := exec.Command(berthBin, "add", "--wait")
		add.Dir, add.Env = wt, append(repo.Env(), cloneDelay+"=120s")
		var out strings.Builder
		add.Stdout, add.Stderr = &out, &out
		if err := add.Start(); err != nil {
			t.Fatal(err)
		}
		var pid int
		waitFor(t, 30*time.Second, "the clone process to start", func() bool {
			res := repo.Berth(t, wt, "status", "--json")
			var s stateStatus
			if json.Unmarshal([]byte(res.Stdout), &s) != nil {
				return false
			}
			for _, e := range s.State {
				if e.Name == "cache" && e.PID > 0 && e.Status == "cloning" {
					pid = e.PID
				}
			}
			return pid > 0
		})
		if err := killNow(pid); err != nil {
			t.Fatalf("kill -9 %d: %v", pid, err)
		}
		done := make(chan error, 1)
		go func() { done <- add.Wait() }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			_ = add.Process.Kill()
			t.Fatalf("berth add --wait still waiting 30s after its clone was killed:\n%s", out.String())
		}
		if code := add.ProcessState.ExitCode(); code != 4 {
			t.Errorf("berth add --wait exited %d after its clone failed, want 4:\n%s", code, out.String())
		}
	})
}

var sizeInMessage = regexp.MustCompile(`\d+(\.\d+)? ?(B|kB|KB|MB|GB|TB|KiB|MiB|GiB|TiB)\b`)

// When a clone would leave less free disk than the configured threshold,
// add warns before starting it and names the volume and its size.
func TestDiskWarningBeforeClone(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)

	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n[clone]\nmin_free = \"1MB\"\n")
	quiet := repo.AddWorktree(t, "feat-roomy")
	res := mustBerth(t, repo, quiet, "add", "--wait")
	if strings.Contains(res.Stderr, "free") {
		t.Errorf("berth add warned about disk with a 1MB threshold:\n%s", res.Stderr)
	}

	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n[clone]\nmin_free = \"100000TB\"\n")
	wt := repo.AddWorktree(t, "feat-full")
	res = mustBerth(t, repo, wt, "add", "--wait")
	lines := strings.Split(res.Stderr, "\n")
	warn, started := -1, -1
	for i, l := range lines {
		if warn < 0 && strings.Contains(l, "cache") && strings.Contains(l, "free") && sizeInMessage.MatchString(l) {
			warn = i
		}
		if started < 0 && strings.Contains(strings.ToLower(l), "cloning volume cache") {
			started = i
		}
	}
	if warn < 0 {
		t.Fatalf("berth add gave no disk warning naming the cache volume and its size:\n%s", res.Stderr)
	}
	if started >= 0 && started < warn {
		t.Errorf("berth add started cloning cache (line %d) before warning (line %d):\n%s", started, warn, res.Stderr)
	}
	if e := stateOf(t, repo, wt).volume(t, "cache"); e.Status != "ready" {
		t.Errorf("after the warning cache is %q, want the clone to run anyway: %+v", e.Status, e)
	}
}

// berth list --json reports each berth's disk use.
func TestListReportsDiskUse(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	cloned := repo.AddWorktree(t, "feat-disk")
	mustBerth(t, repo, cloned, "add", "--wait")
	writeFile(t, repo.privateConfig(), "")
	plain := repo.AddWorktree(t, "feat-plain")
	mustBerth(t, repo, plain, "add")

	res := mustBerth(t, repo, repo.Dir, "list", "--json")
	var l struct {
		Berths []struct {
			Worktree  string `json:"worktree"`
			DiskBytes *int64 `json:"disk_bytes"`
		} `json:"berths"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &l); err != nil {
		t.Fatalf("berth list --json: %v\n%s", err, res.Stdout)
	}
	got := map[string]int64{}
	for _, b := range l.Berths {
		if b.DiskBytes == nil {
			t.Fatalf("berth list --json entry %s has no disk_bytes:\n%s", b.Worktree, res.Stdout)
		}
		got[b.Worktree] = *b.DiskBytes
	}
	if got[cloned] < 1<<20 {
		t.Errorf("berth with a cloned 1 MiB volume reports disk_bytes %d, want at least %d", got[cloned], 1<<20)
	}
	if got[plain] <= 0 || got[plain] >= got[cloned] {
		t.Errorf("berth with only its .env and override reports disk_bytes %d, want more than 0 and less than the cloned berth's %d", got[plain], got[cloned])
	}
}

// berth status shows how far a clone in progress is, as a fraction and in bytes.
func TestStatusShowsCloneProgress(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	wt := repo.AddWorktree(t, "feat-progress")
	if res := berthWithDelay(t, repo, wt, "12s", "add"); res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s", res.Exit, res.Stderr)
	}

	type progressEntry struct {
		Name        string   `json:"name"`
		Status      string   `json:"status"`
		Progress    *float64 `json:"progress"`
		CopiedBytes int64    `json:"copied_bytes"`
		TotalBytes  int64    `json:"total_bytes"`
	}
	var last progressEntry
	waitFor(t, 10*time.Second, "the clone's progress to pass 0", func() bool {
		var s struct {
			State []progressEntry `json:"state"`
		}
		res := mustBerth(t, repo, wt, "status", "--json")
		if err := json.Unmarshal([]byte(res.Stdout), &s); err != nil || len(s.State) != 1 {
			t.Fatalf("berth status --json: %v\n%s", err, res.Stdout)
		}
		last = s.State[0]
		if last.Status != "cloning" {
			t.Fatalf("cache is %q before the delay ended, want cloning: %+v", last.Status, last)
		}
		if last.Progress == nil || last.TotalBytes < 1<<20 {
			t.Fatalf("a cloning volume reports progress %v of %d bytes, want a fraction of main's megabyte", last.Progress, last.TotalBytes)
		}
		return *last.Progress > 0 && last.CopiedBytes > 0
	})
	if *last.Progress >= 1 {
		t.Errorf("progress %v while still cloning, want below 1", *last.Progress)
	}
	if human := mustBerth(t, repo, wt, "status").Stdout; !regexp.MustCompile(`volume cache\s+cloning \(\d+%, .+ of .+\)`).MatchString(human) {
		t.Errorf("human status shows no progress for the clone:\n%s", human)
	}
	mustBerth(t, repo, wt, "status", "--wait")
	if e := stateOf(t, repo, wt).volume(t, "cache"); e.Status != "ready" {
		t.Errorf("after status --wait, cache is %q, want ready", e.Status)
	}
}
