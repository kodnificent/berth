package volume

import (
	"strings"
	"testing"
	"time"
)

func TestStateRoundTrip(t *testing.T) {
	t.Setenv("BERTH_STATE_DIR", t.TempDir())
	s, err := Load("b1")
	if err != nil || len(s.Volumes) != 0 {
		t.Fatalf("Load of a berth that never cloned = %+v, %v; want empty", s, err)
	}
	s.Put(Entry{Key: "db", Status: Cloning, StartedAt: time.Unix(1, 0).UTC()})
	s.Put(Entry{Key: "cache", Status: Ready})
	s.Put(Entry{Key: "db", Status: Failed, Error: "boom"})
	if err := Save("b1", s); err != nil {
		t.Fatal(err)
	}
	got, err := Load("b1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Volumes) != 2 {
		t.Fatalf("Put of an existing key added an entry: %+v", got.Volumes)
	}
	if e, ok := got.Entry("db"); !ok || e.Status != Failed || e.Error != "boom" {
		t.Errorf("db = %+v, want the replaced failed entry", e)
	}
	if got.Pending() {
		t.Errorf("Pending with nothing cloning")
	}
}

func TestLockShowsWhetherACloneRuns(t *testing.T) {
	t.Setenv("BERTH_STATE_DIR", t.TempDir())
	held, ok, err := TryLock("b1")
	if err != nil || !ok {
		t.Fatalf("TryLock with no clone running = %v, %v; want it taken", ok, err)
	}
	if _, ok, err := TryLock("b1"); err != nil || ok {
		t.Fatalf("TryLock while held = %v, %v; want it refused", ok, err)
	}
	if other, ok, _ := TryLock("b2"); !ok {
		t.Errorf("another berth's lock is refused while b1's is held")
	} else {
		other.Close()
	}
	done := make(chan error, 1)
	go func() { done <- WaitUnlocked("b1") }()
	select {
	case <-done:
		t.Fatal("WaitUnlocked returned while the lock was held")
	case <-time.After(200 * time.Millisecond):
	}
	held.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitUnlocked still waiting after the lock was released")
	}
}

func TestPID(t *testing.T) {
	t.Setenv("BERTH_STATE_DIR", t.TempDir())
	if got := ReadPID("b1"); got != 0 {
		t.Errorf("ReadPID with none written = %d", got)
	}
	if err := WritePID("b1", 4242); err != nil {
		t.Fatal(err)
	}
	if got := ReadPID("b1"); got != 4242 {
		t.Errorf("ReadPID = %d, want 4242", got)
	}
}

func TestProgress(t *testing.T) {
	for _, c := range []struct {
		e    Entry
		want float64
		ok   bool
	}{
		{Entry{Status: Cloning, Total: 400, Copied: 100}, 0.25, true},
		{Entry{Status: Cloning, Total: 400}, 0, true},
		{Entry{Status: Cloning, Total: 400, Copied: 500}, 0.99, true},
		{Entry{Status: Cloning}, 0, false},
		{Entry{Status: Ready, Total: 400, Copied: 400}, 0, false},
	} {
		if got, ok := c.e.Progress(); got != c.want || ok != c.ok {
			t.Errorf("Progress of %+v = %v, %v; want %v, %v", c.e, got, ok, c.want, c.ok)
		}
	}
}

func TestRecoverFailsAStoppedDatabaseCopyWithoutDocker(t *testing.T) {
	t.Setenv("BERTH_STATE_DIR", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	s := State{Databases: []Entry{
		{Key: "central", Strategy: "dump", Status: Cloning, Reset: true},
		{Key: "reports", Strategy: "dump", Status: Ready},
	}}
	changed, err := Recover("b1", &s)
	if err != nil || !changed {
		t.Fatalf("Recover with no docker on PATH = %v, %v; want the copy recovered without Docker", changed, err)
	}
	central, _ := s.Database("central")
	if central.Status != Failed || !strings.Contains(central.Error, "berth reset") {
		t.Errorf("central after Recover = %q %q, want failed naming berth reset", central.Status, central.Error)
	}
	if reports, _ := s.Database("reports"); reports.Status != Ready {
		t.Errorf("reports after Recover = %q, want the finished copy left ready", reports.Status)
	}
	saved, err := Load("b1")
	if e, ok := saved.Database("central"); err != nil || !ok || e.Status != Failed {
		t.Errorf("saved state after Recover = %+v, %v; want central failed", saved.Databases, err)
	}
}
