//go:build e2e

package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/joho/godotenv"
)

const (
	blockStart = "# >>> berth (managed, do not edit)"
	blockEnd   = "# <<< berth"
)

// splitBlock returns what comes before the managed block, the block's own lines between
// the markers, and what comes after the end marker.
func splitBlock(t *testing.T, content string) (before, block, after string) {
	t.Helper()
	start := strings.Index(content, blockStart+"\n")
	if start < 0 || (start > 0 && content[start-1] != '\n') {
		t.Fatalf("no managed block start line %q in:\n%s", blockStart, content)
	}
	rest := content[start+len(blockStart)+1:]
	end := strings.Index(rest, blockEnd)
	if end < 0 || (end > 0 && rest[end-1] != '\n') {
		t.Fatalf("no managed block end line %q in:\n%s", blockEnd, content)
	}
	return content[:start], rest[:end], rest[end+len(blockEnd):]
}

func readString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func readlink(t *testing.T, path string) string {
	t.Helper()
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

// In a new worktree, the source's gitignored .env is present after add
// with the managed block appended as its last lines, and everything outside the block is
// byte for byte what the source's .env holds.
func TestEnvIsClonedWithBlockLast(t *testing.T) {
	repo := NewRepo(t)
	appendFile(t, filepath.Join(repo.Dir, ".env"), "# main's own settings\nAPP_NAME=\"Fixture app\"\n\nEMPTY=\n")
	writeFile(t, repo.privateConfig(), "[env.set]\nGREETING = \"hello {name}\"\n")
	source := readString(t, filepath.Join(repo.Dir, ".env"))

	wt := repo.AddWorktree(t, "feat-env")
	if _, err := os.Stat(filepath.Join(wt, ".env")); !os.IsNotExist(err) {
		t.Fatalf("the new worktree already has a .env before add (err %v); the test needs one without", err)
	}
	mustBerth(t, repo, wt, "add")

	got := readString(t, filepath.Join(wt, ".env"))
	before, block, after := splitBlock(t, got)
	if after != "\n" {
		t.Errorf("the managed block is not the last lines of .env; after its end marker comes %q", after)
	}
	if before != source {
		t.Errorf(".env outside the managed block differs from the source's .env\nsource:\n%q\noutside the block:\n%q", source, before)
	}
	if !strings.Contains(block, "GREETING=") {
		t.Errorf("the managed block does not set GREETING:\n%s", block)
	}
}

// The gitignored part: every gitignored .env* file of the source is cloned,
// and a .env* file git does not ignore is not.
func TestOnlyGitignoredEnvFilesAreCloned(t *testing.T) {
	repo := NewRepo(t)
	appendFile(t, filepath.Join(repo.Dir, ".gitignore"), ".env.*\n!.env.example\n")
	writeFile(t, filepath.Join(repo.Dir, ".env.example"), "TRACKED=1\n")
	repo.Git(t, "add", ".gitignore", ".env.example")
	repo.Git(t, "commit", "-q", "-m", "ignore env files")
	writeFile(t, filepath.Join(repo.Dir, ".env.testing"), "DB_DATABASE=testing\n")
	writeFile(t, filepath.Join(repo.Dir, "web", ".env.local"), "NESTED=1\n")

	wt := repo.AddWorktree(t, "feat-envs")
	mustBerth(t, repo, wt, "add")

	for _, name := range []string{".env.testing", filepath.Join("web", ".env.local")} {
		want := readString(t, filepath.Join(repo.Dir, name))
		data, err := os.ReadFile(filepath.Join(wt, name))
		if err != nil {
			t.Errorf("gitignored %s was not cloned: %v", name, err)
			continue
		}
		if string(data) != want {
			t.Errorf("%s = %q, want the source's %q", name, data, want)
		}
	}

	notIgnored := filepath.Join(repo.Dir, ".envnotignored")
	writeFile(t, notIgnored, "X=1\n")
	wt2 := repo.AddWorktree(t, "feat-envs-2")
	mustBerth(t, repo, wt2, "add")
	if _, err := os.Stat(filepath.Join(wt2, ".envnotignored")); !os.IsNotExist(err) {
		t.Errorf(".envnotignored is not gitignored in the source but was cloned (err %v)", err)
	}
}

// Each [env.set] key appears in the block with its template expanded, and a
// key set in the block wins when the file is read by Docker Compose and by a dotenv
// parser.
func TestEnvSetKeysWinForComposeAndDotenv(t *testing.T) {
	repo := NewRepo(t)
	appendFile(t, filepath.Join(repo.Dir, ".env"), "APP_URL=http://localhost:8000\nSPACED=main\nGREETING=main\n")
	writeFile(t, repo.privateConfig(), `[berth]
url = "http://{host}:{port.web}"

[compose.ports]
"web:80" = "assign"

[env]
host = ["SESSION_HOST"]

[env.set]
APP_URL = "{url}"
GREETING = "hello {name}"
SPACED = "two words # not a comment $NOT_EXPANDED \"quoted\""
WEB_ONLY = "{port.web}"
ORIGINS = "{host},app.{host}"
`)

	wt := repo.AddWorktree(t, "feat-set")
	mustBerth(t, repo, wt, "add")
	s := configStatusOf(t, repo, wt)

	want := map[string]string{
		"APP_URL":      s.URL,
		"GREETING":     "hello " + s.Name,
		"SPACED":       `two words # not a comment $NOT_EXPANDED "quoted"`,
		"ORIGINS":      s.Host + ",app." + s.Host,
		"SESSION_HOST": s.Host,
	}
	if !strings.HasPrefix(s.URL, "http://"+s.Host+":") {
		t.Fatalf("status url %q is not built from host %q and the web port", s.URL, s.Host)
	}
	for k, v := range want {
		if s.Env[k] != v {
			t.Errorf("status env %s = %q, want %q", k, s.Env[k], v)
		}
	}
	want["WEB_ONLY"] = s.Env["WEB_ONLY"]
	if want["WEB_ONLY"] == "" || strings.Contains(want["WEB_ONLY"], "{") {
		t.Fatalf("status env WEB_ONLY = %q, want an assigned port", want["WEB_ONLY"])
	}

	content := readString(t, filepath.Join(wt, ".env"))
	_, block, _ := splitBlock(t, content)
	inBlock, err := godotenv.Unmarshal(block)
	if err != nil {
		t.Fatalf("parsing the managed block: %v\n%s", err, block)
	}
	for k, v := range want {
		if inBlock[k] != v {
			t.Errorf("managed block %s = %q, want %q", k, inBlock[k], v)
		}
	}
	if strings.Contains(block, "{") {
		t.Errorf("the managed block still holds a template:\n%s", block)
	}

	dotenv, err := godotenv.Read(filepath.Join(wt, ".env"))
	if err != nil {
		t.Fatalf("dotenv parsing .env: %v", err)
	}
	composeEnv := map[string]string{}
	out := repo.Compose(t, wt, "config", "--environment")
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			composeEnv[k] = v
		}
	}
	for k, v := range want {
		if dotenv[k] != v {
			t.Errorf("a dotenv parser reads %s = %q, want the block's %q", k, dotenv[k], v)
		}
		if composeEnv[k] != v {
			t.Errorf("docker compose reads %s = %q, want the block's %q", k, composeEnv[k], v)
		}
	}
}

type fileState struct {
	mode    fs.FileMode
	modTime int64
	size    int64
	sum     string
	link    string
}

// snapshot records every file under root, so any write, even one that rewrites the same
// bytes, shows up as a changed modification time.
func snapshot(t *testing.T, root string, skip func(rel string) bool) map[string]fileState {
	t.Helper()
	out := map[string]fileState{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if skip != nil && skip(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		st := fileState{mode: info.Mode(), modTime: info.ModTime().UnixNano(), size: info.Size()}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			st.link, _ = os.Readlink(path)
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			st.sum = hex.EncodeToString(sum[:])
		}
		out[rel] = st
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func diffSnapshots(a, b map[string]fileState) []string {
	var diffs []string
	for k, v := range a {
		w, ok := b[k]
		switch {
		case !ok:
			diffs = append(diffs, "removed "+k)
		case v != w:
			diffs = append(diffs, fmt.Sprintf("changed %s: %+v -> %+v", k, v, w))
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			diffs = append(diffs, "added "+k)
		}
	}
	sort.Strings(diffs)
	return diffs
}

// setupFiles gives main untracked content to clone, a listed path main doesn't have, and
// a config that clones them and sets a key in the block.
func setupFiles(t *testing.T, repo *Repo) {
	t.Helper()
	writeFile(t, filepath.Join(repo.Dir, "shared", "file.txt"), "shared file\n")
	writeFile(t, filepath.Join(repo.Dir, "shared", "deep", "nested.bin"), "\x00\x01binary\xff")
	writeFile(t, filepath.Join(repo.Dir, "notes.txt"), "notes\n")
	writeFile(t, filepath.Join(repo.Dir, "web", "build", "out.txt"), "built\n")
	if err := os.Chmod(filepath.Join(repo.Dir, "notes.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo.privateConfig(), `[files]
clone = ["shared", "notes.txt", "web", "missing-in-main"]

[env.set]
GREETING = "hello {name}"
`)
}

// Running add again produces no change to any file.
func TestAddAgainChangesNoFile(t *testing.T) {
	repo := NewRepo(t)
	setupFiles(t, repo)
	wt := repo.AddWorktree(t, "feat-again")
	mustBerth(t, repo, wt, "add")

	skipGit := func(rel string) bool { return rel == ".git" }
	exclude := filepath.Join(repo.Dir, ".git", "info", "exclude")
	beforeWT := snapshot(t, wt, nil)
	beforeMain := snapshot(t, repo.Dir, skipGit)
	// Recording last use is the one write a second add makes, by design (Idle berths).
	skipLastUse := func(rel string) bool { return rel == "lastuse" }
	beforeState := snapshot(t, repo.StateDir, skipLastUse)
	beforeExclude := snapshot(t, filepath.Dir(exclude), nil)

	mustBerth(t, repo, wt, "add")

	for _, c := range []struct {
		what   string
		before map[string]fileState
		after  map[string]fileState
	}{
		{"the worktree", beforeWT, snapshot(t, wt, nil)},
		{"the main worktree", beforeMain, snapshot(t, repo.Dir, skipGit)},
		{"the berth state directory", beforeState, snapshot(t, repo.StateDir, skipLastUse)},
		{".git/info", beforeExclude, snapshot(t, filepath.Dir(exclude), nil)},
	} {
		if d := diffSnapshots(c.before, c.after); len(d) > 0 {
			t.Errorf("a second add changed %s:\n%s", c.what, strings.Join(d, "\n"))
		}
	}
}

// Paths listed under [files] clone exist in the berth with the same
// contents as in the source, including inside a directory git already checked out.
func TestFilesCloneCopiesPaths(t *testing.T) {
	repo := NewRepo(t)
	setupFiles(t, repo)
	wt := repo.AddWorktree(t, "feat-files")
	mustBerth(t, repo, wt, "add")

	for _, rel := range []string{"shared/file.txt", "shared/deep/nested.bin", "notes.txt", "web/build/out.txt", "web/index.html"} {
		want := readString(t, filepath.Join(repo.Dir, rel))
		got, err := os.ReadFile(filepath.Join(wt, rel))
		if err != nil {
			t.Errorf("%s is missing from the berth: %v", rel, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q in the berth, want the source's %q", rel, got, want)
		}
	}
	// The source's mode is 0600, or on Windows, which keeps only a read-only flag, 0666.
	source, err := os.Stat(filepath.Join(repo.Dir, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(wt, "notes.txt"))
	if err == nil && info.Mode().Perm() != source.Mode().Perm() {
		t.Errorf("notes.txt has mode %v in the berth, want the source's %v", info.Mode().Perm(), source.Mode().Perm())
	}
	if _, err := os.Lstat(filepath.Join(wt, "missing-in-main")); !os.IsNotExist(err) {
		t.Errorf("a listed path main doesn't have exists in the berth (err %v)", err)
	}
}

// A symlink inside a cloned path that points into the source worktree points
// to the same relative place inside the berth after cloning; a symlink pointing outside
// the source is unchanged.
func TestSymlinksIntoSourceFollowTheBerth(t *testing.T) {
	repo := NewRepo(t)
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "elsewhere.txt"), "outside\n")
	writeFile(t, filepath.Join(repo.Dir, "shared", "file.txt"), "shared file\n")

	links := map[string]string{
		"shared/abs-file":    filepath.Join(repo.Dir, "shared", "file.txt"),
		"shared/abs-dir":     filepath.Join(repo.Dir, "web"),
		"shared/abs-root":    repo.Dir,
		"shared/rel-inside":  "file.txt",
		"shared/rel-sibling": "../web/index.html",
		"shared/abs-outside": filepath.Join(outside, "elsewhere.txt"),
		"shared/dangling":    filepath.Join(repo.Dir, "shared", "not-there-yet"),
		"storage":            filepath.Join(repo.Dir, "shared"),
	}
	// On macOS the temp dir lives under /var, a symlink to /private/var. A link written
	// with the unresolved path still points into the source.
	unresolved := ""
	if runtime.GOOS == "darwin" && strings.HasPrefix(repo.Dir, "/private/var/") {
		unresolved = strings.TrimPrefix(repo.Dir, "/private")
		links["shared/abs-unresolved"] = filepath.Join(unresolved, "shared", "file.txt")
	}
	for link, target := range links {
		symlink(t, target, filepath.Join(repo.Dir, link))
	}
	writeFile(t, repo.privateConfig(), "[files]\nclone = [\"shared\", \"storage\"]\n")

	wt := repo.AddWorktree(t, "feat-links")
	mustBerth(t, repo, wt, "add")

	want := map[string]string{
		"shared/abs-file":    filepath.Join(wt, "shared", "file.txt"),
		"shared/abs-dir":     filepath.Join(wt, "web"),
		"shared/abs-root":    wt,
		"shared/rel-inside":  "file.txt",
		"shared/rel-sibling": "../web/index.html",
		"shared/abs-outside": filepath.Join(outside, "elsewhere.txt"),
		"shared/dangling":    filepath.Join(wt, "shared", "not-there-yet"),
		"storage":            filepath.Join(wt, "shared"),
	}
	if unresolved != "" {
		want["shared/abs-unresolved"] = filepath.Join(wt, "shared", "file.txt")
	}
	for link, target := range want {
		// Windows keeps a link's target with backslashes.
		target = filepath.FromSlash(target)
		got := readlink(t, filepath.Join(wt, link))
		if got != target && !(link == "shared/abs-unresolved" && got == filepath.Join(strings.TrimPrefix(wt, "/private"), "shared", "file.txt")) {
			t.Errorf("%s -> %q in the berth, want %q", link, got, target)
		}
	}
	for link, target := range links {
		if got := readlink(t, filepath.Join(repo.Dir, link)); got != filepath.FromSlash(target) {
			t.Errorf("cloning changed main's %s -> %q, want it left at %q", link, got, target)
		}
	}
	if got := readString(t, filepath.Join(wt, "shared", "abs-file")); got != "shared file\n" {
		t.Errorf("reading through the berth's shared/abs-file gave %q", got)
	}
}

// Files berth generates never show in git status of the worktree, and the
// repository's .gitignore is unchanged.
func TestGeneratedFilesStayOutOfGitStatus(t *testing.T) {
	repo := NewRepo(t)
	setupFiles(t, repo)
	gitignore := readString(t, filepath.Join(repo.Dir, ".gitignore"))

	wt := repo.AddWorktree(t, "feat-status")
	mustBerth(t, repo, wt, "add")
	if out := repo.mustRun(t, wt, "git", "status", "--porcelain", "--untracked-files=all"); out != "" {
		t.Errorf("git status in the berth shows berth's files:\n%s", out)
	}

	// A worktree whose source has no .env still gets one for the managed block, and it
	// must stay out of git status too, even where .gitignore doesn't cover it.
	other := NewRepo(t)
	if err := os.Remove(filepath.Join(other.Dir, ".env")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(other.Dir, ".gitignore"), "# nothing ignored\n")
	other.Git(t, "commit", "-q", "-am", "ignore nothing")
	writeFile(t, other.privateConfig(), "[env.set]\nGREETING = \"hello {name}\"\n")
	otherIgnore := readString(t, filepath.Join(other.Dir, ".gitignore"))
	wt2 := other.AddWorktree(t, "feat-noenv")
	mustBerth(t, other, wt2, "add")
	if _, err := os.Stat(filepath.Join(wt2, ".env")); err != nil {
		t.Fatalf("berth wrote no .env in a worktree whose source has none: %v", err)
	}
	if out := other.mustRun(t, wt2, "git", "status", "--porcelain", "--untracked-files=all"); out != "" {
		t.Errorf("git status in the berth shows berth's files:\n%s", out)
	}

	for _, c := range []struct{ path, want string }{
		{filepath.Join(repo.Dir, ".gitignore"), gitignore},
		{filepath.Join(wt, ".gitignore"), gitignore},
		{filepath.Join(other.Dir, ".gitignore"), otherIgnore},
		{filepath.Join(wt2, ".gitignore"), otherIgnore},
	} {
		if got := readString(t, c.path); got != c.want {
			t.Errorf("%s changed:\n%q\nwant\n%q", c.path, got, c.want)
		}
	}
}

// berth env prints every managed key, and eval "$(berth env)" sets them in
// a POSIX shell.
func TestEnvCommandPrintsManagedKeys(t *testing.T) {
	repo := NewRepo(t)
	writeFile(t, repo.privateConfig(), `[berth]
url = "http://{host}:{port.web}"

[compose.ports]
"web:80" = "assign"

[ports]
vite = { env = "VITE_PORT" }

[env]
url = ["APP_URL"]

[env.set]
GREETING = "hello {name}"
TRICKY = 'say "hi" $HOME `+"`whoami`"+` end'
`)
	wt := repo.AddWorktree(t, "feat-envcmd")
	mustBerth(t, repo, wt, "add")

	_, block, _ := splitBlock(t, readString(t, filepath.Join(wt, ".env")))
	managed, err := godotenv.Unmarshal(block)
	if err != nil {
		t.Fatalf("parsing the managed block: %v\n%s", err, block)
	}
	for _, k := range []string{"APP_URL", "GREETING", "TRICKY", "VITE_PORT"} {
		if _, ok := managed[k]; !ok {
			t.Errorf("the managed block has no %s:\n%s", k, block)
		}
	}

	printed := mustBerth(t, repo, wt, "env").Stdout
	for k := range managed {
		if !strings.Contains(printed, k) {
			t.Errorf("berth env does not print managed key %s:\n%s", k, printed)
		}
	}

	script := fmt.Sprintf(`eval "$(%q env)" || exit 1; for k in %s; do printf '%%s=%%s\0' "$k" "$(printenv "$k")"; done`, berthBin, strings.Join(sortedKeys(managed), " "))
	cmd := exec.Command(binSh, "-c", script)
	cmd.Dir = wt
	cmd.Env = repo.Env()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("eval \"$(berth env)\" in sh: %v\n%s", err, stderr.String())
	}
	got := map[string]string{}
	for _, kv := range strings.Split(strings.TrimSuffix(stdout.String(), "\x00"), "\x00") {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	for k, v := range managed {
		if got[k] != v {
			t.Errorf("after eval, %s = %q, want the managed %q", k, got[k], v)
		}
	}
	if managed["TRICKY"] != `say "hi" $HOME `+"`whoami`"+` end` {
		t.Errorf("TRICKY in the block = %q, want the configured value verbatim", managed["TRICKY"])
	}

	jsonOut := mustBerth(t, repo, wt, "env", "--json").Stdout
	for k := range managed {
		if !strings.Contains(jsonOut, `"`+k+`"`) {
			t.Errorf("berth env --json does not list %s:\n%s", k, jsonOut)
		}
	}
}

// A path under [files] clone is cloned once: a file the user deletes in the berth stays
// deleted on the next add, while a path added to the config later is cloned then.
func TestClonedPathsAreClonedOnce(t *testing.T) {
	repo := NewRepo(t)
	setupFiles(t, repo)
	wt := repo.AddWorktree(t, "feat-once")
	mustBerth(t, repo, wt, "add")

	if err := os.Remove(filepath.Join(wt, "shared", "file.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo.Dir, "later", "x.txt"), "later\n")
	cfg := strings.Replace(readString(t, repo.privateConfig()), `"missing-in-main"]`, `"missing-in-main", "later"]`, 1)
	writeFile(t, repo.privateConfig(), cfg)
	mustBerth(t, repo, wt, "add")

	if _, err := os.Stat(filepath.Join(wt, "shared", "file.txt")); !os.IsNotExist(err) {
		t.Errorf("a second add brought back a file deleted in the berth (err %v)", err)
	}
	if got := readString(t, filepath.Join(wt, "later", "x.txt")); got != "later\n" {
		t.Errorf("a path added to [files] clone later was not cloned: %q", got)
	}
}
