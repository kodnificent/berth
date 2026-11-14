# 0030. doctor in the main worktree, and its warnings

Status: accepted

## Context
`berth doctor` refused the main worktree with `main_worktree` (0002, 0028), so a config could only be checked by creating a berth. Doctor also stayed silent about two things that cost time in practice: a gitignored local test config in main that the berth lacked, which sent tests to the wrong database, and `VITE_*` keys the berth set apart from main, which Vite had already inlined into build output. Berth stays framework-agnostic and never rewrites build output (design, Principles and Cloned files).

## Decision
- **Main:** `doctor` in the main worktree runs the `config` check, then plans the compose files a berth would read (0017) and checks them with `docker compose -f <each file> config --quiet` in main, reports `container_name` in them (0028), and reports `config` problems for `[compose] exclude`, `[compose.ports]` and `[compose.volumes]` entries that name a service or volume the project lacks. The per-berth checks (`berth`, `shadowing`, `ports`, `source_path` and the warnings below) are skipped. Nothing is created or registered. `main_worktree` stays for `add`, `status`, `env` and `remove`.
- **Warnings:** a third `level`, `warning`, for findings that may be intended. Warnings print with `(warning)` and never change the exit code.
- **`build_env`:** one warning naming every managed key whose name starts with a build-time prefix (`VITE_`, `NEXT_PUBLIC_`, `REACT_APP_`, `PUBLIC_`, `NUXT_PUBLIC_`) and whose value differs from main's `.env` (read with compose's dotenv parser; a key main lacks counts as different). The prefixes are ecosystem data in `internal/envfile/buildtime.go`, like the lockfile table, not framework logic.
- **`ignored_file`:** one warning per gitignored regular file at the top level of main that the berth lacks and `[files] clone` doesn't list, when the file is at most 64 KiB, its name doesn't start with `.`, doesn't end in `~` and has none of the extensions `.log`, `.cache`, `.pid`, `.tmp`, `.swp`, `.bak`, `.orig`. The rule is deliberately narrow: local config (a `local.xml`, a credentials file) is small and sits at the top; dotfiles are `.env*`, which berth clones itself, or tool caches; large files are lockfiles and dumps; nested files are build output and caches. In the e2e fixture, of `local.xml`, a 100 KB file, a `.log`, a dotfile and a nested file, the rule picks only `local.xml`. Adding the file to `[files] clone` silences it.

## Options considered
- `status` on main instead of `doctor`: an agent setting berth up reaches for `doctor`, and the design names it as the setup check.
- Validating the berth's would-be project with a generated override in main: needs ports assigned from the pool without a registry entry, which `add` does under the registry lock.
- Warning about every gitignored file in main the berth lacks: that includes `vendor/`, `node_modules/`, build output and caches, every time.
- A per-key `build_env` finding: several findings for one cause, and the fix is one rebuild.
- Rewriting the inlined values in build output: rewriting file contents is ruled out (design, Cloned files), and bundlers hash and minify them.

## Evidence
`e2e/doctor_test.go` covers: main clean with no config and no berths; invalid config, an invalid `[compose] files` file, a `container_name` in it and three bad references each found, with nothing registered; and exit `0` with warnings, where `VITE_API_URL` is reported but not an equal `VITE_SAME` or a non-build `APP_NAME`, and `local.xml` is reported until it is cloned. `internal/envfile` tests cover the build-time prefixes.

## Undoing it
`mainFindings`, `configRefFindings`, `buildEnvFindings` and `ignoredFileFindings` in `internal/cli/doctor.go`. Restoring `resolveBerthWorktree` in `runDoctor` brings back the refusal in main.
