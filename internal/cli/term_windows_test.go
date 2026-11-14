package cli

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

func TestConsoleTakesEscapes(t *testing.T) {
	const vt = windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING
	refused := errors.New("refused")
	for _, c := range []struct {
		name    string
		mode    uint32
		modeErr error
		setErr  error
		want    bool
		wantSet uint32
	}{
		{name: "not a console", modeErr: refused, want: false},
		{name: "escape codes already on", mode: 3 | vt, want: true},
		{name: "a console that takes them", mode: 3, want: true, wantSet: 3 | vt},
		{name: "a console that refuses them", mode: 3, setErr: refused, want: false, wantSet: 3 | vt},
	} {
		t.Run(c.name, func(t *testing.T) {
			var set uint32
			got := consoleTakesEscapes(
				func() (uint32, error) { return c.mode, c.modeErr },
				func(m uint32) error { set = m; return c.setErr },
			)
			if got != c.want || set != c.wantSet {
				t.Errorf("styling %v after setting mode %#x, want %v after %#x", got, set, c.want, c.wantSet)
			}
		})
	}
}
