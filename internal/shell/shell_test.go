package shell

import (
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestGitShFindsGitForWindowsShAboveTheExecPath(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "bin", "sh.exe"))
	touch(t, filepath.Join(root, "usr", "bin", "sh.exe"))

	got, ok := gitSh(filepath.Join(root, "mingw64", "libexec", "git-core"), 4)

	if want := filepath.Join(root, "bin", "sh.exe"); !ok || got != want {
		t.Errorf("gitSh = %q, %v; want %s, the sh that sets up git's PATH", got, ok, want)
	}
}

func TestGitShFindsMSYS2ShInUsrBin(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "usr", "bin", "sh.exe"))

	got, ok := gitSh(filepath.Join(root, "usr", "lib", "git-core"), 4)

	if want := filepath.Join(root, "usr", "bin", "sh.exe"); !ok || got != want {
		t.Errorf("gitSh = %q, %v; want %s", got, ok, want)
	}
}

func TestGitShLooksNoHigherThanTheInstall(t *testing.T) {
	above := t.TempDir()
	touch(t, filepath.Join(above, "bin", "sh.exe"))
	root := filepath.Join(above, "mingit")
	if err := os.MkdirAll(filepath.Join(root, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got, ok := gitSh(filepath.Join(root, "cmd"), 2); ok {
		t.Errorf("gitSh found %s, outside the git install with no sh", got)
	}
}

func TestArgumentsReachTheScriptExactlyAsGiven(t *testing.T) {
	args := []string{`C:\Users\O'Brien\code\app\.berth.toml`, `{a,b} *`, `a\\b`, ""}
	cmd, err := Command(nil, `printf '<%s>' "$#" "$@"`, "editor", args[0], args[1], args[2], args[3])
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out), `<4><C:\Users\O'Brien\code\app\.berth.toml><{a,b} *><a\\b><>`; got != want {
		t.Errorf("the script printed %q, want %q", got, want)
	}
}
