package cli

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/kodnificent/berth/internal/apperr"
)

var dockerNotFound = &exec.Error{Name: "docker", Err: exec.ErrNotFound}

// Only a version berth can read and knows to be too old stops add: a compose that
// doesn't answer, or answers with something else, is left for compose itself to report.
func TestComposeTooOldRefusesOnlyAKnownOldVersion(t *testing.T) {
	err := composeTooOld("2.24.3", nil)
	if apperr.CodeOf(err) != apperr.ComposeTooOld {
		t.Fatalf("composeTooOld(2.24.3) = %v, want a compose_too_old error", err)
	}
	for _, want := range []string{"2.24.3", "2.24.4", OverrideFile, "main's ports"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q doesn't name %q", err.Error(), want)
		}
	}
	next := apperr.NextOf(err)
	if len(next) != 1 || !strings.Contains(next[0], "2.24.4") || !strings.Contains(next[0], "berth add") {
		t.Errorf("next = %q, want one hint naming 2.24.4 and berth add", next)
	}

	for _, c := range []struct {
		version string
		err     error
	}{
		{"2.24.4", nil},
		{"2.24.6-desktop.1", nil},
		{"5.8.0", nil},
		{"dev", nil},
		{"", errors.New("docker: 'compose' is not a docker command")},
		{"", dockerNotFound},
	} {
		if err := composeTooOld(c.version, c.err); err != nil {
			t.Errorf("composeTooOld(%q, %v) = %v, want nil", c.version, c.err, err)
		}
	}
}

func TestComposeVersionFindings(t *testing.T) {
	cases := []struct {
		name    string
		version string
		err     error
		level   string
		names   []string
	}{
		{"too old", "2.18.1", nil, problem, []string{"2.18.1", "2.24.4", "github.com/docker/compose/releases"}},
		{"docker not found", "", dockerNotFound, notVerified, []string{"could not run docker compose version", "executable file not found", "2.24.4"}},
		{"no compose plugin", "", errors.New("docker: 'compose' is not a docker command"), notVerified, []string{"could not run docker compose version", "not a docker command"}},
		{"not a version", "dev", nil, notVerified, []string{`"dev"`, "2.24.4"}},
		{"new enough", "2.24.4", nil, "", nil},
		{"current", "5.8.0", nil, "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			found := composeVersionFindings(c.version, c.err)
			if c.level == "" {
				if len(found) != 0 {
					t.Fatalf("findings = %+v, want none", found)
				}
				return
			}
			if len(found) != 1 {
				t.Fatalf("findings = %+v, want one", found)
			}
			f := found[0]
			if f.Check != "compose_version" || f.Level != c.level || f.Location == "" {
				t.Errorf("finding = %+v, want check compose_version at level %s with a location", f, c.level)
			}
			for _, want := range c.names {
				if !strings.Contains(f.Message, want) {
					t.Errorf("message %q doesn't name %q", f.Message, want)
				}
			}
		})
	}
}
