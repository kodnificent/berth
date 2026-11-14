//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"text/tabwriter"
	"time"
)

// collapsed is each non-blank line of out with its runs of spaces collapsed, since the
// summary's column padding need not match status's.
func collapsed(out string) []string {
	var rows []string
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			rows = append(rows, strings.Join(f, " "))
		}
	}
	return rows
}

// summaryOf splits add's human output into its first line and the summary rows after it.
func summaryOf(t *testing.T, stdout string) (string, []string) {
	t.Helper()
	rows := collapsed(stdout)
	if len(rows) == 0 {
		t.Fatalf("berth add printed nothing on stdout")
	}
	return rows[0], rows[1:]
}

func rowsLabelled(rows []string, label string) []string {
	var out []string
	for _, r := range rows {
		if strings.HasPrefix(r, label+" ") {
			out = append(out, r)
		}
	}
	return out
}

func rowLabelled(t *testing.T, rows []string, label, stdout string) string {
	t.Helper()
	got := rowsLabelled(rows, label)
	if len(got) != 1 {
		t.Fatalf("summary has %d %q rows, want 1:\n%s", len(got), label, stdout)
	}
	return got[0]
}

func humanStatusRows(t *testing.T, repo *Repo, dir string) []string {
	t.Helper()
	return collapsed(mustBerth(t, repo, dir, "status").Stdout)
}

// assertRowsFromStatus checks that every summary row reads as a row berth status prints.
func assertRowsFromStatus(t *testing.T, summary, statusRows []string, stdout string) {
	t.Helper()
	for _, r := range summary {
		if !slices.Contains(statusRows, r) {
			t.Errorf("summary row %q is not a row berth status prints:\nadd:\n%s\nstatus rows:\n%s", r, stdout, strings.Join(statusRows, "\n"))
		}
	}
}

type humanStatus struct {
	ID             string            `json:"id"`
	Worktree       string            `json:"worktree"`
	Repository     string            `json:"repository"`
	Source         string            `json:"source"`
	CreatedAt      string            `json:"created_at"`
	Name           string            `json:"name"`
	Host           string            `json:"host"`
	URL            string            `json:"url"`
	ComposeProject string            `json:"compose_project"`
	Ports          []portEntry       `json:"ports"`
	Env            map[string]string `json:"env"`
	Unresolved     map[string]string `json:"unresolved"`
	Shadowed       []struct {
		Key      string `json:"key"`
		Exported string `json:"exported"`
		Value    string `json:"value"`
	} `json:"shadowed"`
	Files   []string          `json:"files"`
	State   []json.RawMessage `json:"state"`
	PostAdd json.RawMessage   `json:"post_add"`
	Builds  []struct {
		Service string `json:"service"`
		Shared  bool   `json:"shared"`
		Reason  string `json:"reason"`
	} `json:"builds"`
	Next []string `json:"next"`
}

func humanStatusOf(t *testing.T, repo *Repo, dir string) humanStatus {
	t.Helper()
	res := mustBerth(t, repo, dir, "status", "--json")
	var s humanStatus
	if err := json.Unmarshal([]byte(res.Stdout), &s); err != nil {
		t.Fatalf("berth status --json: %v\n%s", err, res.Stdout)
	}
	return s
}

func assertNoEscape(t *testing.T, what, out string) {
	t.Helper()
	if strings.Contains(out, "\x1b") {
		t.Errorf("%s holds an escape byte:\n%q", what, out)
	}
}

func TestHumanOutputAddPrintsNameURLAndPorts(t *testing.T) {
	repo := NewRepo(t)
	search := readDotEnv(t, filepath.Join(repo.Dir, ".env"))["SEARCH_PORT"]
	writeFile(t, repo.privateConfig(), fmt.Sprintf("[compose.ports]\n\"web:80\" = \"assign\"\n\"db:3306\" = \"assign\"\n\"search:%s\" = \"none\"\n", search))
	wt := repo.AddWorktree(t, "feat-summary")
	res := mustBerth(t, repo, wt, "add")
	s := humanStatusOf(t, repo, wt)
	first, rows := summaryOf(t, res.Stdout)
	if !strings.HasPrefix(first, "Added berth "+s.ID) {
		t.Errorf("first line %q, want it to say berth %s was added", first, s.ID)
	}

	if got, want := rowLabelled(t, rows, "name", res.Stdout), "name "+s.Name; got != want {
		t.Errorf("name row %q, want %q", got, want)
	}
	if s.URL == "" {
		t.Fatalf("status --json reports no url for the fixture")
	}
	if got, want := rowLabelled(t, rows, "url", res.Stdout), "url "+s.URL; got != want {
		t.Errorf("url row %q, want %q", got, want)
	}
	statusRows := humanStatusRows(t, repo, wt)
	for _, p := range []portEntry{s.port(t, "web", 80), s.port(t, "db", 3306)} {
		if p.HostPort == 0 {
			t.Fatalf("port %s has no assigned host port: %+v", p.Name, p)
		}
		got := rowLabelled(t, rows, "port "+p.Name, res.Stdout)
		if !slices.Contains(strings.Fields(got), fmt.Sprint(p.HostPort)) {
			t.Errorf("port row %q, want it to name host port %d", got, p.HostPort)
		}
		if !slices.Contains(statusRows, got) {
			t.Errorf("port row %q differs from the one berth status prints:\n%s", got, strings.Join(statusRows, "\n"))
		}
	}
	if got := rowsLabelled(rows, "port search:"+search); len(got) != 0 {
		t.Errorf("summary lists the unassigned port: %q", got)
	}
	assertRowsFromStatus(t, rows, statusRows, res.Stdout)
}

func (s humanStatus) port(t *testing.T, service string, containerPort int) portEntry {
	t.Helper()
	return portsStatus{Ports: s.Ports}.port(t, service, containerPort)
}

// A clone still running when add returns shows as cloning, with a hint to wait.
func TestHumanOutputAddShowsCloningAndHintsWait(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	wt := repo.AddWorktree(t, "feat-summary-clone")
	res := berthWithDelay(t, repo, wt, "20s", "add")
	if res.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s", res.Exit, res.Stderr)
	}
	if e := stateOf(t, repo, wt).volume(t, "cache"); e.Status != "cloning" {
		t.Fatalf("cache is %q right after add, want cloning", e.Status)
	}
	_, rows := summaryOf(t, res.Stdout)
	if v := rowLabelled(t, rows, "volume cache", res.Stdout); !strings.HasPrefix(v, "volume cache cloning") {
		t.Errorf("cache row %q, want it shown as cloning", v)
	}
	next := rowsLabelled(rows, "next")
	if len(next) == 0 || !hintContaining(next, "berth status --wait") {
		t.Errorf("next rows %q, want one naming berth status --wait:\n%s", next, res.Stdout)
	}
	assertRowsFromStatus(t, rows, humanStatusRows(t, repo, wt), res.Stdout)
}

// After add --wait the clone shows as ready and the hint names the URL.
func TestHumanOutputAddWaitShowsReadyAndHintsURL(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	wt := repo.AddWorktree(t, "feat-summary-ready")
	res := mustBerth(t, repo, wt, "add", "--wait")
	s := humanStatusOf(t, repo, wt)
	_, rows := summaryOf(t, res.Stdout)
	if v := rowLabelled(t, rows, "volume cache", res.Stdout); !strings.HasPrefix(v, "volume cache ready") {
		t.Errorf("cache row %q after add --wait, want it shown as ready", v)
	}
	next := rowsLabelled(rows, "next")
	if s.URL == "" || len(next) == 0 || !hintContaining(next, s.URL) {
		t.Errorf("next rows %q, want one naming the berth's URL %q:\n%s", next, s.URL, res.Stdout)
	}
	assertRowsFromStatus(t, rows, humanStatusRows(t, repo, wt), res.Stdout)
}

func TestHumanOutputSecondAddPrintsSameSummaryWithCurrentStates(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	wt := repo.AddWorktree(t, "feat-summary-again")
	firstRes := berthWithDelay(t, repo, wt, "5s", "add")
	if firstRes.Exit != 0 {
		t.Fatalf("berth add: exit %d\n%s", firstRes.Exit, firstRes.Stderr)
	}
	_, firstRows := summaryOf(t, firstRes.Stdout)
	mustBerth(t, repo, wt, "status", "--wait")

	res := mustBerth(t, repo, wt, "add")
	s := humanStatusOf(t, repo, wt)
	first, rows := summaryOf(t, res.Stdout)
	if want := fmt.Sprintf("Already have berth %s for %s", s.ID, s.Worktree); first != want {
		t.Errorf("first line of the second add %q, want %q", first, want)
	}
	if len(rowsLabelled(rows, "name")) != 1 || len(rowsLabelled(rows, "next")) == 0 {
		t.Errorf("second add's summary lacks its name or next rows:\n%s", res.Stdout)
	}
	if v := rowLabelled(t, rows, "volume cache", res.Stdout); !strings.HasPrefix(v, "volume cache ready") {
		t.Errorf("cache row %q once the clone is done, want it shown as ready", v)
	}
	stable := func(rows []string) []string {
		return slices.DeleteFunc(slices.Clone(rows), func(r string) bool {
			return strings.HasPrefix(r, "volume ") || strings.HasPrefix(r, "next ")
		})
	}
	if a, b := stable(firstRows), stable(rows); !slices.Equal(a, b) {
		t.Errorf("second add's summary differs from the first beyond states and hints:\nfirst:\n%s\nsecond:\n%s", strings.Join(a, "\n"), strings.Join(b, "\n"))
	}
	assertRowsFromStatus(t, rows, humanStatusRows(t, repo, wt), res.Stdout)
}

// Add's summary, states included, is plain when stdout is a pipe.
func TestHumanOutputPlainWhenNotATerminal(t *testing.T) {
	repo := NewRepo(t)
	fillCache(t, repo)
	writeFile(t, repo.privateConfig(), "[compose.volumes]\ncache = \"clone\"\n")
	wt := repo.AddWorktree(t, "feat-plain")
	env := []string{"TERM=xterm-256color", "NO_COLOR="}
	res := repo.BerthWith(t, env, wt, "add", "--wait")
	if res.Exit != 0 {
		t.Fatalf("berth add --wait: exit %d\n%s", res.Exit, res.Stderr)
	}
	_, rows := summaryOf(t, res.Stdout)
	if len(rowsLabelled(rows, "volume cache")) != 1 {
		t.Errorf("add's summary has no cache row:\n%s", res.Stdout)
	}
	assertNoEscape(t, "berth add stdout", res.Stdout)
	assertNoEscape(t, "berth add stderr", res.Stderr)
	st := repo.BerthWith(t, env, wt, "status")
	if st.Exit != 0 {
		t.Fatalf("berth status: exit %d\n%s", st.Exit, st.Stderr)
	}
	assertNoEscape(t, "berth status stdout", st.Stdout)
}

// Status off a terminal prints the layout it printed before human output
// was styled, rebuilt here from status --json in that layout.
func TestHumanOutputStatusUnchangedWhenNotATerminal(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-unchanged")
	mustBerth(t, repo, wt, "add")
	s := humanStatusOf(t, repo, wt)
	if len(s.State) != 0 || len(s.PostAdd) != 0 {
		t.Fatalf("fixture berth has state or post-add rows this test cannot rebuild: %+v", s)
	}

	var want strings.Builder
	tw := tabwriter.NewWriter(&want, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "berth\t%s\nname\t%s\nworktree\t%s\nrepository\t%s\nhost\t%s\n", s.ID, s.Name, s.Worktree, s.Repository, s.Host)
	if s.URL != "" {
		fmt.Fprintf(tw, "url\t%s\n", s.URL)
	}
	if s.ComposeProject != "" {
		fmt.Fprintf(tw, "compose project\t%s\n", s.ComposeProject)
	}
	for _, p := range s.Ports {
		host := p.Policy
		if p.HostPort > 0 {
			host = fmt.Sprintf("%d (%s)", p.HostPort, p.Policy)
		}
		fmt.Fprintf(tw, "port %s\t%s\n", p.Name, host)
	}
	for _, k := range sortedKeys(s.Env) {
		fmt.Fprintf(tw, "env %s\t%s\n", k, s.Env[k])
	}
	unresolved := make([]string, 0, len(s.Unresolved))
	for k := range s.Unresolved {
		unresolved = append(unresolved, k)
	}
	sort.Strings(unresolved)
	for _, k := range unresolved {
		fmt.Fprintf(tw, "unresolved %s\t%s\n", k, s.Unresolved[k])
	}
	for _, sh := range s.Shadowed {
		fmt.Fprintf(tw, "shadowed %s\texported as %s in this shell, which overrides the berth's %s\n", sh.Key, sh.Exported, sh.Value)
	}
	fmt.Fprintf(tw, "files\t%s\n", strings.Join(s.Files, ", "))
	for _, b := range s.Builds {
		line := "main's image"
		if !b.Shared {
			line = "builds on up: " + b.Reason
		}
		fmt.Fprintf(tw, "build %s\t%s\n", b.Service, line)
	}
	for _, h := range s.Next {
		fmt.Fprintf(tw, "next\t%s\n", h)
	}
	if err := tw.Flush(); err != nil {
		t.Fatal(err)
	}

	got := repo.BerthWith(t, []string{"TERM=xterm-256color", "NO_COLOR="}, wt, "status")
	if got.Exit != 0 {
		t.Fatalf("berth status: exit %d\n%s", got.Exit, got.Stderr)
	}
	if got.Stdout != want.String() {
		t.Errorf("berth status off a terminal changed:\ngot:\n%q\nwant:\n%q", got.Stdout, want.String())
	}
}

func TestHumanOutputJSONUnchanged(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-json")
	env := []string{"TERM=xterm-256color", "NO_COLOR="}

	keysOf := func(t *testing.T, what, out string) map[string]any {
		t.Helper()
		assertNoEscape(t, what, out)
		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatalf("%s is not JSON: %v\n%s", what, err, out)
		}
		return m
	}
	sortedOf := func(m map[string]any) []string {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys
	}

	first := repo.BerthWith(t, env, wt, "add", "--json")
	if first.Exit != 0 {
		t.Fatalf("berth add --json: exit %d\n%s", first.Exit, first.Stderr)
	}
	added := keysOf(t, "berth add --json", first.Stdout)
	wantAdd := []string{"created", "created_at", "id", "repository", "source", "worktree"}
	if got := sortedOf(added); !slices.Equal(got, wantAdd) {
		t.Errorf("berth add --json keys %v, want %v", got, wantAdd)
	}

	st := repo.BerthWith(t, env, wt, "status", "--json")
	if st.Exit != 0 {
		t.Fatalf("berth status --json: exit %d\n%s", st.Exit, st.Stderr)
	}
	status := keysOf(t, "berth status --json", st.Stdout)
	wantStatus := []string{"builds", "compose_project", "created_at", "env", "files", "host", "id", "name", "next", "ports", "repository", "shadowed", "source", "state", "url", "worktree"}
	if got := sortedOf(status); !slices.Equal(got, wantStatus) {
		t.Errorf("berth status --json keys %v, want %v", got, wantStatus)
	}

	for _, k := range []string{"id", "worktree", "repository", "source", "created_at"} {
		if added[k] != status[k] || added[k] == "" || added[k] == nil {
			t.Errorf("berth add --json %s = %v, want status's %v", k, added[k], status[k])
		}
	}
	if added["created"] != true {
		t.Errorf("first berth add --json created = %v, want true", added["created"])
	}

	again := repo.BerthWith(t, env, wt, "add", "--json")
	if again.Exit != 0 {
		t.Fatalf("second berth add --json: exit %d\n%s", again.Exit, again.Stderr)
	}
	second := keysOf(t, "second berth add --json", again.Stdout)
	if got := sortedOf(second); !slices.Equal(got, wantAdd) {
		t.Errorf("second berth add --json keys %v, want %v", got, wantAdd)
	}
	if second["created"] != false || second["id"] != added["id"] {
		t.Errorf("second berth add --json created = %v, id = %v; want false and %v", second["created"], second["id"], added["id"])
	}
}

func TestHumanOutputHookPrintsPlainSummary(t *testing.T) {
	repo := NewRepo(t)
	mustBerth(t, repo, repo.Dir, "hook", "install")
	path := filepath.Join(filepath.Dir(repo.Dir), "worktrees", "feat-hook-summary")
	res, took := gitWith(t, repo, []string{"TERM=xterm-256color", "NO_COLOR="}, repo.Dir, "worktree", "add", "-b", "feat-hook-summary", path)
	if res.Exit != 0 {
		t.Fatalf("git worktree add: exit %d\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	if took > time.Minute {
		t.Errorf("git worktree add with the hook took %s with stdin empty, want it to wait for no input", took)
	}
	out := res.Stdout + res.Stderr
	assertNoEscape(t, "git worktree add output", out)
	s := humanStatusOf(t, repo, path)
	rows := collapsed(out)
	if !hintContaining(rows, "Added berth "+s.ID) {
		t.Errorf("git worktree add output lacks the Added berth line:\n%s", out)
	}
	if !slices.Contains(rows, "name "+s.Name) {
		t.Errorf("git worktree add output lacks the name row %q:\n%s", "name "+s.Name, out)
	}
	if !slices.Contains(rows, "url "+s.URL) && !slices.Contains(rows, "host "+s.Host) {
		t.Errorf("git worktree add output lacks a url or host row for %s:\n%s", s.Host, out)
	}
}

// A berth with no assigned port gets no port rows and no row without a value.
// The no-URL half is not reachable: every berth resolves a URL.
func TestHumanOutputSummaryWithoutAssignedPortsHasNoEmptyRows(t *testing.T) {
	repo := NewRepo(t)
	search := readDotEnv(t, filepath.Join(repo.Dir, ".env"))["SEARCH_PORT"]
	writeFile(t, repo.privateConfig(), fmt.Sprintf("[compose.ports]\n\"web:80\" = \"none\"\n\"db:3306\" = \"none\"\n\"search:%s\" = \"none\"\n", search))
	wt := repo.AddWorktree(t, "feat-no-ports")
	res := mustBerth(t, repo, wt, "add")
	s := humanStatusOf(t, repo, wt)
	for _, p := range s.Ports {
		if p.HostPort != 0 {
			t.Fatalf("port %s has host port %d, want none assigned", p.Name, p.HostPort)
		}
	}
	_, rows := summaryOf(t, res.Stdout)
	if got := rowLabelled(t, rows, "name", res.Stdout); got != "name "+s.Name {
		t.Errorf("name row %q, want %q", got, "name "+s.Name)
	}
	if s.URL != "" {
		if got := rowLabelled(t, rows, "url", res.Stdout); got != "url "+s.URL {
			t.Errorf("url row %q, want %q", got, "url "+s.URL)
		}
	} else if got := rowLabelled(t, rows, "host", res.Stdout); got != "host "+s.Host {
		t.Errorf("host row %q, want %q", got, "host "+s.Host)
	}
	if got := rowsLabelled(rows, "port"); len(got) != 0 {
		t.Errorf("summary lists ports with no assigned host port: %q", got)
	}
	for _, r := range rows {
		if len(strings.Fields(r)) < 2 {
			t.Errorf("summary row %q has no value:\n%s", r, res.Stdout)
		}
	}
	assertRowsFromStatus(t, rows, humanStatusRows(t, repo, wt), res.Stdout)
}
