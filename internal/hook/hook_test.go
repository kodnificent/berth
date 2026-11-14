package hook

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kodnificent/berth/internal/shell"
	"github.com/kodnificent/berth/internal/worktree"
)

func gitRepo(t *testing.T) (main, linked *worktree.Worktree) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "repo")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("BERTH_STATE_DIR", filepath.Join(root, "state"))
	os.WriteFile(filepath.Join(root, "gitconfig"), []byte("[user]\n\tname = t\n\temail = t@t.invalid\n[commit]\n\tgpgsign = false\n"), 0o644)
	run := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	os.MkdirAll(dir, 0o755)
	run(dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644)
	run(dir, "add", "-A")
	run(dir, "commit", "-q", "-m", "init")
	run(dir, "worktree", "add", "-q", "-b", "x", filepath.Join(root, "x"))
	m, err := worktree.Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	l, err := worktree.Resolve(filepath.Join(root, "x"))
	if err != nil {
		t.Fatal(err)
	}
	return m, l
}

func TestInstallWritesOwnHookAndRefusesOthers(t *testing.T) {
	m, l := gitRepo(t)
	path, changed, err := Install(l, "/opt/berth's bin/berth", false)
	if err != nil || !changed {
		t.Fatalf("Install: changed=%v err=%v", changed, err)
	}
	if path != filepath.Join(m.CommonDir, "hooks", "post-checkout") {
		t.Errorf("installed at %s, want the common git directory's hooks", path)
	}
	info, _ := os.Stat(path)
	if keepsExecBit(t) && info.Mode()&0o111 == 0 {
		t.Errorf("hook mode %v is not executable", info.Mode())
	}
	if _, changed, err := Install(m, "/opt/berth's bin/berth", false); err != nil || changed {
		t.Errorf("reinstalling the same hook: changed=%v err=%v, want unchanged", changed, err)
	}
	if _, changed, err := Install(m, "/usr/local/bin/berth", false); err != nil || !changed {
		t.Errorf("reinstalling for another binary: changed=%v err=%v, want rewritten", changed, err)
	}

	os.WriteFile(path, []byte("#!/bin/sh\necho mine\n"), 0o755)
	if _, _, err := Install(m, "/usr/local/bin/berth", false); err == nil || !strings.Contains(err.Error(), "didn't write") {
		t.Errorf("Install over someone else's hook: %v, want a refusal", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "#!/bin/sh\necho mine\n" {
		t.Errorf("someone else's hook was changed:\n%s", data)
	}
}

func TestInstallDryRunWritesNothing(t *testing.T) {
	m, _ := gitRepo(t)
	if _, changed, err := Install(m, "/bin/berth", true); err != nil || !changed {
		t.Fatalf("dry run: changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(filepath.Join(m.CommonDir, "hooks", "post-checkout")); err == nil {
		t.Errorf("a dry run wrote %s", filepath.Join(m.CommonDir, "hooks", "post-checkout"))
	}
}

// The script runs a stand-in berth that records its arguments and working directory.
func TestScriptRunsAddOnlyForNewLinkedWorktrees(t *testing.T) {
	m, l := gitRepo(t)
	dir := t.TempDir()
	record := filepath.Join(dir, "calls")
	fake := filepath.Join(dir, "berth")
	pwd := "$PWD"
	if runtime.GOOS == "windows" {
		// Git for Windows' sh spells the directory /c/...; cygpath gives Windows' spelling.
		pwd = `$(cygpath -w "$PWD")`
	}
	os.WriteFile(fake, []byte(fmt.Sprintf("#!/bin/sh\necho \"%s $*\" >> '%s'\nexit 7\n", pwd, record)), 0o755)
	script := filepath.Join(dir, "post-checkout")
	os.WriteFile(script, []byte(Script(fake)), 0o755)

	zero, sha := strings.Repeat("0", 40), strings.Repeat("1", 40)
	cases := []struct {
		dir, prev string
		calls     bool
	}{
		{l.Path, zero, true},
		{l.Path, sha, false},
		{m.Path, zero, false},
		{l.Path, strings.Repeat("0", 64), true},
	}
	for _, c := range cases {
		os.Remove(record)
		cmd := runHook(t, script, c.prev, sha, "1")
		cmd.Dir = c.dir
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Errorf("hook in %s with previous HEAD %s: %v, want exit 0 always", c.dir, c.prev[:4], err)
		}
		data, _ := os.ReadFile(record)
		if got := len(data) > 0; got != c.calls {
			t.Errorf("hook in %s with previous HEAD %s ran berth: %v, want %v", c.dir, c.prev[:4], got, c.calls)
		}
		if c.calls {
			if string(data) != l.Path+" add\n" {
				t.Errorf("hook ran %q, want berth add in the worktree", data)
			}
			if !strings.Contains(stderr.String(), "retry") {
				t.Errorf("hook didn't say berth add failed:\n%s", stderr.String())
			}
		}
	}
}

func TestInstallRefusesHooksPath(t *testing.T) {
	m, _ := gitRepo(t)
	exec.Command("git", "-C", m.Path, "config", "core.hooksPath", ".husky").Run()
	_, _, err := Install(m, "/bin/berth", false)
	if err == nil || !strings.Contains(err.Error(), "core.hooksPath") || !strings.Contains(err.Error(), ".husky") {
		t.Errorf("Install with core.hooksPath: %v", err)
	}
}

func TestHooksDirPlacesTheFolderGitRuns(t *testing.T) {
	m, l := gitRepo(t)
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	link := filepath.Join(outside, "link")
	os.Symlink(filepath.Join(m.Path, ".husky"), link)
	cases := []struct {
		setting    string
		dir        string
		inWorkTree bool
	}{
		{"", filepath.Join(m.CommonDir, "hooks"), false},
		{".git/hooks", filepath.Join(m.CommonDir, "hooks"), false},
		{filepath.Join(m.CommonDir, "hooks"), filepath.Join(m.CommonDir, "hooks"), false},
		{filepath.Join(m.CommonDir, "worktrees", "x", "hooks"), filepath.Join(m.CommonDir, "worktrees", "x", "hooks"), false},
		{outside, outside, false},
		{".husky", filepath.Join(m.Path, ".husky"), true},
		{"tools/hooks", filepath.Join(m.Path, "tools", "hooks"), true},
		{link, filepath.Join(m.Path, ".husky"), true},
		{filepath.Join(l.Path, ".githooks"), filepath.Join(l.Path, ".githooks"), true},
		{m.Path, m.Path, true},
	}
	for _, c := range cases {
		exec.Command("git", "-C", m.Path, "config", "--unset", "core.hooksPath").Run()
		if c.setting != "" {
			exec.Command("git", "-C", m.Path, "config", "core.hooksPath", c.setting).Run()
		}
		for _, w := range []*worktree.Worktree{m, l} {
			h, err := HooksDir(w)
			if err != nil || h.Dir != c.dir || h.InWorkTree != c.inWorkTree {
				t.Errorf("core.hooksPath %q from %s: %+v %v, want %s in work tree %v", c.setting, w.Path, h, err, c.dir, c.inWorkTree)
			}
		}
	}
}

func TestChangedFiles(t *testing.T) {
	m, l := gitRepo(t)
	if got := ChangedFiles(l); len(got) != 0 {
		t.Errorf("ChangedFiles on a fresh worktree = %v, want none", got)
	}
	os.WriteFile(filepath.Join(l.Path, "a.txt"), []byte("berth\n"), 0o644)
	os.WriteFile(filepath.Join(m.Path, "b.txt"), []byte("main\n"), 0o644)
	if got := strings.Join(ChangedFiles(l), ","); got != "a.txt,b.txt" {
		t.Errorf("ChangedFiles = %s, want a.txt,b.txt", got)
	}
}

func TestTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	os.WriteFile(path, []byte(b.String()), 0o644)
	got := strings.Split(Tail(path), "\n")
	if len(got) != tailLines || got[0] != "line 81" || got[len(got)-1] != "line 100" {
		t.Errorf("Tail = %q", got)
	}
	os.WriteFile(path, []byte(strings.Repeat("x", 10000)+"\nend\n"), 0o644)
	if got := Tail(path); got != "end" {
		t.Errorf("Tail of a long line = %q, want only whole lines inside the byte limit", got)
	}
}

func TestExecuteRecordsExitAndEnvironment(t *testing.T) {
	t.Setenv("BERTH_STATE_DIR", t.TempDir())
	t.Setenv("GIT_DIR", "/nowhere")
	dir := t.TempDir()
	pwd := "$(pwd -P)"
	if runtime.GOOS == "windows" {
		// Git for Windows' sh spells the directory /c/...; cygpath gives Windows' spelling.
		pwd = `$(cygpath -w "$(pwd -P)")`
	}
	p := PostAdd{Command: `echo "$BERTH_PATH|$BERTH_SOURCE|$BERTH_CHANGED_FILES|${GIT_DIR-unset}|` + pwd + `"; exit 4`, Dir: dir, Source: "/src", Changed: []string{"a", "b"}, Status: Pending}
	if err := Execute("id1", &p); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	want := dir + "|/src|a\nb|unset|" + real
	if p.Status != Failed || p.ExitCode == nil || *p.ExitCode != 4 || p.Output != want {
		t.Errorf("after Execute: status %s exit %v output %q, want failed, 4, %q", p.Status, p.ExitCode, p.Output, want)
	}
	saved, ok, err := Load("id1")
	if err != nil || !ok || saved.Status != Failed {
		t.Errorf("saved state %+v ok=%v err=%v", saved, ok, err)
	}
}

func TestLiveReportsDeadProcessAsFailed(t *testing.T) {
	t.Setenv("BERTH_STATE_DIR", t.TempDir())
	Save("id2", PostAdd{Command: "true", Status: Running})
	p, ok, err := Live("id2")
	if err != nil || !ok || p.Status != Failed || !strings.Contains(p.Error, "interrupted") {
		t.Errorf("Live with no process: %+v ok=%v err=%v, want failed as interrupted", p, ok, err)
	}

	Save("id3", PostAdd{Command: "true", Status: Running})
	lock, _, _ := TryLock("id3")
	defer lock.Close()
	if p, _, _ := Live("id3"); p.Status != Running {
		t.Errorf("Live while the lock is held: %s, want running", p.Status)
	}
}

func TestPreRemove(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := PreRemove(`echo "$BERTH_PATH"`, dir, "/src", nil, &out); err != nil || strings.TrimSpace(out.String()) != dir {
		t.Errorf("PreRemove: %v, output %q", err, out.String())
	}
	if err := PreRemove("exit 2", dir, "/src", nil, &out); err == nil {
		t.Errorf("a failing pre-remove returned no error")
	}
}

func TestUninstallRemovesOnlyBerthsOwnHook(t *testing.T) {
	m, l := gitRepo(t)
	path := filepath.Join(m.CommonDir, "hooks", "post-checkout")
	if _, removed, err := Uninstall(l, false); err != nil || removed {
		t.Fatalf("Uninstall with no hook: removed=%v err=%v, want nothing to do", removed, err)
	}
	if _, _, err := Install(m, "/opt/berth's bin/berth", false); err != nil {
		t.Fatal(err)
	}
	if _, removed, err := Uninstall(l, true); err != nil || !removed {
		t.Fatalf("Uninstall dry run: removed=%v err=%v", removed, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a dry run removed the hook: %v", err)
	}
	if _, removed, err := Uninstall(l, false); err != nil || !removed {
		t.Fatalf("Uninstall: removed=%v err=%v", removed, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the hook is still there: %v", err)
	}
	if _, removed, err := Uninstall(l, false); err != nil || removed {
		t.Errorf("Uninstall again: removed=%v err=%v, want nothing to do", removed, err)
	}

	for name, content := range map[string]string{
		"someone else's": "#!/bin/sh\necho mine\n",
		"edited":         Script("/bin/berth") + "echo mine\n",
	} {
		os.WriteFile(path, []byte(content), 0o755)
		if _, _, err := Uninstall(m, false); err == nil {
			t.Errorf("Uninstall of %s hook succeeded, want a refusal", name)
		}
		if data, _ := os.ReadFile(path); string(data) != content {
			t.Errorf("%s hook was changed:\n%s", name, data)
		}
	}
}

// keepsExecBit reports whether the filesystem of the test's temporary directory records
// that a file is executable, which Windows' doesn't.
func keepsExecBit(t *testing.T) bool {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(probe, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(probe)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode()&0o111 != 0
}

// runHook runs the hook script as git does: by itself, or on Windows with git's sh.
func runHook(t *testing.T, script string, args ...string) *exec.Cmd {
	t.Helper()
	if runtime.GOOS != "windows" {
		return exec.Command(script, args...)
	}
	sh, err := shell.Path()
	if err != nil {
		t.Fatal(err)
	}
	return exec.Command(sh, append([]string{script}, args...)...)
}
