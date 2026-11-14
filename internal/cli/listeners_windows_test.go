package cli

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListenersNameTheProcessAndWhereItRuns(t *testing.T) {
	l, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	got, err := listeners(l.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range got {
		if p.pid != os.Getpid() {
			continue
		}
		if want := strings.TrimSuffix(filepath.Base(self), ".exe"); !strings.EqualFold(p.command, want) {
			t.Errorf("listener command %q, want %q", p.command, want)
		}
		if !strings.EqualFold(p.cwd, wd) {
			t.Errorf("listener runs in %q, want %q", p.cwd, wd)
		}
		return
	}
	t.Errorf("listeners of this process's port = %+v, want this process, %d", got, os.Getpid())
}
