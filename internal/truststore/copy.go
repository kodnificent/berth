//go:build darwin || windows

package truststore

import "os"

// withPrivateCopy writes pemData to a file only this user can read, made for this call,
// and hands its path to fn, for a tool that reads only a file. The tool runs as this user
// and reads the file at once; a process of the same user that rewrote it in between could
// as well run the tool itself, and raises the same system prompt either way.
func withPrivateCopy(pemData []byte, fn func(path string) error) error {
	f, err := os.CreateTemp("", "berth-proxy-ca-*.pem")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(pemData); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return fn(f.Name())
}
