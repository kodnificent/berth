//go:build !linux

package postgres

// localDataDir is unknown off Linux: on macOS a clone either shares blocks or fails, so
// berth needn't look.
func localDataDir(pid, port int) (dir, fs string, ok bool) { return "", "", false }

func freeBytes(dir string) (int64, bool) { return 0, false }
