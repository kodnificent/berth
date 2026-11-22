package compose

import "testing"

func TestParseVersionReadsTheLeadingNumbers(t *testing.T) {
	cases := []struct {
		in   string
		want [3]int
		ok   bool
	}{
		{"2.24.4", [3]int{2, 24, 4}, true},
		{"v2.24.4", [3]int{2, 24, 4}, true},
		{"2.24.6-desktop.1", [3]int{2, 24, 6}, true},
		{"5.8.0", [3]int{5, 8, 0}, true},
		{"2.24", [3]int{2, 24, 0}, true},
		{"", [3]int{}, false},
		{"dev", [3]int{}, false},
		{"2", [3]int{}, false},
		{"x2.24.4", [3]int{}, false},
	}
	for _, c := range cases {
		got, ok := ParseVersion(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseVersion(%q) = %v, %v; want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// 2.24.4 is the first release whose ports: !override replaces main's list rather than
// adding to it: 2.18.1, 2.23.3, 2.24.0 and 2.24.3 all kept main's port beside the
// berth's in an override berth wrote, and 2.24.4 and 5.8.0 kept only the berth's (0058).
func TestReadsOverrideFrom2244(t *testing.T) {
	cases := []struct {
		version      string
		reads, known bool
	}{
		{"1.29.2", false, true},
		{"2.18.1", false, true},
		{"2.23.3", false, true},
		{"2.24.0", false, true},
		{"2.24.3", false, true},
		{"2.24.4", true, true},
		{"2.24.6-desktop.1", true, true},
		{"2.25.0", true, true},
		{"2.100.0", true, true},
		{"5.5.1", true, true},
		{"", false, false},
		{"dev", false, false},
	}
	for _, c := range cases {
		reads, known := ReadsOverride(c.version)
		if reads != c.reads || known != c.known {
			t.Errorf("ReadsOverride(%q) = %v, %v; want %v, %v", c.version, reads, known, c.reads, c.known)
		}
	}
	if reads, known := ReadsOverride(MinVersion); !reads || !known {
		t.Errorf("ReadsOverride(MinVersion %q) = %v, %v; want true, true", MinVersion, reads, known)
	}
}
