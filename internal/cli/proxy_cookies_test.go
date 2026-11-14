package cli

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kodnificent/berth/internal/proxy"
	"github.com/kodnificent/berth/internal/registry"
)

var proxiedWideCookie = proxy.SetCookie{Name: "wide_session", Domain: ".myapp.localhost", URL: "https://feat-x.myapp.localhost/login", Wide: true}

func cookieRegistry(t *testing.T) (state string, rec registry.Record) {
	t.Helper()
	root, state := fakeRegistry(t)
	repo := fakeMain(t, root, "myapp", "", "")
	fakeBerth(t, state, repo, "a1", "feat-x", "feat-x.myapp.localhost", hostConfig)
	mustWrite(t, filepath.Join(state, "lock"), "")
	return state, registry.Record{ID: "a1", Name: "feat-x", Host: "feat-x.myapp.localhost"}
}

func TestDoctorReportsAWideCookieTheProxyRecorded(t *testing.T) {
	_, rec := cookieRegistry(t)
	newCookieWatch(io.Discard).apply(cookieJob{"a1", proxiedWideCookie})

	found := proxyCookieFindings(rec, nil)

	if len(found) != 1 || found[0].Check != "cookie_domain" || !strings.Contains(found[0].Message, "wide_session") || !strings.Contains(found[0].Message, ".myapp.localhost") {
		t.Errorf("doctor reports %+v, want one cookie_domain finding naming wide_session and .myapp.localhost", found)
	}
}

func TestTheProxyForgetsACookieTheAppNowScopesToTheBerth(t *testing.T) {
	_, rec := cookieRegistry(t)
	w := newCookieWatch(io.Discard)
	w.apply(cookieJob{"a1", proxiedWideCookie})

	w.apply(cookieJob{"a1", proxy.SetCookie{Name: "wide_session", Domain: "feat-x.myapp.localhost", URL: proxiedWideCookie.URL}})

	if found := proxyCookieFindings(rec, nil); len(found) != 0 {
		t.Errorf("after the app set wide_session within the berth's host, doctor still reports %+v", found)
	}
}

// A response in flight when the berth is removed must not leave a record of the berth's
// cookies behind it.
func TestTheProxyRecordsNothingForABerthNoLongerRecorded(t *testing.T) {
	state, _ := cookieRegistry(t)
	before := treeOf(t, state)

	newCookieWatch(io.Discard).apply(cookieJob{"b2", proxiedWideCookie})

	if after := treeOf(t, state); !reflect.DeepEqual(after, before) {
		t.Errorf("the state directory changed for a berth with no record: %v", after)
	}
}

func TestTheProxyCreatesNoRegistryLockToRecordACookie(t *testing.T) {
	state, _ := cookieRegistry(t)
	if err := os.Remove(filepath.Join(state, "lock")); err != nil {
		t.Fatal(err)
	}
	before := treeOf(t, state)

	newCookieWatch(io.Discard).apply(cookieJob{"a1", proxiedWideCookie})

	if after := treeOf(t, state); !reflect.DeepEqual(after, before) {
		t.Errorf("recording a cookie with no registry lock changed the state directory: %v", after)
	}
}
