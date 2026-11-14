package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/worktree"
)

const proxyPorts = "[berth]\nhost = \"{name}.myapp.localhost\"\n[compose.ports]\n\"web:80\" = \"assign\"\n[ports]\nvite = { env = \"VITE_PORT\" }\n"

func TestProxyRoutesOutsideTheBerthsHostAreAConfigError(t *testing.T) {
	for _, route := range []string{"vite.myapp.localhost", "vite-{host}", "{host}", "vite.{name}.other.localhost", "*.{host}"} {
		paths := layers(t, proxyPorts+"[proxy.routes]\n\""+route+"\" = \"vite\"\n")
		msg := loadErr(t, paths...)
		if !strings.Contains(msg, paths[0]+": proxy.routes.") {
			t.Errorf("route %s: error %q does not name the file and proxy.routes", route, msg)
		}
	}
}

func TestProxyPortsNamingNoDefinedPortAreAConfigError(t *testing.T) {
	for _, cfg := range []string{"[proxy]\nport = \"api\"\n", "[proxy.routes]\n\"api.{host}\" = \"api\"\n"} {
		if msg := loadErr(t, layers(t, proxyPorts+cfg)...); !strings.Contains(msg, `"api" names no defined port`) {
			t.Errorf("%s: error %q does not name the undefined port", cfg, msg)
		}
	}
}

// loadLayers loads a linked worktree's config with each layer's file written when its
// content isn't empty, and returns the paths of the personal and repository files.
func loadLayers(t *testing.T, personal, repo, user string) (personalPath, repoPath string, err error) {
	t.Helper()
	root := t.TempDir()
	main := filepath.Join(root, "myapp")
	w := &worktree.Worktree{Path: filepath.Join(root, "feat-x"), AdminDir: filepath.Join(main, ".git", "worktrees", "feat-x"), CommonDir: filepath.Join(main, ".git"), Repository: main}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	personalPath, repoPath = filepath.Join(main, LocalName), filepath.Join(w.Path, "berth.toml")
	userPath := filepath.Join(root, "config", "berth", "config.toml")
	for path, data := range map[string]string{personalPath: personal, repoPath: repo, userPath: user} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if data != "" {
			if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.MkdirAll(w.CommonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = Load(w)
	return personalPath, repoPath, err
}

func TestProxyListenPortsAreAConfigErrorOutsideTheUserConfig(t *testing.T) {
	for _, layer := range []string{"personal", "repository"} {
		personal, repo := "", ""
		if layer == "personal" {
			personal = "[proxy]\nhttp = 8080\n"
		} else {
			repo = "[proxy]\nhttp = 8080\n"
		}

		personalPath, repoPath, err := loadLayers(t, personal, repo, "")

		file := map[string]string{"personal": personalPath, "repository": repoPath}[layer]
		if apperr.CodeOf(err) != apperr.ConfigInvalid || !strings.Contains(fmt.Sprint(err), file+": proxy.http") {
			t.Errorf("[proxy] http in the %s config: error %v, want config_invalid naming %s and proxy.http", layer, err, file)
		}
	}
}

func TestProxyListenPortsInTheUserConfigLoad(t *testing.T) {
	if _, _, err := loadLayers(t, "", "", "[proxy]\nhttp = 8080\nhttps = 8443\n"); err != nil {
		t.Errorf("[proxy] http and https in the user config: %v, want them accepted", err)
	}
}

// A user config may hold [proxy] port and routes as defaults for every project, which
// only a project's own ports can make sense of.
func TestProxyListenPortIsReadFromAUserConfigHoldingProjectDefaults(t *testing.T) {
	paths := layers(t, "[proxy]\nhttp = 8080\nhttps = 8443\nport = \"web\"\n\n[proxy.routes]\n\"assets.{host}\" = \"assets\"\n")

	port, _, err := ProxyListenPorts(paths[0])

	if err != nil || port != 8080 {
		t.Errorf("port %d, error %v; want 8080", port, err)
	}
}

func TestProxyRoutesExpandForTheBerth(t *testing.T) {
	c := mustLoad(t, layers(t, proxyPorts+"[proxy.routes]\n\"vite.{host}\" = \"vite\"\n\"api-{name}.{host}\" = \"web\"\n")...)

	got := c.ProxyRoutes("feat-x", "feat-x.myapp.localhost")

	want := map[string]string{"vite.feat-x.myapp.localhost": "vite", "api-feat-x.feat-x.myapp.localhost": "web"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("routes %v, want %v", got, want)
	}
}

func TestProxyCookiesOtherThanWarnOrRewriteIsAConfigError(t *testing.T) {
	if msg := loadErr(t, layers(t, proxyPorts+"[proxy]\ncookies = \"rename\"\n")...); !strings.Contains(msg, "proxy.cookies") {
		t.Errorf("cookies = \"rename\": error %q does not name proxy.cookies", msg)
	}
}

func TestProxyHTTPOnTheDefaultHTTPSPortIsAConfigError(t *testing.T) {
	paths := layers(t, "[proxy]\nhttp = 443\n")

	if _, _, err := ProxyListenPorts(paths[0]); apperr.CodeOf(err) != apperr.ConfigInvalid {
		t.Errorf("[proxy] http = 443 with https left at 443: %v, want config_invalid", err)
	}
}
