//go:build e2e && !windows

package e2e

import "testing"

func volumeFilesystem(t *testing.T, path string) string { return "" }

func clusterNumbers(t *testing.T, path string) []string { return nil }

func berthWithoutSymlinkPrivilege(t *testing.T, repo *Repo, dir string, args ...string) Result {
	return Result{}
}
