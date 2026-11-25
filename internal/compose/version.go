package compose

import (
	"regexp"
	"strconv"
)

// Before 2.24.4, compose adds an !override list to main's instead of replacing it, so a
// berth would publish main's ports (0058).
const MinVersion = "2.24.4"

// Docker Desktop's builds carry a suffix, such as 2.24.6-desktop.1.
var leadingVersion = regexp.MustCompile(`^v?(\d+)\.(\d+)(?:\.(\d+))?`)

func ParseVersion(version string) (v [3]int, ok bool) {
	m := leadingVersion.FindStringSubmatch(version)
	if m == nil {
		return v, false
	}
	for i, part := range m[1:] {
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}

// known is false when version isn't one berth can read, so nobody can tell.
func ReadsOverride(version string) (reads, known bool) {
	v, ok := ParseVersion(version)
	if !ok {
		return false, false
	}
	min, _ := ParseVersion(MinVersion)
	for i := range v {
		if v[i] != min[i] {
			return v[i] > min[i], true
		}
	}
	return true, true
}
