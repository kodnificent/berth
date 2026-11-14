//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type removedItem struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type removeResult struct {
	ID      string        `json:"id"`
	DryRun  bool          `json:"dry_run"`
	Removed []removedItem `json:"removed"`
}

type pruneResult struct {
	DryRun bool           `json:"dry_run"`
	Berths []removeResult `json:"berths"`
}

func removeJSON(t *testing.T, repo *Repo, dir string, args ...string) removeResult {
	t.Helper()
	res := mustBerth(t, repo, dir, append([]string{"remove", "--json"}, args...)...)
	var r removeResult
	if err := json.Unmarshal([]byte(res.Stdout), &r); err != nil {
		t.Fatalf("berth remove --json: %v\n%s", err, res.Stdout)
	}
	if r.Removed == nil {
		t.Fatalf("berth remove --json has no removed list:\n%s", res.Stdout)
	}
	return r
}

func pruneJSON(t *testing.T, repo *Repo, args ...string) pruneResult {
	t.Helper()
	res := mustBerth(t, repo, repo.Dir, append([]string{"prune", "--json"}, args...)...)
	var p pruneResult
	if err := json.Unmarshal([]byte(res.Stdout), &p); err != nil {
		t.Fatalf("berth prune --json: %v\n%s", err, res.Stdout)
	}
	if p.Berths == nil {
		t.Fatalf("berth prune --json has no berths list:\n%s", res.Stdout)
	}
	return p
}

func (r removeResult) has(kind, name string) bool {
	for _, it := range r.Removed {
		if it.Kind == kind && it.Name == name {
			return true
		}
	}
	return false
}

func (r removeResult) items() []string {
	var out []string
	for _, it := range r.Removed {
		out = append(out, it.Kind+" "+it.Name)
	}
	sort.Strings(out)
	return out
}

func dockerHas(kind, name string) bool {
	return exec.Command("docker", kind, "inspect", name).Run() == nil
}

// labelled lists the containers or networks Docker labels with compose project.
func labelled(t *testing.T, kind, project string) []string {
	t.Helper()
	args := []string{"ps", "-a", "--format", "{{.Names}}"}
	if kind == "network" {
		args = []string{"network", "ls", "--format", "{{.Name}}"}
	}
	args = append(args, "--filter", "label=com.docker.compose.project="+project)
	out, err := exec.Command("docker", args...).Output()
	if err != nil {
		t.Fatalf("docker %v: %v", args, err)
	}
	return strings.Fields(string(out))
}

func mustDocker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// worktreeState describes a worktree as git and the filesystem see it: every path git
// reports, ignored ones included, and each tracked file's content.
func worktreeState(t *testing.T, repo *Repo, dir string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(repo.mustRun(t, dir, "git", "status", "--porcelain=v1", "--untracked-files=all", "--ignored"))
	for _, f := range strings.Split(strings.TrimRight(repo.mustRun(t, dir, "git", "ls-files"), "\n"), "\n") {
		data, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s %q\n", f, data)
	}
	return b.String()
}

// berth remove deletes the berth's compose project, its clone and fresh
// volumes, and its generated files, prints what it removed, and never deletes a share
// volume or anything in main.
func TestRemoveDeletesBerthResources(t *testing.T) {
	repo := NewRepo(t)
	mainCache := fillCache(t, repo)
	mainSum := checksum(t, mainCache)
	repo.Compose(t, repo.Dir, "build", "web")
	mainImage := repo.Project + "-web"
	mainImageID := imageID(t, mainImage)
	if mainImageID == "" {
		t.Fatalf("main's image %s was not built", mainImage)
	}

	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	wt := repo.AddWorktree(t, "feat-rm")
	mustBerth(t, repo, wt, "add", "--wait")
	project := stateOf(t, repo, wt).ComposeProject
	repo.Compose(t, wt, "create")
	repo.Compose(t, wt, "up", "-d", "--no-deps", "search")

	cloned, fresh := project+"_cache", project+"_db-data"
	for _, v := range []string{cloned, fresh} {
		if !dockerHas("volume", v) {
			t.Fatalf("berth volume %s does not exist before remove", v)
		}
	}
	containers, networks := labelled(t, "container", project), labelled(t, "network", project)
	if len(containers) == 0 || len(networks) == 0 {
		t.Fatalf("berth project %s has containers %v and networks %v before remove, want some of each", project, containers, networks)
	}

	dry := removeJSON(t, repo, wt, "--dry-run")
	if !dry.DryRun {
		t.Errorf("remove --dry-run --json reports dry_run false")
	}
	for _, want := range []removedItem{{"volume", cloned}, {"volume", fresh}, {"file", ".env"}, {"file", "compose.berth.yml"}} {
		if !dry.has(want.Kind, want.Name) {
			t.Errorf("remove --dry-run does not list %s %s: %v", want.Kind, want.Name, dry.items())
		}
	}
	if !dockerHas("volume", cloned) || !fileExistsAt(filepath.Join(wt, ".env")) || len(labelled(t, "container", project)) != len(containers) {
		t.Fatalf("remove --dry-run deleted something")
	}
	mustBerth(t, repo, wt, "status")

	got := removeJSON(t, repo, wt)
	for _, c := range containers {
		if !got.has("container", c) {
			t.Errorf("remove does not report removing container %s: %v", c, got.items())
		}
	}
	for _, n := range networks {
		if !got.has("network", n) {
			t.Errorf("remove does not report removing network %s: %v", n, got.items())
		}
	}
	for _, v := range []string{cloned, fresh} {
		if !got.has("volume", v) {
			t.Errorf("remove does not report removing volume %s: %v", v, got.items())
		}
		if dockerHas("volume", v) {
			t.Errorf("berth volume %s still exists after remove", v)
		}
	}
	if left := labelled(t, "container", project); len(left) > 0 {
		t.Errorf("containers of the berth's project left after remove: %v", left)
	}
	if left := labelled(t, "network", project); len(left) > 0 {
		t.Errorf("networks of the berth's project left after remove: %v", left)
	}
	if dockerHas("image", project+"-web") {
		t.Errorf("the berth's image %s-web still exists after remove", project)
	}
	for _, f := range []string{".env", "compose.berth.yml"} {
		if fileExistsAt(filepath.Join(wt, f)) {
			t.Errorf("%s still exists in the worktree after remove", f)
		}
	}
	if got := imageID(t, mainImage); got != mainImageID {
		t.Errorf("main's image %s is %s after remove, want %s", mainImage, got, mainImageID)
	}
	if got := checksum(t, mainCache); got != mainSum {
		t.Errorf("main's cache volume changed after remove")
	}
	if res := repo.Berth(t, wt, "status", "--json"); res.Exit == 0 || !strings.Contains(res.Stdout, "no_berth") {
		t.Errorf("status after remove: exit %d, want no_berth\n%s", res.Exit, res.Stdout)
	}

	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"share\"\n")
	shared := repo.AddWorktree(t, "feat-share")
	mustBerth(t, repo, shared, "add")
	sharedProject := stateOf(t, repo, shared).ComposeProject
	repo.Compose(t, shared, "create", "--no-build", "search", "db")
	res := mustBerth(t, repo, shared, "remove")
	for _, want := range []string{sharedProject + "_db-data", ".env", "compose.berth.yml"} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("remove does not print that it removed %s:\n%s", want, res.Stdout)
		}
	}
	if strings.Contains(res.Stdout, mainCache) {
		t.Errorf("remove prints main's share volume %s as removed:\n%s", mainCache, res.Stdout)
	}
	if !dockerHas("volume", mainCache) {
		t.Fatalf("remove deleted main's share volume %s", mainCache)
	}
	if got := checksum(t, mainCache); got != mainSum {
		t.Errorf("main's cache volume changed after removing a berth that shares it")
	}
}

func fileExistsAt(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// After remove, the worktree's tracked files are untouched, and its .env is
// exactly what it was before add, or absent if berth created it.
func TestRemoveRestoresWorktree(t *testing.T) {
	repo := NewRepo(t)
	writeFile(t, filepath.Join(repo.Dir, "web", "extra.txt"), "cloned into the berth\n")
	writeFile(t, repo.privateConfig(), "[files]\nclone = [\"web\"]\n")

	created := repo.AddWorktree(t, "feat-created")
	before := worktreeState(t, repo, created)
	mustBerth(t, repo, created, "add")
	if !fileExistsAt(filepath.Join(created, ".env")) || !fileExistsAt(filepath.Join(created, "web", "extra.txt")) {
		t.Fatalf("add did not clone .env and web/extra.txt")
	}
	removeJSON(t, repo, created)
	if fileExistsAt(filepath.Join(created, ".env")) {
		t.Errorf(".env berth created is still there after remove")
	}
	if after := worktreeState(t, repo, created); after != before {
		t.Errorf("worktree after remove differs from before add:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	own := repo.AddWorktree(t, "feat-own")
	envPath := filepath.Join(own, ".env")
	original := []byte("OWN=1\n# a comment\nNO_FINAL_NEWLINE=yes")
	if err := os.WriteFile(envPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	// 0600, or on Windows, which keeps only a read-only flag, 0666.
	written, err := os.Stat(envPath)
	if err != nil {
		t.Fatal(err)
	}
	before = worktreeState(t, repo, own)
	mustBerth(t, repo, own, "add")
	if got := readString(t, envPath); got == string(original) {
		t.Fatalf("add did not write the managed block into the worktree's own .env")
	}
	removeJSON(t, repo, own)
	got, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("the worktree's own .env is gone after remove: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf(".env after remove is %q, want exactly what it held before add, %q", got, original)
	}
	if info, err := os.Stat(envPath); err == nil && info.Mode().Perm() != written.Mode().Perm() {
		t.Errorf(".env mode after remove is %v, want the %v it was written with", info.Mode().Perm(), written.Mode().Perm())
	}
	if after := worktreeState(t, repo, own); after != before {
		t.Errorf("worktree after remove differs from before add:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

type listedFull struct {
	ID        string `json:"id"`
	Worktree  string `json:"worktree"`
	Orphaned  bool   `json:"orphaned"`
	DiskBytes *int64 `json:"disk_bytes"`
	Host      string `json:"host"`
	Ports     []any  `json:"ports"`
}

func listFull(t *testing.T, repo *Repo) map[string]listedFull {
	t.Helper()
	res := mustBerth(t, repo, repo.Dir, "list", "--json")
	var l struct {
		Berths []listedFull `json:"berths"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &l); err != nil {
		t.Fatalf("berth list --json: %v\n%s", err, res.Stdout)
	}
	out := map[string]listedFull{}
	for _, b := range l.Berths {
		out[b.ID] = b
	}
	return out
}

// After git worktree remove, the next berth command frees that berth's
// ports and hostname, and lists the berth as orphaned with its disk use.
func TestRemovedWorktreeFreesPortsAndIsOrphaned(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	pool := freePortBlock(t, 1)
	writeFile(t, repo.userConfig(), poolConfig(pool))
	writeFile(t, repo.privateConfig(), onlyWeb(t, repo)+"\n[compose.volumes]\ncache = \"clone\"\n")

	gone := repo.AddWorktree(t, "feat-gone")
	mustBerth(t, repo, gone, "add", "--wait")
	s := portsStatusOf(t, repo, gone)
	if got := s.port(t, "web", 80).HostPort; got != pool[0] {
		t.Fatalf("web was assigned %d, want the only pool port %d", got, pool[0])
	}
	if b := listFull(t, repo)[s.ID]; b.Host == "" || len(b.Ports) == 0 {
		t.Fatalf("list --json reports host %q and ports %v for a live berth, want both", b.Host, b.Ports)
	}

	repo.Git(t, "worktree", "remove", "--force", gone)
	b, ok := listFull(t, repo)[s.ID]
	if !ok {
		t.Fatalf("berth %s is not listed after git worktree remove", s.ID)
	}
	if !b.Orphaned {
		t.Errorf("berth of a removed worktree is not listed as orphaned: %+v", b)
	}
	if b.DiskBytes == nil || *b.DiskBytes < 1<<20 {
		t.Errorf("orphaned berth with a cloned 1 MiB volume lists disk_bytes %v, want at least %d", b.DiskBytes, 1<<20)
	}
	if b.Host != "" || len(b.Ports) != 0 {
		t.Errorf("orphaned berth still holds host %q and ports %v after the next berth command", b.Host, b.Ports)
	}

	next := repo.AddWorktree(t, "feat-next")
	res := repo.Berth(t, next, "add")
	if res.Exit != 0 {
		t.Fatalf("add after the only pool port's berth was orphaned: exit %d\n%s%s", res.Exit, res.Stdout, res.Stderr)
	}
	if got := portsStatusOf(t, repo, next).port(t, "web", 80).HostPort; got != pool[0] {
		t.Errorf("new berth got web port %d, want the orphan's freed port %d", got, pool[0])
	}
}

// berth prune --dry-run lists every orphaned berth and what would be
// deleted, deleting nothing; berth prune deletes exactly that list.
func TestPruneDeletesExactlyWhatDryRunLists(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	removed := repo.AddWorktree(t, "feat-removed")
	deleted := repo.AddWorktree(t, "feat-deleted")
	live := repo.AddWorktree(t, "feat-live")
	ids := map[string]string{}
	projects := map[string]string{}
	for _, wt := range []string{removed, deleted, live} {
		mustBerth(t, repo, wt, "add", "--wait")
		s := stateOf(t, repo, wt)
		ids[wt], projects[wt] = s.ID, s.ComposeProject
	}
	repo.Compose(t, removed, "create", "--no-build", "search")

	repo.Git(t, "worktree", "remove", "--force", removed)
	if err := os.RemoveAll(deleted); err != nil {
		t.Fatal(err)
	}

	dry := pruneJSON(t, repo, "--dry-run")
	if !dry.DryRun {
		t.Errorf("prune --dry-run --json reports dry_run false")
	}
	byID := map[string]removeResult{}
	for _, b := range dry.Berths {
		byID[b.ID] = b
	}
	if len(byID) != 2 || byID[ids[removed]].ID == "" || byID[ids[deleted]].ID == "" {
		t.Fatalf("prune --dry-run lists %v, want exactly the orphans %s and %s", dry.Berths, ids[removed], ids[deleted])
	}
	for _, wt := range []string{removed, deleted} {
		if v := projects[wt] + "_cache"; !byID[ids[wt]].has("volume", v) {
			t.Errorf("prune --dry-run does not list volume %s: %v", v, byID[ids[wt]].items())
		}
	}
	for _, c := range labelled(t, "container", projects[removed]) {
		if !byID[ids[removed]].has("container", c) {
			t.Errorf("prune --dry-run does not list container %s: %v", c, byID[ids[removed]].items())
		}
	}
	for _, wt := range []string{removed, deleted} {
		if !dockerHas("volume", projects[wt]+"_cache") {
			t.Errorf("prune --dry-run deleted volume %s", projects[wt]+"_cache")
		}
	}
	if n := len(listFull(t, repo)); n != 3 {
		t.Errorf("after prune --dry-run, list has %d berths, want all 3", n)
	}

	real := pruneJSON(t, repo)
	if real.DryRun {
		t.Errorf("prune --json reports dry_run true")
	}
	if len(real.Berths) != len(dry.Berths) {
		t.Fatalf("prune removed %d berths, dry run listed %d", len(real.Berths), len(dry.Berths))
	}
	for _, b := range real.Berths {
		want := byID[b.ID]
		if strings.Join(b.items(), "\n") != strings.Join(want.items(), "\n") {
			t.Errorf("prune removed for %s:\n%s\nwant what the dry run listed:\n%s", b.ID, strings.Join(b.items(), "\n"), strings.Join(want.items(), "\n"))
		}
		for _, it := range b.Removed {
			if (it.Kind == "volume" || it.Kind == "container" || it.Kind == "network") && dockerHas(it.Kind, it.Name) {
				t.Errorf("prune reports removing %s %s, but it still exists", it.Kind, it.Name)
			}
		}
	}
	after := listFull(t, repo)
	if len(after) != 1 || after[ids[live]].ID == "" {
		t.Errorf("after prune, list has %v, want only the live berth %s", after, ids[live])
	}
	if !dockerHas("volume", projects[live]+"_cache") {
		t.Errorf("prune deleted the live berth's volume %s", projects[live]+"_cache")
	}
	mustBerth(t, repo, live, "status")
	if !dockerHas("volume", repo.Project+"_cache") {
		t.Errorf("prune deleted main's cache volume")
	}
}

// strayImage builds a tiny image of its own under ref, so cleaning it up never removes
// an image another test or the developer uses.
func strayImage(t *testing.T, ref string) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Dockerfile"), "FROM busybox:1.36.1\nLABEL dev.berth.e2e.stray="+ref+"\n")
	mustDocker(t, "build", "-q", "-t", ref, dir)
}

// strays creates resources whose names match the berth's project, one volume even
// labelled with it, none of which berth created.
func strays(t *testing.T, project string) []removedItem {
	t.Helper()
	mustDocker(t, "volume", "create", "--label", "com.docker.compose.project="+project, project+"_stray")
	mustDocker(t, "volume", "create", project+"_plain")
	mustDocker(t, "create", "--name", project+"-stray-1", "busybox:1.36.1", "true")
	strayImage(t, project+"-stray")
	return []removedItem{
		{"volume", project + "_stray"}, {"volume", project + "_plain"},
		{"container", project + "-stray-1"}, {"image", project + "-stray"},
	}
}

func checkStrays(t *testing.T, after string, items []removedItem) {
	t.Helper()
	for _, it := range items {
		if !dockerHas(it.Kind, it.Name) {
			t.Errorf("%s deleted %s %s, which berth didn't create", after, it.Kind, it.Name)
		}
	}
}

// Remove and prune never delete a resource berth didn't record as created
// by that berth, even when names match.
func TestRemoveAndPruneSpareUnrecordedResources(t *testing.T) {
	repo := NewRepo(t)
	mainCache := fillCache(t, repo)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	first := repo.AddWorktree(t, "feat")
	second := repo.AddWorktree(t, "feat-2")
	mustBerth(t, repo, first, "add", "--wait")
	mustBerth(t, repo, second, "add", "--wait")
	p1, p2 := stateOf(t, repo, first).ComposeProject, stateOf(t, repo, second).ComposeProject
	if !strings.HasPrefix(p2, p1) {
		t.Fatalf("second berth's project %s doesn't start with the first's %s, so the names don't overlap as intended", p2, p1)
	}

	firstStrays := strays(t, p1)
	r := removeJSON(t, repo, first)
	for _, it := range firstStrays {
		if r.has(it.Kind, it.Name) {
			t.Errorf("remove reports removing %s %s, which berth didn't create", it.Kind, it.Name)
		}
	}
	checkStrays(t, "remove", firstStrays)
	for _, v := range []string{p2 + "_cache", p2 + "_db-data", mainCache} {
		if !dockerHas("volume", v) {
			t.Errorf("removing berth %s deleted volume %s", p1, v)
		}
	}

	secondStrays := strays(t, p2)
	repo.Git(t, "worktree", "remove", "--force", second)
	p := pruneJSON(t, repo)
	if len(p.Berths) != 1 {
		t.Fatalf("prune removed %d berths, want the one orphan: %+v", len(p.Berths), p.Berths)
	}
	for _, it := range append(firstStrays, secondStrays...) {
		if p.Berths[0].has(it.Kind, it.Name) {
			t.Errorf("prune reports removing %s %s, which berth didn't create", it.Kind, it.Name)
		}
	}
	checkStrays(t, "prune", append(firstStrays, secondStrays...))
	if dockerHas("volume", p2+"_cache") {
		t.Errorf("prune left the orphan's own volume %s_cache", p2)
	}
	if !dockerHas("volume", mainCache) {
		t.Errorf("prune deleted main's volume %s", mainCache)
	}
}

// pre-remove commands run before berth remove deletes anything.
// The command can run berth itself, and a failing one stops the removal with nothing
// deleted.
func TestPreRemoveRunsBeforeAnythingIsDeleted(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	out := filepath.Join(t.TempDir(), "pre-remove.out")
	script := `{
  test -f .env && echo env=yes
  test -f compose.berth.yml && echo override=yes
  docker volume inspect "$(grep '^COMPOSE_PROJECT_NAME=' .env | cut -d= -f2)_cache" >/dev/null 2>&1 && echo volume=yes
  echo "path=$BERTH_PATH"
  ` + shellQuote(berthBin) + ` status >/dev/null && echo status=yes
} > ` + shellQuote(out)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n\n[hooks]\npre-remove = "+tomlString(script)+"\n")
	wt := repo.AddWorktree(t, "feat-pre")
	mustBerth(t, repo, wt, "add", "--wait")
	project := stateOf(t, repo, wt).ComposeProject

	removeJSON(t, repo, wt)
	got := readString(t, out)
	for _, want := range []string{"env=yes", "override=yes", "volume=yes", "path=" + wt, "status=yes"} {
		if !strings.Contains(got, want) {
			t.Errorf("pre-remove saw %q, want %s: it must run before remove deletes anything", got, want)
		}
	}
	if dockerHas("volume", project+"_cache") {
		t.Errorf("remove left the berth's volume after pre-remove")
	}

	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n\n[hooks]\npre-remove = \"exit 7\"\n")
	failing := repo.AddWorktree(t, "feat-pre-fails")
	mustBerth(t, repo, failing, "add", "--wait")
	failingProject := stateOf(t, repo, failing).ComposeProject
	res := repo.Berth(t, failing, "remove")
	if res.Exit == 0 {
		t.Fatalf("remove with a failing pre-remove exited 0\n%s%s", res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "pre-remove") {
		t.Errorf("remove with a failing pre-remove doesn't name pre-remove:\n%s", res.Stderr)
	}
	mustBerth(t, repo, failing, "status")
	if !fileExistsAt(filepath.Join(failing, ".env")) || !dockerHas("volume", failingProject+"_cache") {
		t.Errorf("remove deleted things although pre-remove failed")
	}
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	removeJSON(t, repo, failing)
}
