//go:build e2e && !darwin

package e2e

import "testing"

func requireAPFS(t *testing.T, dir string) {
	t.Skip("block sharing is checked on APFS, which only macOS has")
}

func blockOffsets(t *testing.T, path string) []int64 { return nil }

func physicalOffset(t *testing.T, path string, off int64) int64 { return 0 }
