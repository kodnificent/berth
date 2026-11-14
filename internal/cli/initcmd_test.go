package cli

import (
	"strings"
	"testing"

	"github.com/kodnificent/berth/internal/apperr"
)

func TestInitRefusesContradictoryPresetFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--preset", "vite", "--no-presets"},
		{"--preset", "vite", "--preset", "laravel"},
		{"--preset"},
	} {
		if err := runInit(&Context{}, args); apperr.CodeOf(err) != apperr.Usage {
			t.Errorf("berth init %v = %v, want a usage error", args, err)
		}
	}
}

func TestInitRefusesAnEmptyPresetName(t *testing.T) {
	for _, args := range [][]string{{"--preset="}, {"--preset", ""}} {
		err := runInit(&Context{}, args)
		if apperr.CodeOf(err) != apperr.Usage || !strings.Contains(err.Error(), "--preset needs") {
			t.Errorf("berth init %q = %v, want the usage error for a missing name", args, err)
		}
	}
}
