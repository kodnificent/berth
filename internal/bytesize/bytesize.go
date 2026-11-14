// Package bytesize reads and writes sizes such as "10GB" and "512MiB".
package bytesize

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

var units = []struct {
	suffix string
	factor float64
}{
	{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40},
	{"kB", 1e3}, {"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9}, {"TB", 1e12},
	{"B", 1},
}

// Parse reads a non-negative size with a unit: B, kB or KB, MB, GB, TB, or KiB, MiB,
// GiB, TiB. A unit is required, so "10" never silently means ten bytes.
func Parse(s string) (int64, error) {
	t := strings.TrimSpace(s)
	for _, u := range units {
		num, ok := strings.CutSuffix(t, u.suffix)
		if !ok {
			continue
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
		if err != nil || f < 0 || math.IsInf(f, 0) || math.IsNaN(f) || f*u.factor > math.MaxInt64 {
			break
		}
		return int64(f * u.factor), nil
	}
	return 0, fmt.Errorf("%q is not a size; write a number and a unit, such as \"10GB\" or \"512MiB\"", s)
}

// Format writes n in decimal units with one decimal place, such as "1.2 GB".
func Format(n int64) string {
	f := float64(n)
	for _, u := range []struct {
		suffix string
		factor float64
	}{{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}} {
		if math.Abs(f) >= u.factor {
			return strconv.FormatFloat(f/u.factor, 'f', 1, 64) + " " + u.suffix
		}
	}
	return strconv.FormatInt(n, 10) + " B"
}
