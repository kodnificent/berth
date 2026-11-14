//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The registry lives under $XDG_STATE_HOME/berth when it is set.
func TestRegistryHonoursXDGStateHome(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-xdg-state")
	stateHome := t.TempDir()
	env := []string{"BERTH_STATE_DIR=", "XDG_STATE_HOME=" + stateHome}

	if res := repo.BerthWith(t, env, wt, "add"); res.Exit != 0 {
		t.Fatalf("add with XDG_STATE_HOME: exit %d\n%s", res.Exit, res.Stderr)
	}
	if entries, err := os.ReadDir(filepath.Join(stateHome, "berth")); err != nil || len(entries) == 0 {
		t.Errorf("add with XDG_STATE_HOME=%s left nothing under %s/berth (err %v)", stateHome, stateHome, err)
	}
	if n := countBerths(t, repo, env); n != 1 {
		t.Errorf("list with the same XDG_STATE_HOME shows %d berths, want the one add made", n)
	}
	if n := countBerths(t, repo, []string{"BERTH_STATE_DIR=", "XDG_STATE_HOME=" + t.TempDir()}); n != 0 {
		t.Errorf("list with another XDG_STATE_HOME shows %d berths, want none", n)
	}
}

// homeEnv points HOME at a fresh directory with no XDG variables, keeping Docker's own
// config where it really is, since Docker reads its contexts from HOME.
func homeEnv(t *testing.T) (string, []string) {
	t.Helper()
	home := t.TempDir()
	dockerConfig := os.Getenv("DOCKER_CONFIG")
	if dockerConfig == "" {
		realHome, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		dockerConfig = filepath.Join(realHome, ".docker")
	}
	// Windows finds the home directory in USERPROFILE, and keeps state in LOCALAPPDATA
	// when that is set.
	return home, []string{"HOME=" + home, "USERPROFILE=" + home, "LOCALAPPDATA=", "DOCKER_CONFIG=" + dockerConfig, "BERTH_STATE_DIR=", "XDG_STATE_HOME=", "XDG_CONFIG_HOME="}
}

// With no XDG_STATE_HOME, the registry lives under ~/.local/state/berth.
func TestRegistryDefaultsToLocalState(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-local-state")
	home, env := homeEnv(t)

	if res := repo.BerthWith(t, env, wt, "add"); res.Exit != 0 {
		t.Fatalf("add with HOME=%s: exit %d\n%s", home, res.Exit, res.Stderr)
	}
	dir := filepath.Join(home, ".local", "state", "berth")
	if entries, err := os.ReadDir(dir); err != nil || len(entries) == 0 {
		t.Errorf("add with no XDG_STATE_HOME left nothing under %s (err %v)", dir, err)
	}
	if n := countBerths(t, repo, env); n != 1 {
		t.Errorf("list with the same HOME shows %d berths, want the one add made", n)
	}
}

// With no XDG_CONFIG_HOME, the user config is read from
// ~/.config/berth/config.toml.
func TestUserConfigDefaultsToDotConfig(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-dot-config")
	home, env := homeEnv(t)
	writeFile(t, filepath.Join(home, ".config", "berth", "config.toml"), "[berth]\nhost = \"{name}.fromhome.localhost\"\n")

	if res := repo.BerthWith(t, env, wt, "add"); res.Exit != 0 {
		t.Fatalf("add with HOME=%s: exit %d\n%s", home, res.Exit, res.Stderr)
	}
	res := repo.BerthWith(t, env, wt, "status", "--json")
	if !strings.Contains(res.Stdout, ".fromhome.localhost") {
		t.Errorf("status does not use the host from ~/.config/berth/config.toml:\n%s%s", res.Stdout, res.Stderr)
	}
}

// XDG_CONFIG_HOME, when set, wins over ~/.config.
func TestUserConfigHonoursXDGConfigHome(t *testing.T) {
	repo := NewRepo(t)
	wt := repo.AddWorktree(t, "feat-xdg-config")
	home, env := homeEnv(t)
	writeFile(t, filepath.Join(home, ".config", "berth", "config.toml"), "[berth]\nhost = \"{name}.fromhome.localhost\"\n")
	configHome := t.TempDir()
	writeFile(t, filepath.Join(configHome, "berth", "config.toml"), "[berth]\nhost = \"{name}.fromxdg.localhost\"\n")
	env = append(env, "XDG_CONFIG_HOME="+configHome)

	if res := repo.BerthWith(t, env, wt, "add"); res.Exit != 0 {
		t.Fatalf("add with XDG_CONFIG_HOME: exit %d\n%s", res.Exit, res.Stderr)
	}
	res := repo.BerthWith(t, env, wt, "status", "--json")
	if !strings.Contains(res.Stdout, ".fromxdg.localhost") {
		t.Errorf("status does not use the host from $XDG_CONFIG_HOME/berth/config.toml:\n%s%s", res.Stdout, res.Stderr)
	}
}

func countBerths(t *testing.T, repo *Repo, env []string) int {
	t.Helper()
	res := repo.BerthWith(t, env, repo.Dir, "list", "--json")
	if res.Exit != 0 {
		t.Fatalf("list: exit %d\n%s", res.Exit, res.Stderr)
	}
	var l struct {
		Berths []listedBerth `json:"berths"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &l); err != nil {
		t.Fatalf("berth list --json: %v\n%s", err, res.Stdout)
	}
	return len(l.Berths)
}
