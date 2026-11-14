package initconf

import (
	"path/filepath"
	"slices"
	"strings"
)

// lockfile is the design's lockfile table: whether the dependency directory next to a
// lockfile is cloned, or installed again from the package manager's global cache.
type lockfile struct {
	name    string
	dir     string
	clone   bool
	install string
}

var lockfiles = []lockfile{
	{"composer.lock", "vendor", true, ""},
	{"package-lock.json", "node_modules", true, ""},
	{"yarn.lock", "node_modules", true, ""},
	{"pnpm-lock.yaml", "node_modules", false, "pnpm install --frozen-lockfile --prefer-offline"},
	{"bun.lock", "node_modules", false, "bun install --frozen-lockfile"},
	{"bun.lockb", "node_modules", false, "bun install --frozen-lockfile"},
	{"uv.lock", ".venv", false, "uv sync --frozen"},
	{"poetry.lock", ".venv", false, "poetry install"},
}

// LockfileNames are the lockfiles init looks for at main's top level.
func LockfileNames() []string {
	out := make([]string, len(lockfiles))
	for i, l := range lockfiles {
		out[i] = l.name
	}
	return out
}

// DependencyDir reports whether path names a directory a package manager installs into,
// the kind of clone copy-on-write turns from minutes into a moment.
func DependencyDir(path string) bool {
	base := filepath.Base(path)
	for _, l := range lockfiles {
		if l.dir == base {
			return true
		}
	}
	return false
}

var (
	packageManagers = []string{"composer", "npm", "yarn", "pnpm", "bun", "pip", "uv", "poetry", "cargo", "go", "gomod", "gradle", "maven", "m2", "nuget", "gem", "gems", "bundle", "bundler", "deno"}
	cacheWords      = []string{"cache", "caches", "store"}
	// cachePaths end the paths package managers keep their global cache at, whichever
	// user's home they sit in.
	cachePaths = []string{"/.npm", "/.cache/pip", "/.cache/uv", "/.cache/pypoetry", "/.composer/cache", "/.cache/composer",
		"/.cache/yarn", "/.yarn/berry/cache", "/.bun/install/cache", "/.pnpm-store", "/pnpm/store", "/.cargo/registry",
		"/cargo/registry", "/go/pkg/mod", "/.gradle/caches", "/.m2/repository", "/.nuget/packages", "/.cache/deno"}
)

// isCache reports whether a volume is clearly a package manager's cache: its key names a
// package manager and a cache, as composer_cache does, or it is mounted where a package
// manager keeps its cache.
func isCache(key string, mounts []string) bool {
	tokens := strings.FieldsFunc(strings.ToLower(key), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
	var manager, cache bool
	for _, t := range tokens {
		manager = manager || slices.Contains(packageManagers, t)
		cache = cache || slices.Contains(cacheWords, t)
	}
	if manager && cache {
		return true
	}
	for _, m := range mounts {
		m = strings.TrimRight(m, "/")
		for _, p := range cachePaths {
			if strings.HasSuffix(m, p) {
				return true
			}
		}
	}
	return false
}
