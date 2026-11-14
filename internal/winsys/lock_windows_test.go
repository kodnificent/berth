package winsys

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestAPassableLockIsHeldAtEveryStepOfAHandOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	from, to, other := open(t, path), open(t, path), open(t, path)
	if ok, err := TryPassable(from); err != nil || !ok {
		t.Fatalf("TryPassable of a free lock = %v, %v", ok, err)
	}

	if err := HandOff(from); err != nil {
		t.Fatal(err)
	}
	if ok, _ := TryPassable(other); ok {
		t.Fatal("the lock was free to take while it was being handed off")
	}
	if err := TakeOver(to); err != nil {
		t.Fatal(err)
	}
	from.Close()
	if ok, _ := TryPassable(other); ok {
		t.Fatal("the lock was free to take once taken over")
	}

	to.Close()
	if ok, err := TryPassable(other); err != nil || !ok {
		t.Errorf("TryPassable once the holder closed it = %v, %v; want it taken", ok, err)
	}
}

func TestWaitPassableReturnsOnceTheLastHolderLetsGo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	held, waiter := open(t, path), open(t, path)
	if ok, err := TryPassable(held); err != nil || !ok {
		t.Fatalf("TryPassable of a free lock = %v, %v", ok, err)
	}
	done := make(chan error, 1)
	go func() { done <- WaitPassable(waiter) }()

	select {
	case <-done:
		t.Fatal("WaitPassable returned while the lock was held")
	case <-time.After(200 * time.Millisecond):
	}
	held.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitPassable still waiting after the lock was let go")
	}
}
