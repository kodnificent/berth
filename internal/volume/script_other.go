//go:build !(darwin || linux || freebsd || netbsd || openbsd || dragonfly || windows)

package volume

func stopScript(int, string) error { return nil }
