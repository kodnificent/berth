//go:build e2e

package e2e

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stateFilesNaming lists the files in berth's state directory that mention s.
func stateFilesNaming(t *testing.T, repo *Repo, s string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(repo.StateDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), s) {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestRemoveLeavesNoRecordOfTheCookiesTheProxySaw(t *testing.T) {
	repo := NewRepo(t)
	b := startCookieBerths(t, repo, "", map[string]http.HandlerFunc{
		"/login": setCookie("wide_session=v; Domain=.myapp.localhost; Path=/"),
	})
	b.p.mustGetTLS(t, b.p.client(nil), "https://feat-x.myapp.localhost/login")
	waitForCookieFinding(t, repo, b.featX, "wide_session")

	mustBerth(t, repo, b.featX, "remove")

	if left := stateFilesNaming(t, repo, "wide_session"); len(left) != 0 {
		t.Errorf("after berth remove, berth's state directory still records feat-x's cookie in %v", left)
	}
}

func TestDoctorForgetsAWideCookieOnceTheAppScopesItToTheBerth(t *testing.T) {
	repo := NewRepo(t)
	b := startCookieBerths(t, repo, "", map[string]http.HandlerFunc{
		"/login": setCookie("wide_session=v; Domain=.myapp.localhost; Path=/"),
		"/fixed": setCookie("wide_session=v; Domain=feat-x.myapp.localhost; Path=/"),
	})
	client := b.p.client(nil)
	b.p.mustGetTLS(t, client, "https://feat-x.myapp.localhost/login")
	waitForCookieFinding(t, repo, b.featX, "wide_session")

	b.p.mustGetTLS(t, client, "https://feat-x.myapp.localhost/fixed")

	deadline := time.Now().Add(5 * time.Second)
	for {
		d := probeDoctor(t, repo, b.featX)
		still := false
		for _, f := range d.with("cookie_domain") {
			still = still || strings.Contains(f.Message, "wide_session")
		}
		if !still {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("doctor still reports wide_session 5s after the app set it within the berth's host:\n%s", d.Raw)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
