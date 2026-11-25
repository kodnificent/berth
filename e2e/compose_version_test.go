//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// CI installs one compose, so a docker first on PATH answers docker compose version
// with the given sh and passes every other command to the real docker.
func composeVersionEnv(t *testing.T, answer string) []string {
	t.Helper()
	realDocker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	shim := t.TempDir()
	writeFile(t, filepath.Join(shim, "docker"), `#!/bin/sh
if [ "$1" = compose ] && [ "$2" = version ]; then `+answer+`; fi
exec `+shellQuote(realDocker)+` "$@"
`)
	if err := makeExecutable(filepath.Join(shim, "docker")); err != nil {
		t.Fatal(err)
	}
	return []string{"PATH=" + shim + string(os.PathListSeparator) + os.Getenv("PATH")}
}

func reportsCompose(version string) string { return "echo " + shellQuote(version) + "; exit 0" }

func composeVersionFindings(t *testing.T, d doctorResult) []struct{ Level, Message string } {
	t.Helper()
	var all struct {
		Findings []struct {
			Check, Level, Message string
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(d.Raw), &all); err != nil {
		t.Fatalf("berth doctor --json: %v\n%s", err, d.Raw)
	}
	var out []struct{ Level, Message string }
	for _, f := range all.Findings {
		if f.Check == "compose_version" {
			out = append(out, struct{ Level, Message string }{f.Level, f.Message})
		}
	}
	return out
}

// Before 2.24.4, compose adds main's published ports to the berth's: add refuses it
// before changing anything, and doctor reports it in main and in a berth.
func TestComposeTooOldForTheOverrideIsRefused(t *testing.T) {
	repo := NewRepo(t)
	old := composeVersionEnv(t, reportsCompose("2.18.1"))

	expectFinding(t, doctor(t, repo, old, repo.Dir), "compose_version", "docker compose version")

	wt := repo.AddWorktree(t, "feat-old-compose")
	res := repo.BerthWith(t, old, wt, "add", "--json")
	var e hintedError
	if err := json.Unmarshal([]byte(res.Stdout), &e); err != nil || e.Error == nil {
		t.Fatalf("berth add --json with compose 2.18.1: exit %d, want an error envelope: %v\n%s%s", res.Exit, err, res.Stdout, res.Stderr)
	}
	if res.Exit != 1 || e.Error.Code != "compose_too_old" {
		t.Fatalf("berth add with compose 2.18.1: exit %d, code %q, want 1 and compose_too_old", res.Exit, e.Error.Code)
	}
	if !strings.Contains(e.Error.Message, "2.18.1") || !strings.Contains(e.Error.Message, "2.24.4") {
		t.Errorf("message %q doesn't name the version found and the one berth needs", e.Error.Message)
	}
	if e.Error.Next == nil || !hintContaining(*e.Error.Next, "2.24.4") {
		t.Errorf("compose_too_old carries next %v, want a hint naming 2.24.4", e.Error.Next)
	}
	for _, f := range []string{"compose.berth.yml", ".env"} {
		if fileExistsAt(filepath.Join(wt, f)) {
			t.Errorf("the refused berth add wrote %s", f)
		}
	}
	if berths := list(t, repo); len(berths) != 0 {
		t.Errorf("the refused berth add made a berth: %+v", berths)
	}

	mustBerthWith(t, repo, composeVersionEnv(t, reportsCompose("2.24.4")), wt, "add")
	if f := doctor(t, repo, nil, wt).with("compose_version"); len(f) != 0 {
		t.Errorf("doctor with the real compose reports %+v", f)
	}
	expectFinding(t, doctor(t, repo, old, wt), "compose_version", "docker compose version")
}

// When berth can't tell the version, compose itself reports what's wrong when it runs.
func TestComposeVersionBerthCantTellDoesNotBlockAdd(t *testing.T) {
	cases := []struct{ name, answer string }{
		{"no-compose", `echo "docker: 'compose' is not a docker command." >&2; exit 1`},
		{"not-a-version", reportsCompose("dev")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := NewRepo(t)
			env := composeVersionEnv(t, c.answer)
			wt := repo.AddWorktree(t, "feat-"+c.name)
			mustBerthWith(t, repo, env, wt, "add")
			for _, dir := range []string{repo.Dir, wt} {
				found := composeVersionFindings(t, doctor(t, repo, env, dir))
				if len(found) != 1 || found[0].Level != "not_verified" {
					t.Errorf("doctor in %s reports compose_version %+v, want one not_verified finding", dir, found)
				}
			}
		})
	}
}

func TestComposeVersionIsNotCheckedWithoutComposeFiles(t *testing.T) {
	repo := composelessRepo(t)
	old := composeVersionEnv(t, reportsCompose("2.18.1"))
	wt := repo.AddWorktree(t, "feat-no-compose")
	mustBerthWith(t, repo, old, wt, "add")
	for _, dir := range []string{repo.Dir, wt} {
		if found := composeVersionFindings(t, doctor(t, repo, old, dir)); len(found) != 0 {
			t.Errorf("doctor in %s, with no compose files, reports compose_version %+v", dir, found)
		}
	}
}

func mustBerthWith(t *testing.T, repo *Repo, env []string, dir string, args ...string) Result {
	t.Helper()
	res := repo.BerthWith(t, env, dir, args...)
	if res.Exit != 0 {
		t.Fatalf("berth %v (in %s): exit %d\nstdout:\n%s\nstderr:\n%s", args, dir, res.Exit, res.Stdout, res.Stderr)
	}
	return res
}
