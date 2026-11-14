package cli

import (
	"os"

	"golang.org/x/sys/windows"
)

func takesEscapes(f *os.File) bool {
	h := windows.Handle(f.Fd())
	return consoleTakesEscapes(
		func() (uint32, error) {
			var mode uint32
			err := windows.GetConsoleMode(h, &mode)
			return mode, err
		},
		func(mode uint32) error { return windows.SetConsoleMode(h, mode) },
	)
}

// consoleTakesEscapes turns escape codes on for a console and reports whether it took
// them: consoles before Windows 10 1511, and some hosts since, print them as text.
func consoleTakesEscapes(mode func() (uint32, error), setMode func(uint32) error) bool {
	m, err := mode()
	if err != nil {
		return false
	}
	if m&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return setMode(m|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
