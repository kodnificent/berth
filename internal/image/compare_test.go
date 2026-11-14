package image

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kodnificent/berth/internal/compose"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func build(root string) compose.Build {
	return compose.Build{Service: "web", Context: root, Dockerfile: filepath.Join(root, "Dockerfile"), Spec: []byte(`{"context":"."}`)}
}

func compare(t *testing.T, berth, main compose.Build, lim Limits) Result {
	t.Helper()
	res, err := Compare(berth, NewTree(berth.Context), main, NewTree(main.Context), lim)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func same(t *testing.T, berth, main compose.Build) bool {
	t.Helper()
	return compare(t, berth, main, DefaultLimits).Same
}

func TestCompare(t *testing.T) {
	base := map[string]string{
		"Dockerfile":         "FROM busybox\nCOPY conf/ /etc/app/\nCOPY *.ini /etc/\n",
		".dockerignore":      "conf/*.log\n",
		"conf/app.conf":      "a=1\n",
		"conf/debug.log":     "noise\n",
		"php.ini":            "x=1\n",
		"app/Controller.php": "<?php\n",
	}
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, root string)
		same   bool
	}{
		{"identical", func(*testing.T, string) {}, true},
		{"a file the build doesn't read", func(t *testing.T, root string) {
			write(t, root, map[string]string{"app/Controller.php": "<?php // changed\n", "untracked.txt": "x"})
		}, true},
		{"an ignored file under a copied directory", func(t *testing.T, root string) {
			write(t, root, map[string]string{"conf/debug.log": "other noise\n"})
		}, true},
		{"a copied file", func(t *testing.T, root string) {
			write(t, root, map[string]string{"conf/app.conf": "a=2\n"})
		}, false},
		{"a new file under a copied directory", func(t *testing.T, root string) {
			write(t, root, map[string]string{"conf/extra.conf": "b=1\n"})
		}, false},
		{"a copied file missing", func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, "conf/app.conf")); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"a file a glob copies", func(t *testing.T, root string) {
			write(t, root, map[string]string{"opcache.ini": "y=1\n"})
		}, false},
		{"permissions of a copied file", func(t *testing.T, root string) {
			if err := os.Chmod(filepath.Join(root, "php.ini"), 0o755); err != nil {
				t.Fatal(err)
			}
			if info, err := os.Stat(filepath.Join(root, "php.ini")); err == nil && info.Mode().Perm() != 0o755 {
				t.Skipf("this filesystem keeps no permission bits (php.ini reads %v), so no build can tell them apart", info.Mode().Perm())
			}
		}, false},
		{"the Dockerfile", func(t *testing.T, root string) {
			write(t, root, map[string]string{"Dockerfile": base["Dockerfile"] + "RUN true\n"})
		}, false},
		{"the ignore file", func(t *testing.T, root string) {
			write(t, root, map[string]string{".dockerignore": "conf/*.log\nconf/*.tmp\n"})
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			main, berth := t.TempDir(), t.TempDir()
			write(t, main, base)
			write(t, berth, base)
			tc.change(t, berth)
			res := compare(t, build(berth), build(main), DefaultLimits)
			if res.Same != tc.same {
				t.Errorf("same = %v, want %v (%+v)", res.Same, tc.same, res)
			}
			if !res.Same && res.Reason == "" {
				t.Error("builds that differ have no reason")
			}
		})
	}
}

func TestCompareSpecAndTarget(t *testing.T) {
	main, berth := t.TempDir(), t.TempDir()
	files := map[string]string{
		"Dockerfile": "FROM busybox AS dev\nCOPY dev.txt /\nFROM busybox AS prod\nCOPY . /app\n",
		"dev.txt":    "d",
		"code.txt":   "c",
	}
	write(t, main, files)
	write(t, berth, files)
	write(t, berth, map[string]string{"code.txt": "changed"})
	dev, mainDev := build(berth), build(main)
	dev.Target, mainDev.Target = "dev", "dev"
	if !same(t, dev, mainDev) {
		t.Error("a file only the prod stage copies made the dev targets differ")
	}
	prod, mainProd := build(berth), build(main)
	prod.Target, mainProd.Target = "prod", "prod"
	if same(t, prod, mainProd) {
		t.Error("a file the prod stage copies didn't make the prod targets differ")
	}
	other := dev
	other.Spec = []byte(`{"context":".","args":{"X":"1"}}`)
	if same(t, other, mainDev) {
		t.Error("different build sections compared equal")
	}
}

func TestCompareNamedContext(t *testing.T) {
	main, berth, mainAssets, berthAssets := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	for _, root := range []string{main, berth} {
		write(t, root, map[string]string{"Dockerfile": "FROM busybox\nCOPY --from=assets /logo.svg /www/\n"})
	}
	write(t, mainAssets, map[string]string{"logo.svg": "<svg/>"})
	write(t, berthAssets, map[string]string{"logo.svg": "<svg></svg>"})
	b, m := build(berth), build(main)
	b.AdditionalContexts = map[string]string{"assets": berthAssets}
	m.AdditionalContexts = map[string]string{"assets": mainAssets}
	if same(t, b, m) {
		t.Error("a named context the build reads differs, but the builds compared equal")
	}
	b.AdditionalContexts = map[string]string{"assets": mainAssets}
	if res := compare(t, b, m, DefaultLimits); !res.Same || res.Read != 0 {
		t.Errorf("both builds read one shared named context, want equal without reading it: %+v", res)
	}
}

// twins writes the same files into two roots with the same modification times, as a
// copy-on-write clone leaves them.
func twins(t *testing.T, files map[string]string) (main, berth string) {
	t.Helper()
	main, berth = t.TempDir(), t.TempDir()
	write(t, main, files)
	write(t, berth, files)
	when := time.Now().Add(-time.Hour)
	for rel := range files {
		for _, root := range []string{main, berth} {
			if err := os.Chtimes(filepath.Join(root, rel), when, when); err != nil {
				t.Fatal(err)
			}
		}
	}
	return main, berth
}

func TestCompareSkipsReadingFilesWithTheSameSizeAndTime(t *testing.T) {
	big := strings.Repeat("x", 1<<20)
	main, berth := twins(t, map[string]string{"Dockerfile": "FROM busybox\nCOPY . /app\n", "data.bin": big})
	res := compare(t, build(berth), build(main), DefaultLimits)
	if !res.Same || res.Read != 0 {
		t.Errorf("copies with the same size and modification time: %+v, want equal with nothing read", res)
	}

	write(t, berth, map[string]string{"data.bin": big + "y"})
	if res := compare(t, build(berth), build(main), DefaultLimits); res.Same || res.Read != 0 {
		t.Errorf("files of different sizes: %+v, want different with nothing read", res)
	}
}

func TestCompareReadsFilesThatOnlyLookDifferent(t *testing.T) {
	main, berth := twins(t, map[string]string{"Dockerfile": "FROM busybox\nCOPY . /app\n", "data.bin": "aaaa"})
	now := time.Now()
	if err := os.Chtimes(filepath.Join(berth, "data.bin"), now, now); err != nil {
		t.Fatal(err)
	}
	if res := compare(t, build(berth), build(main), DefaultLimits); !res.Same || res.Read == 0 {
		t.Errorf("same content, new modification time: %+v, want equal after reading both", res)
	}
	write(t, berth, map[string]string{"data.bin": "aaab"})
	if res := compare(t, build(berth), build(main), DefaultLimits); res.Same {
		t.Errorf("same size, different content: %+v, want different", res)
	}
}

func TestCompareBudget(t *testing.T) {
	main, berth := twins(t, map[string]string{"Dockerfile": "FROM busybox\nCOPY . /app\n", "data.bin": strings.Repeat("x", 1<<20)})
	now := time.Now()
	if err := os.Chtimes(filepath.Join(berth, "data.bin"), now, now); err != nil {
		t.Fatal(err)
	}
	res := compare(t, build(berth), build(main), Limits{Bytes: 1 << 20, Entries: 100})
	if res.Same || !res.Skipped || res.Read != 0 {
		t.Errorf("deciding needs 2 MB with a 1 MB budget: %+v, want skipped with nothing read", res)
	}
	if !strings.Contains(res.Reason, "MB") {
		t.Errorf("reason %q doesn't say how much berth would have to read", res.Reason)
	}

	write(t, berth, map[string]string{"extra.txt": "only in the berth"})
	if res := compare(t, build(berth), build(main), Limits{Bytes: 1 << 20, Entries: 100}); res.Skipped || res.Same {
		t.Errorf("a file only the berth has settles it over the budget: %+v, want different, not skipped", res)
	}

	files := map[string]string{"Dockerfile": "FROM busybox\nCOPY . /app\n"}
	for i := range 20 {
		files[filepath.Join("pkgs", strings.Repeat("p", i+1))] = "x"
	}
	main, berth = twins(t, files)
	if res := compare(t, build(berth), build(main), Limits{Bytes: 1 << 20, Entries: 10}); res.Same || !res.Skipped {
		t.Errorf("a context with more files than the bound: %+v, want skipped", res)
	}

	// Files the build never reads still cost a walk when exclusions or a leading glob
	// make berth go through their directories.
	for name, f := range map[string]map[string]string{
		"an exclusion": {"Dockerfile": "FROM busybox\nCOPY . /app\n", ".dockerignore": "*\n!Dockerfile\n"},
		"a glob":       {"Dockerfile": "FROM busybox\nCOPY *.none ./\n"},
	} {
		for i := range 20 {
			f[filepath.Join("node_modules", strings.Repeat("p", i+1))] = "x"
		}
		main, berth = twins(t, f)
		if res := compare(t, build(berth), build(main), Limits{Bytes: 1 << 20, Entries: 10}); res.Same || !res.Skipped {
			t.Errorf("walking past the bound through ignored files with %s: %+v, want skipped", name, res)
		}
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestCompareSettlesTrackedFilesWithGit(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "main")
	write(t, main, map[string]string{"Dockerfile": "FROM busybox\nCOPY . /app\n", ".dockerignore": ".git\n", "app.php": strings.Repeat("<?php\n", 1000)})
	git(t, main, "init", "-q")
	git(t, main, "add", "-A")
	git(t, main, "-c", "user.name=t", "-c", "user.email=t@t.invalid", "commit", "-q", "-m", "init")
	berth := filepath.Join(root, "berth")
	time.Sleep(10 * time.Millisecond)
	git(t, main, "worktree", "add", "-q", "-b", "feat", berth)

	res := compare(t, build(berth), build(main), DefaultLimits)
	if !res.Same || res.Read != 0 {
		t.Errorf("a fresh worktree of main's commit: %+v, want equal with nothing read", res)
	}

	write(t, berth, map[string]string{"app.php": strings.Repeat("<?PHP\n", 1000)})
	if res := compare(t, build(berth), build(main), DefaultLimits); res.Same {
		t.Errorf("a tracked file edited in the berth: %+v, want different", res)
	}
}

func TestReadsWholeRepo(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"Dockerfile": "FROM busybox\nARG A=x\nCOPY ./docker/${A}.deb /tmp/\n"})
	b := build(root)
	if !ReadsWholeRepo(b, root) {
		t.Error("a build arg in a COPY source, context ., no .dockerignore: want the whole repository")
	}
	write(t, root, map[string]string{".dockerignore": "node_modules\n"})
	if ReadsWholeRepo(b, root) {
		t.Error("with a .dockerignore: want not the whole repository")
	}
	sub := filepath.Join(root, "web")
	write(t, sub, map[string]string{"Dockerfile": "FROM busybox\nCOPY . /app\n"})
	if ReadsWholeRepo(build(sub), root) {
		t.Error("a context below the repository root: want not the whole repository")
	}
}
