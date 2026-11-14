package clone

import (
	"os"
	"path/filepath"
)

// Support says whether clones from one directory into another share their data by
// copy-on-write, and names the filesystem each sits on, empty when unknown.
type Support struct {
	Cow          bool
	Source, Dest string
}

// Probe clones a scratch file from srcDir into dstDir the way Tree clones any file, and
// removes both again.
func Probe(srcDir, dstDir string) (Support, error) {
	s := Support{Source: filesystem(srcDir), Dest: filesystem(dstDir)}
	f, err := os.CreateTemp(srcDir, ".berth-cow-probe-")
	if err != nil {
		return s, err
	}
	defer os.Remove(f.Name())
	// Larger than any filesystem keeps inline in its metadata, where nothing is shared.
	_, err = f.Write(make([]byte, 64<<10))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return s, err
	}
	dst := filepath.Join(dstDir, filepath.Base(f.Name())+"-clone")
	defer os.Remove(dst)
	shared, _, err := Tree(f.Name(), dst)
	s.Cow = shared > 0
	return s, err
}
