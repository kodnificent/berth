//go:build e2e

package e2e

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type finding struct {
	Check    string `json:"check"`
	Location string `json:"location"`
	Message  string `json:"message"`
}

type doctorResult struct {
	Findings []finding `json:"findings"`
	Exit     int
	Raw      string
}

func doctor(t *testing.T, repo *Repo, env []string, dir string) doctorResult {
	t.Helper()
	res := repo.BerthWith(t, env, dir, "doctor", "--json")
	var d doctorResult
	if err := json.Unmarshal([]byte(res.Stdout), &d); err != nil {
		t.Fatalf("berth doctor --json: %v\n%s%s", err, res.Stdout, res.Stderr)
	}
	if d.Findings == nil {
		t.Fatalf("berth doctor --json has no findings list:\n%s", res.Stdout)
	}
	for _, f := range d.Findings {
		if f.Check == "" || f.Location == "" || f.Message == "" {
			t.Errorf("doctor finding lacks a check, location or message: %+v", f)
		}
	}
	d.Exit, d.Raw = res.Exit, res.Stdout
	return d
}

func (d doctorResult) with(check string) []finding {
	var out []finding
	for _, f := range d.Findings {
		if f.Check == check {
			out = append(out, f)
		}
	}
	return out
}

// expectFinding checks doctor reports a problem with check whose location contains
// where, and exits non-zero.
func expectFinding(t *testing.T, d doctorResult, check, where string) {
	t.Helper()
	if d.Exit == 0 {
		t.Errorf("doctor exited 0 although it should report %s at %s:\n%s", check, where, d.Raw)
	}
	for _, f := range d.with(check) {
		if strings.Contains(f.Location, where) {
			return
		}
	}
	t.Errorf("doctor reports no %s finding at %s:\n%s", check, where, d.Raw)
}

func expectClean(t *testing.T, d doctorResult, when string) {
	t.Helper()
	if d.Exit != 0 || len(d.Findings) != 0 {
		t.Errorf("doctor %s: exit %d with findings, want exit 0 and none:\n%s", when, d.Exit, d.Raw)
	}
}

// With COMPOSE_PROJECT_NAME or any managed key exported in the shell, berth
// status and berth doctor both report it as shadowing the berth's value.
func TestShadowedKeysAreReported(t *testing.T) {
	repo := NewRepo(t)
	writeFile(t, repo.privateConfig(), "[env.set]\nGREETING = \"hello\"\n")
	wt := repo.AddWorktree(t, "feat-shadow")
	mustBerth(t, repo, wt, "add")
	project := stateOf(t, repo, wt).ComposeProject
	exported := []string{"COMPOSE_PROJECT_NAME=elsewhere", "GREETING=bye"}

	res := repo.BerthWith(t, exported, wt, "status", "--json")
	if res.Exit != 0 {
		t.Fatalf("status with shadowed keys: exit %d\n%s", res.Exit, res.Stderr)
	}
	var s struct {
		Shadowed []struct {
			Key      string `json:"key"`
			Exported string `json:"exported"`
			Value    string `json:"value"`
		} `json:"shadowed"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &s); err != nil {
		t.Fatalf("berth status --json: %v\n%s", err, res.Stdout)
	}
	want := map[string][2]string{"COMPOSE_PROJECT_NAME": {"elsewhere", project}, "GREETING": {"bye", "hello"}}
	got := map[string][2]string{}
	for _, e := range s.Shadowed {
		got[e.Key] = [2]string{e.Exported, e.Value}
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("status reports %s shadowed as %v, want exported %q over the berth's %q\n%s", k, got[k], v[0], v[1], res.Stdout)
		}
	}
	human := repo.BerthWith(t, exported, wt, "status")
	for k := range want {
		if !strings.Contains(human.Stdout, k) || !strings.Contains(human.Stdout, "shadow") {
			t.Errorf("human status doesn't report %s as shadowed:\n%s", k, human.Stdout)
		}
	}

	d := doctor(t, repo, exported, wt)
	for k := range want {
		expectFinding(t, d, "shadowing", k)
	}

	same := []string{"COMPOSE_PROJECT_NAME=" + project, "GREETING=hello"}
	res = repo.BerthWith(t, same, wt, "status", "--json")
	s.Shadowed = nil
	if err := json.Unmarshal([]byte(res.Stdout), &s); err != nil {
		t.Fatalf("berth status --json: %v\n%s", err, res.Stdout)
	}
	if len(s.Shadowed) != 0 {
		t.Errorf("status reports keys exported with the berth's own values as shadowed: %+v", s.Shadowed)
	}
	expectClean(t, doctor(t, repo, same, wt), "with managed keys exported at the berth's own values")
}

// berth doctor reports invalid config, an invalid berth compose
// project, a hardcoded container_name and an assigned port now taken by another
// process; it exits 0 when it finds nothing and non-zero when it reports a problem, and
// --json lists each finding with its check, location and message.
func TestDoctorReportsProblems(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-doctor")
	mustBerth(t, repo, wt, "add")
	expectClean(t, doctor(t, repo, nil, wt), "on a fresh berth")
	if res := mustBerth(t, repo, wt, "doctor"); res.Exit != 0 {
		t.Errorf("human doctor on a fresh berth: exit %d", res.Exit)
	}

	writeFile(t, repo.privateConfig(), "[berth]\nhots = \"{name}.localhost\"\n")
	expectFinding(t, doctor(t, repo, nil, wt), "config", repo.privateConfig())
	writeFile(t, repo.privateConfig(), "")
	expectClean(t, doctor(t, repo, nil, wt), "after the config is fixed")

	override := filepath.Join(wt, "compose.berth.yml")
	good := readString(t, override)
	writeFile(t, override, good+"services:\n  - not a map\n")
	expectFinding(t, doctor(t, repo, nil, wt), "compose", wt)
	writeFile(t, override, good)
	expectClean(t, doctor(t, repo, nil, wt), "after compose.berth.yml is restored")

	composeFile := filepath.Join(wt, "compose.yml")
	original := readString(t, composeFile)
	fixed := strings.Replace(original, "\n  search:\n", "\n  search:\n    container_name: "+repo.Project+"-fixed-search\n", 1)
	if fixed == original {
		t.Fatalf("fixture compose.yml has no search service to give a container_name")
	}
	writeFile(t, composeFile, fixed)
	d := doctor(t, repo, nil, wt)
	expectFinding(t, d, "container_name", "compose.yml")
	if fs := d.with("container_name"); len(fs) > 0 && !strings.Contains(fs[0].Message, repo.Project+"-fixed-search") {
		t.Errorf("container_name finding doesn't name the container: %+v", fs[0])
	}
	writeFile(t, composeFile, original)
	expectClean(t, doctor(t, repo, nil, wt), "after compose.yml is restored")

	port := portsStatusOf(t, repo, wt).port(t, "web", 80).HostPort
	l, err := net.Listen("tcp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("listening on the berth's web port %d: %v", port, err)
	}
	expectFinding(t, doctor(t, repo, nil, wt), "ports", strconv.Itoa(port))
	l.Close()
	expectClean(t, doctor(t, repo, nil, wt), "after the port is released")

	repo.Compose(t, wt, "up", "-d", "--no-deps", "search")
	expectClean(t, doctor(t, repo, nil, wt), "while the berth's own container publishes its ports")
}

// berth doctor reports cloned text files containing the source worktree's
// absolute path, including its resolved and JSON-escaped forms.
func TestDoctorFindsSourcePath(t *testing.T) {
	repo := NewRepo(t)
	resolved := repo.Dir
	writeFile(t, filepath.Join(repo.Dir, "leaks", "config.php"), "<?php\nreturn ['base' => '"+resolved+"/storage'];\n")
	writeFile(t, filepath.Join(repo.Dir, "leaks", "manifest.json"), `{"root": "`+strings.ReplaceAll(resolved, "/", `\/`)+`"}`+"\n")
	writeFile(t, filepath.Join(repo.Dir, "leaks", "clean.txt"), "nothing to see\n")
	unresolved, hasAlias := strings.CutPrefix(resolved, "/private")
	if hasAlias {
		if real, err := filepath.EvalSymlinks(unresolved); err != nil || real != resolved {
			hasAlias = false
		}
	}
	if hasAlias {
		writeFile(t, filepath.Join(repo.Dir, "leaks", "alias.txt"), "one\ntwo\ncd "+unresolved+"\n")
	} else {
		t.Logf("%s has no /private alias here; not checking the unresolved form", resolved)
	}
	writeFile(t, repo.privateConfig(), "[files]\nclone = [\"leaks\"]\n")
	wt := repo.AddWorktree(t, "feat-leak")
	mustBerth(t, repo, wt, "add")

	d := doctor(t, repo, nil, wt)
	expectFinding(t, d, "source_path", "leaks/config.php:2")
	expectFinding(t, d, "source_path", "leaks/manifest.json:1")
	if hasAlias {
		expectFinding(t, d, "source_path", "leaks/alias.txt:3")
	}
	for _, f := range d.with("source_path") {
		if strings.Contains(f.Location, "clean.txt") {
			t.Errorf("doctor reports a file without the source path: %+v", f)
		}
	}

	for _, f := range []string{"config.php", "manifest.json", "alias.txt"} {
		_ = os.Remove(filepath.Join(wt, "leaks", f))
	}
	expectClean(t, doctor(t, repo, nil, wt), "once the leaking files are gone")
}

// berth doctor in the main worktree checks the config and the compose files a berth
// would use, so a config can be checked before the first berth exists, and creates
// nothing.
func TestDoctorChecksMainBeforeAnyBerth(t *testing.T) {
	repo := NewRepo(t)
	expectClean(t, doctor(t, repo, nil, repo.Dir), "in main with no config")
	if res := mustBerth(t, repo, repo.Dir, "doctor"); !strings.Contains(res.Stdout, "No problems found") {
		t.Errorf("human doctor in main: %q", res.Stdout)
	}

	writeFile(t, repo.privateConfig(), "[berth]\nhots = \"{name}.localhost\"\n")
	d := doctor(t, repo, nil, repo.Dir)
	expectFinding(t, d, "config", repo.privateConfig())
	if d.Exit != 1 {
		t.Errorf("doctor with an invalid config exited %d, want 1 for problems found", d.Exit)
	}

	writeFile(t, repo.privateConfig(), "[compose]\nfiles = [\"berth-only.yml\"]\n")
	writeFile(t, filepath.Join(repo.Dir, "berth-only.yml"), "services:\n  db:\n    ports: not a list\n")
	expectFinding(t, doctor(t, repo, nil, repo.Dir), "compose", "berth-only.yml")

	writeFile(t, filepath.Join(repo.Dir, "berth-only.yml"), "services:\n  db:\n    container_name: "+repo.Project+"-fixed-db\n")
	expectFinding(t, doctor(t, repo, nil, repo.Dir), "container_name", "berth-only.yml")

	writeFile(t, filepath.Join(repo.Dir, "berth-only.yml"), "services:\n  db:\n    environment:\n      SMALL: \"1\"\n")
	writeFile(t, repo.privateConfig(), `[compose]
files = ["berth-only.yml"]
exclude = ["ghost"]

[compose.ports]
"phantom:80" = "assign"
"web:80" = "assign"

[compose.volumes]
db-data = "clone"
nowhere = "fresh"
`)
	d = doctor(t, repo, nil, repo.Dir)
	for _, where := range []string{"exclude", "phantom:80", "nowhere"} {
		expectFinding(t, d, "config", where)
	}
	if n := len(d.Findings); n != 3 {
		t.Errorf("doctor reports %d findings, want only the 3 bad references:\n%s", n, d.Raw)
	}

	writeFile(t, repo.privateConfig(), "[compose]\nfiles = [\"berth-only.yml\"]\n")
	expectClean(t, doctor(t, repo, nil, repo.Dir), "in main after the config is fixed")
	if berths := list(t, repo); len(berths) != 0 {
		t.Errorf("doctor in main created berths: %+v", berths)
	}
}

// berth doctor warns, without failing, about build-time env keys the berth sets apart
// from main, and about small gitignored files at main's top level the berth lacks.
func TestDoctorWarnsAboutBuildEnvAndIgnoredFiles(t *testing.T) {
	repo := NewRepo(t)
	appendFile(t, filepath.Join(repo.Dir, ".env"), "VITE_API_URL=http://localhost:8000\nVITE_SAME=same\n")
	appendFile(t, filepath.Join(repo.Dir, ".gitignore"), "local.xml\nbig.json\ndebug.log\n.toolcache\nsub/\n")
	repo.Git(t, "commit", "-q", "-am", "ignore local files")
	writeFile(t, filepath.Join(repo.Dir, "local.xml"), "<config/>\n")
	writeFile(t, filepath.Join(repo.Dir, "big.json"), strings.Repeat("x", 100<<10))
	writeFile(t, filepath.Join(repo.Dir, "debug.log"), "log\n")
	writeFile(t, filepath.Join(repo.Dir, ".toolcache"), "cache\n")
	writeFile(t, filepath.Join(repo.Dir, "sub", "nested.xml"), "<config/>\n")
	writeFile(t, repo.privateConfig(), "[env.set]\nVITE_API_URL = \"{url}\"\nVITE_SAME = \"same\"\nAPP_NAME = \"berth\"\n")

	wt := repo.AddWorktree(t, "feat-warn")
	mustBerth(t, repo, wt, "add")
	d := doctor(t, repo, nil, wt)
	if d.Exit != 0 {
		t.Errorf("doctor with only warnings exited %d, want 0:\n%s", d.Exit, d.Raw)
	}
	build := d.with("build_env")
	if len(build) != 1 || !strings.Contains(build[0].Message, "VITE_API_URL") || strings.Contains(build[0].Message, "VITE_SAME") || strings.Contains(build[0].Message, "APP_NAME") {
		t.Errorf("build_env findings = %+v, want one naming VITE_API_URL only", build)
	}
	var ignored []string
	for _, f := range d.with("ignored_file") {
		ignored = append(ignored, f.Location)
	}
	if strings.Join(ignored, ",") != "local.xml" {
		t.Errorf("ignored_file findings at %v, want only local.xml:\n%s", ignored, d.Raw)
	}
	if human := mustBerth(t, repo, wt, "doctor").Stdout; !strings.Contains(human, "(warning)") {
		t.Errorf("human doctor doesn't mark warnings:\n%s", human)
	}

	writeFile(t, repo.privateConfig(), "[files]\nclone = [\"local.xml\"]\n")
	mustBerth(t, repo, wt, "add")
	expectClean(t, doctor(t, repo, nil, wt), "once local.xml is cloned and no build-time key differs")
}
