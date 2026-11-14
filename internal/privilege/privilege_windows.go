package privilege

// IsRoot is false on Windows: the proxy needs no administrator there, since Windows keeps
// no ports for administrators, and berth has no root to give up.
func IsRoot() bool { return false }

// Drop does nothing on Windows; see IsRoot.
func Drop() (bool, error) { return false, nil }

// StateOwned accepts any state directory on Windows, where berth never runs as root.
func StateOwned(string, int) error { return nil }
