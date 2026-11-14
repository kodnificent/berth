package cli

import (
	"testing"
	"time"
)

func TestAgoNamesTheLargestWholeUnit(t *testing.T) {
	for d, want := range map[time.Duration]string{
		59 * time.Second:              "just now",
		90 * time.Second:              "1m ago",
		59 * time.Minute:              "59m ago",
		23*time.Hour + 59*time.Minute: "23h ago",
		50 * time.Hour:                "2d ago",
	} {
		if got := ago(d); got != want {
			t.Errorf("ago(%s) = %q, want %q", d, got, want)
		}
	}
}
