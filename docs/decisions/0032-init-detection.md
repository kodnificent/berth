# 0032. berth init: where it runs, what it detects, and how it writes suggestions

Status: accepted; presets add to what init writes, in [0044](0044-init-presets.md)

## Context
`init` writes a starting config in the main worktree, never overwrites one, and prints doctor's main findings when done (design). It writes only what it can detect for certain, from the compose project main would use, and comments out anything that needs intent, with a one-line reason. The design leaves open how `init` refuses, how each item is detected and how a suggestion is spelled so that uncommenting it gives a valid config. Nothing here may know a framework.

## Decision
**Running and refusing**
- **Only in main:** a linked worktree is `usage`, exit 1, naming main's path, because the config `init` would write is main's: `berth.toml` at main's top level, or the personal layer, which berth reads from main anyway (0031). A bare-repository layout has no main working tree, so `init` refuses there too.
- **Never overwriting:** an existing target is refused before anything is detected, with its own code `config_exists`, exit 1, naming the file. `--private` with `.git/berth.toml` present refuses the same way, since a repository has one personal layer. The file is created with `O_EXCL`, so one that appears between the check and the write is refused too, not replaced.
- **Checked before writing:** the rendered text goes through `config.Parse`, the same decode, unknown-key check and validation `config.Load` runs, as if it were the only layer. A refusal there is a bug in berth and writes nothing.
- **Exit:** `init` exits 0 once the file is written, whatever doctor's main-worktree findings (`mainFindings`, 0030) say: the user sees what's left, and the file is there to fix.

**Source.** Main's project is `planCompose` with an empty config, the loading `add` and doctor already use: `COMPOSE_FILE` from main's environment, else compose's default names, every profile active.

**Written as config**
- **Ports:** `ports.Wanted` with an empty config, so init writes exactly the policy berth would apply with no config: each published port keyed `"<service>:<container port>"` as `assign` when main publishes it on a fixed host port, `ephemeral` when main lets Docker pick. Identical entries collapse as in the override file (0018). Each line ends with the host port main uses.
- **Volumes:** every volume main declares. `share` when it is clearly a package-manager cache, `fresh` when berth can't copy it (a non-local driver, or a `device` option, the same rules the clone step refuses with in 0020), otherwise `clone`. An external volume gets no role, only a comment: main and every berth already use the same volume. Cache detection is ecosystem data in `internal/initconf/ecosystem.go`: the key's words name a package manager (composer, npm, yarn, pnpm, bun, pip, uv, poetry, cargo, go, gradle, maven, nuget, gem, bundler, deno) and a cache (`cache`, `caches`, `store`), or the volume is mounted at a path a package manager keeps its global cache at, such as `/root/.npm` or `/go/pkg/mod`. A bare `cache` volume is not a package manager's, so it is cloned.
- **Dependency directories:** the design's lockfile table, for lockfiles git tracks at main's top level. `vendor` (`composer.lock`) and `node_modules` (`package-lock.json`, `yarn.lock`) go under `[files] clone` when main has them as gitignored directories, with the lockfile named. When a service bind-mounts main's directory and mounts a named volume over that directory inside it (`.:/app` with `vendor:/app/vendor`), the service never sees the host's copy, so init writes a note instead: the volume covers it. Berth clones nothing the config doesn't name (design, Cloned files), so writing them is what makes the table hold.

**Written as comments, for information:** the compose files main reads and its gitignored env files. Berth reads and clones both with no config, so a key for them would only repeat the default.

**Suggestions, commented out with a reason line**
- **Env keys:** each key in main's `.env` (read with compose's dotenv parser, so `${OTHER}` is expanded as compose sees it) whose value holds main's host or a port main publishes. Main's host is the `localhost` hostname, with any subdomain: `localhost` and `app.localhost` become `{host}` and `app.{host}`; `mylocalhost` and `localhost.example.com` are left alone. A published port counts when it is the whole value or follows a colon, as in a URL or `host:port`, and becomes `{port.<service>}` when the service has one assigned port, else `{port.<service>:<container port>}`. `127.0.0.1` stays, so a loopback URL keeps its loopback host with the berth's port. Skipped: keys main's compose files read (`${WEB_PORT}`), which berth never changes; `COMPOSE_*` keys, which berth owns or compose reads; ports after a compose service name (`http://db:3306`), which are container ports; and values with URL credentials (`://user:pass@`), which get a reason line only, so a secret from `.env` never lands in a committed file. Literal braces are doubled so the template means the value. The reason names what matched and flags a host port that equals the container port, where the value may be meant for containers.
- **`[berth] url`:** when every URL-shaped suggestion agrees on one `http(s)://{host}[:{port.X}]`, it is suggested.
- **`[compose] exclude`:** services under compose profiles, with their profiles.
- **`[compose] files`:** compose files (`*compose*.yml`, `*compose*.yaml`, tracked or untracked but not ignored) at main's top level or one directory down that main doesn't read, other than `compose.berth.yml`.
- **`[files] clone`:** the small top-level gitignored files doctor's `ignored_file` warning looks for (0030), from the same function.
- **`[hooks] post-add`:** the install command for `pnpm-lock.yaml`, `bun.lock`, `uv.lock` and `poetry.lock`, whose dependencies the design says not to clone, unless a compose volume covers the directory.

**Spelling.** A suggested scalar is one `# KEY = value` line under its real table header, so deleting `# ` gives a valid config; a unit test uncomments every suggestion and loads the result. Arrays are written one entry per line, with suggested entries as `#   "path",` lines inside the array. When an array would have no active entry the whole key is commented, because an empty array in one layer replaces a lower layer's array (0007).

## Options considered
- `init` in a linked worktree writing that worktree's `berth.toml`: a branch-only config is possible, but every new worktree would lack it.
- `main_worktree` or a new code for a linked worktree: `main_worktree` means the opposite (a berth command pointed at main). `usage` already means "not how this command is run".
- `error` for an existing config: agents can't tell "already set up" from a failure without parsing English. `config_exists` costs one row in 0002's table.
- Exiting non-zero when doctor finds problems: the write succeeded, and doctor's own exit code is one command away. Findings are in the output either way.
- Validating with a temporary file and `config.LoadFiles`: same result with a file to clean up.
- Writing the compose and env files as config: there is no key for main's own compose files, and `[files] clone` of `.env` would clone it twice.
- Writing env suggestions as active `[env.set]` keys: an env value that only looks like a host or port would then change in every berth, and the design has them as suggestions.
- `[env] url` and `[env] host` lists instead of templates: they can't express `app.{host}:{port.web}` or a mixed list.
- Treating `127.0.0.1` as `{host}`: `*.localhost` names don't always resolve outside browsers (design), and a loopback value is usually a server-to-server call.
- Detecting caches by mount path only: a project can mount a package manager's cache at a path of its own choosing, set through an env variable, and then the volume's name is the only clear signal.
- Excluding every profile service by default: a service under a profile, such as a database for tests, can be one the project's own commands need in a berth.

## Evidence
`internal/initconf/initconf_test.go` covers templates (hosts, subdomains, ports, service hosts, literal braces, near misses), cache detection, and rendering: every kind of line, nothing secret or berth-owned, loading as written and with suggestions uncommented. `e2e/init_test.go` covers the refusals in human and `--json` output with nothing changed, the dry run, `init` only in main, every port compose reports as published and every volume with a package-manager cache shared, and the env suggestions for URL, list, loopback and host values, with doctor in main clean once they are uncommented.

## Undoing it
The command is `internal/cli/initcmd.go`, with gathering in `detect`; `config.Parse` is in `internal/config/config.go`; rendering and the ecosystem lists are `internal/initconf`. Each detection rule is local: dropping one changes only what init writes, never how a config is read. `config_exists` is part of the `--json` schema and is never renamed (0002).
