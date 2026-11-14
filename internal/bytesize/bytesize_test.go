package bytesize

import "testing"

func TestParse(t *testing.T) {
	for in, want := range map[string]int64{
		"10GB":     10e9,
		"1.5 MB":   1_500_000,
		"512MiB":   512 << 20,
		"0B":       0,
		"3kB":      3000,
		"3KB":      3000,
		"2TiB":     2 << 40,
		" 7 B ":    7,
		"100000TB": 100000e12,
	} {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "10", "GB", "-1GB", "ten GB", "1PB", "1e30TB"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) succeeded, want an error", in)
		}
	}
}

func TestFormat(t *testing.T) {
	for n, want := range map[int64]string{
		0:             "0 B",
		999:           "999 B",
		1_048_576:     "1.0 MB",
		1_200_000_000: "1.2 GB",
		10e12:         "10.0 TB",
	} {
		if got := Format(n); got != want {
			t.Errorf("Format(%d) = %q, want %q", n, got, want)
		}
	}
}
