package postgres

import (
	"strings"
	"testing"
)

const (
	linuxBuild  = "PostgreSQL 18.1 (Debian 18.1-1.pgdg13+2) on aarch64-unknown-linux-gnu, compiled by gcc 14.2.0, 64-bit"
	darwinBuild = "PostgreSQL 18.1 (Homebrew) on aarch64-apple-darwin25.0.0, compiled by Apple clang 17.0.0, 64-bit"
)

func TestPostgres13And14CopyWithoutAStrategyClause(t *testing.T) {
	for _, v := range []int{130022, 140019} {
		p := planCopy(v, linuxBuild, false, "", "")
		if strings.Contains(p.statement("app_central", "app_x__central"), "STRATEGY") || p.report != FullCopy {
			t.Errorf("version %d: %q reported as %q, want no STRATEGY clause and a full copy", v, p.statement("app_central", "app_x__central"), p.report)
		}
	}
}

func TestPostgres15To17CopyFilesInFull(t *testing.T) {
	p := planCopy(170007, darwinBuild, false, "", btrfs)
	if !strings.HasSuffix(p.statement("app_central", "app_x__central"), "STRATEGY FILE_COPY") || p.method != "" || p.report != FullCopy {
		t.Errorf("Postgres 17: %+v, want FILE_COPY reported as a full copy, with file_copy_method left alone", p)
	}
}

func TestPostgres18OnMacOSAsksForAClone(t *testing.T) {
	p := planCopy(180001, darwinBuild, true, "copy", "")
	if !p.fileCopy || p.method != "clone" || p.report != CopyOnWrite {
		t.Errorf("Postgres 18 on macOS: %+v, want FILE_COPY with file_copy_method = clone, reported as copy-on-write", p)
	}
}

func TestPostgres18OnLinuxClonesOnlyWhereItSeesASharingFilesystem(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		local      string
		method     string
		report     string
	}{
		{"btrfs seen", "copy", btrfs, "clone", CopyOnWrite},
		{"XFS seen, reported from the disk it took", "copy", xfs, "clone", ""},
		{"ext4 seen", "copy", otherFS, "", FullCopy},
		{"ext4 seen, server set to clone", "clone", otherFS, "", FullCopy},
		{"not seen, as in a container", "copy", "", "", FullCopy},
		{"not seen, server set to clone", "clone", "", "", FileClone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := planCopy(180001, linuxBuild, true, c.configured, c.local)
			if p.method != c.method || p.report != c.report {
				t.Errorf("got file_copy_method %q reported as %q, want %q reported as %q", p.method, p.report, c.method, c.report)
			}
		})
	}
}

func TestPostgres18WithoutCloneSupportCopiesInFull(t *testing.T) {
	if p := planCopy(180001, linuxBuild, false, "copy", btrfs); p.method != "" || p.report != FullCopy {
		t.Errorf("a server whose file_copy_method has no clone: %+v, want a full copy", p)
	}
}

// XFS made without reflink copies every block of a clone asked for, so only the disk the
// copy took tells.
func TestCloneOnXFSIsReportedFromTheDiskItTook(t *testing.T) {
	const gb = 1 << 30
	cases := []struct {
		name     string
		size     int64
		used     int64
		measured bool
		want     string
	}{
		{"blocks shared", gb, 4 << 20, true, CopyOnWrite},
		{"every block copied", gb, gb, true, FullCopy},
		{"too small to tell", 8 << 20, 0, true, FileClone},
		{"disk not measured", gb, 0, false, FileClone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := measuredMethod(c.size, c.used, c.measured); got != c.want {
				t.Errorf("a %d-byte copy that took %d bytes of disk reads %q, want %q", c.size, c.used, got, c.want)
			}
		})
	}
}

func TestRefusalNamesEachSessionsApplicationAndAddress(t *testing.T) {
	msg := sessionsRefusal("app_central", []Session{
		{Application: "billing_worker", Address: "172.17.0.3", Kind: "client backend", Visible: true, Own: true},
		{Application: "", Address: "local", Kind: "client backend", Visible: true, Own: true},
		{Application: "psql", Role: "postgres", Kind: "client backend"},
	}, quiet, false, "berth add")
	for _, want := range []string{
		"app_central",
		"billing_worker from 172.17.0.3",
		"an unnamed application over a local socket",
		`psql under role "postgres", whose address the login can't see`,
		"disconnect = true",
		"berth add",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q does not contain %q", msg, want)
		}
	}
}

// The login's own role is the user berth connects as, which berth never prints.
func TestRefusalLeavesOutTheLoginsOwnRole(t *testing.T) {
	msg := sessionsRefusal("app_central", []Session{{Role: "app_user", Application: "web", Address: "10.0.0.2", Kind: "client backend", Visible: true, Own: true}}, quiet, false, "berth add")
	if strings.Contains(msg, "app_user") {
		t.Errorf("refusal %q names the login's role", msg)
	}
}

func TestDisconnectRefusalSaysNothingWasEnded(t *testing.T) {
	msg := sessionsRefusal("app_central", []Session{{Role: "postgres", Application: "admin", Kind: "client backend"}}, unendable, false, "berth reset")
	if !strings.Contains(msg, "no right to end") || !strings.Contains(msg, "ended none") || !strings.Contains(msg, "berth reset") {
		t.Errorf("refusal under disconnect %q does not say the login may not end the session, that none was ended, and what to run", msg)
	}
}

// A session the login may not end can turn up on a later try, after berth has ended others.
func TestDisconnectRefusalAfterEndingSessionsSaysSo(t *testing.T) {
	msg := sessionsRefusal("app_central", []Session{{Role: "postgres", Application: "admin", Kind: "client backend"}}, unendable, true, "berth add")
	if strings.Contains(msg, "ended none") || !strings.Contains(msg, "already ended") || !strings.Contains(msg, "stopped") {
		t.Errorf("refusal after ending sessions %q does not say berth ended some and then stopped", msg)
	}
}
