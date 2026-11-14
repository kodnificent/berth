# Setting up berth in a repository

Berth works with no config, cloning every compose service into each worktree. Config adds precision: which ports stay stable, which volumes and directories are copied, which env keys get the worktree's URL. Set it up once per repository, in the main worktree. Every linked worktree reads its config from there.

## Steps

1. Work in the main worktree, the repository's original checkout. `berth init` refuses to run anywhere else.
2. Choose where the config goes. `berth init` writes `berth.toml`, committed and shared with the team. `berth init --private` writes `berth.local.toml`, kept out of `git status`, with nothing committed. Use `--private` unless the user wants berth in the repository.
3. Run `berth init --dry-run` to print the file without writing it, then `berth init` (with `--private` if chosen) to write it. It never overwrites a config: with one already there it exits `1` with error code `config_exists`, naming the file, and you edit that file instead.
4. Read the file. What berth detected for certain is written: published ports under `[compose.ports]`, named volumes under `[compose.volumes]`, dependency directories under `[files] clone`. What needs intent is commented out, with a reason above each line. Uncomment a suggestion by deleting its `# `. `init` also applies the presets whose files it finds (`laravel`, `vite`): their suggestions come under a comment naming the preset, active when a file in the repository confirms them, commented with the reason when not. `--no-presets` applies none, and `--preset <name>` applies one by name.
5. Decide each suggestion. Map env keys that hold main's URL, host or port, so each worktree gets its own. Exclude services a worktree doesn't need. Clone small gitignored files the tests need, such as `phpunit.xml`. Keep a `post-add` install when dependencies aren't cloned. Where `berth doctor` warns that dependency clones will be full copies (ext4, or main and worktrees on different filesystems), each `add` copies them byte for byte, and installing from the package manager's cache in `post-add` may be faster.
6. Run `berth doctor` in the main worktree. It checks the config, the compose files a berth would read and the config's references into them. Fix every problem; it exits `0` when there are none. Warnings never fail it and may be intended, but read each one.
7. Try it in a scratch worktree, then remove what you made:

```sh
git worktree add .worktrees/berth-try
berth add .worktrees/berth-try
berth status --wait .worktrees/berth-try
berth doctor .worktrees/berth-try
# start the app in the scratch worktree, as you would in main, then:
berth doctor --probe .worktrees/berth-try
berth remove .worktrees/berth-try
git worktree remove .worktrees/berth-try
```

`berth doctor --probe` requests the running app at the worktree's URL and reports a cookie whose domain other worktrees share, such as `Domain=.myapp.localhost`, and a redirect to main's host or port. Berth never starts the app, so with nothing running it reports the URL as not verified. It requests only ports berth assigned the worktree, so set `[berth] url` to one, such as `url = "http://{host}:{port.web}"`. List pages that set cookies or redirect, such as a login page, under `[doctor] probe` (`probe = ["http://{host}:{port.web}/login"]`). When a finding in `berth doctor --probe --json` has a `fix`, merge its `config` into the config file you chose and run `berth add` again.

When the team uses `berth proxy`, set `[proxy] port` to the port the app serves on. The proxy reports the same wide cookies the probe does, and plain `berth doctor` shows what it saw. `[proxy] cookies = "rewrite"` keeps such a cookie inside each worktree behind the proxy, but the app still sets it wide on its port URL, so prefer the `fix`. Leave `berth proxy trust` to the user: it changes the system's trust store through a system prompt.

## Config files

- `berth.toml` at the top of the main worktree: committed team defaults.
- `berth.local.toml` at the top of the main worktree, or `.git/berth.toml`: the personal layer, never committed. Use one, not both; with both, berth exits `5` and names them. `git clean -fdX` deletes `berth.local.toml`, but not `.git/berth.toml`.
- `~/.config/berth/config.toml`: machine defaults, such as the port pool under `[pool]`.
- Layers override key by key in that order: personal, then repo, then user, then the built-in defaults. An array replaces the lower layer's whole.
- A `berth.local.toml` inside a linked worktree is never read; `berth doctor` warns about one.
- `berth config path` prints the personal config's path, `--repo` and `--user` the other layers'. `berth config edit` opens the personal config in `$VISUAL` or `$EDITOR`, creating it first, and checks it when the editor exits.
- Berth refuses a config it doesn't understand: an unknown key, a wrong type or a template naming an undefined port exits `5`, naming the file and the key. Run `berth doctor` after every edit.

## Databases on a host server

When main's `.env` points at a MySQL server on the machine (DBngin, Herd, brew) rather than one in compose, every worktree would share main's databases. `[state.mysql]` gives each berth its own, next to main's on the same server:

```toml
[state.mysql]
from = "env:DB_*"                  # main's .env sets DB_HOST, DB_PORT, DB_USERNAME, DB_PASSWORD
prefix = "env:DB_DATABASE_PREFIX"  # only if the app derives its database names from a prefix
default = "skip"                   # databases not listed are left alone

[state.mysql.databases]
central = "dump"                   # with prefix, keys are the part after it: app_central
"*" = "fresh"                      # every other database under main's prefix
```

- Ask the user which databases a worktree needs of its own. `dump` copies main's in the background, consistently and without stopping main's InnoDB writes; `fresh` creates it empty, for `post-add` to migrate or seed; `skip` ignores it; `none` keeps using main's database itself, and `doctor` warns that state is shared; `snapshot` clones from a copy the user takes with `berth snapshot` in the main worktree, so main never pauses during `add` or `reset`, and fails naming `berth snapshot` until one exists. Prefer `dump` or `fresh` over `none`.
- `dump` needs no `mysqldump` and no Docker. A table that isn't InnoDB (MyISAM, MEMORY) has its writes held while it is read, and `add` names it first; suggest converting it to InnoDB if that matters. Stored procedures, functions and events are not copied, and views or triggers that name another database, or call a routine, are left out; `berth status` lists them under the database's notes, so recreate them in `post-add` if the app needs them.
- A database can also start from a seed file, `{ from = "file:database/seeds/tenant.sql" }` (plain SQL or gzip, resolved in the main worktree), or a script of the user's, `{ command = "./scripts/copy-reports.sh" }`. Both run in the background like `dump`. A seed file is the user's own SQL and runs as written with main's login: berth doesn't inspect it, so a seed that names another database reaches it, the same as with the `mysql` client. Suggest `mysqldump` of a single database, without `--databases`, so the file holds no `USE`. The script runs once, in the main worktree, with `BERTH_SOURCE_DB`, `BERTH_TARGET_DB`, `BERTH_DB_HOST`, `BERTH_DB_PORT`, `BERTH_DB_USER` and `BERTH_DB_PASSWORD` in its environment, and its exit status decides ready or failed; `berth status` names the log of its output. It runs with main's login, so it is trusted code, like `post-add`.
- With `prefix`, the berth gets a prefix of its own (`app_` becomes `app_feat_x__`) and berth writes it to that key. Without it, list each database by name: `myapp` becomes `myapp__feat_x`. Patterns such as `"*"` need `prefix`.
- Berth rewrites every key in main's `.env` whose value names a database the berth has its own of, such as `DB_DATABASE`. Don't also set those keys, or the prefix key, under `[env.set]` or `[ports]`: berth exits `5` naming the key.
- `from` must name keys main's `.env` sets, and `prefix` a key it sets; otherwise `berth add` exits `5`. A server berth can't reach exits `1` and creates nothing.
- Berth never stores or prints the user or password. `berth remove` and `berth prune` drop a berth's databases, including any the app made under its prefix, reading only the berth's record and its own `.env`. For an orphan whose worktree is gone, they read the login from the shell, so export `DB_USERNAME` and `DB_PASSWORD` (under your `from` prefix) and run `berth prune` again.

A Postgres server on the machine is configured the same way, under `[state.postgres]`:

```toml
[state.postgres]
from = "env:DB_*"
prefix = "env:DB_DATABASE_PREFIX"
default = "skip"

[state.postgres.databases]
central = "fresh"
```

- On Postgres the strategies are `template`, `fresh`, `skip`, `none` and `{ command = "..." }`, with the same `BERTH_*` variables. `template` copies main's database inside the server, in the background like `dump`. `dump`, seed files and `snapshot` exit `5`, naming the database: suggest `template` for a copy of main's data, or `fresh` with `post-add` migrating, or a `command` script.
- A `template` copy fails while main has any other session on that database, naming each session's application and client address. Suggest stopping main's app or workers before `berth add`, or, only if the user agrees to main's sessions ending with their open transactions rolled back, `disconnect = true` under `[state.postgres]`. New connections to main's database wait for the copy; `add` says so first.
- Names are cut to Postgres's 63 bytes. `berth remove`, `prune` and `reset` drop a berth's databases even while its app is connected, ending only the sessions on those databases.
- A project names one host server: `[state.mysql]` or `[state.postgres]`, never both.
- When main's `.env` holds the server as one URL, such as `DATABASE_URL=postgres://user:pass@127.0.0.1:5432/myapp`, use `from = "env:DATABASE_URL"` under either table and list the URL's database, here `myapp`. Berth writes the berth's URL, main's with only the database changed, to that key, and shows it with the login taken out. A URL naming a database with no entry, or with `prefix` one outside main's prefix, exits `5`, as does a URL with several hosts or a query setting the host, port or database (`?dbname=`): suggest moving those into the URL itself.

## Redis on a host server

When main's `.env` points at a Redis server on the machine rather than one in compose, every worktree would share main's keys. `[state.redis]` gives each berth a key prefix of its own on the same server:

```toml
[state.redis]
strategy = "prefix"
from = "env:REDIS_*"          # main's .env sets REDIS_HOST, REDIS_PORT, and REDIS_USERNAME, REDIS_PASSWORD when needed
prefix = "env:REDIS_PREFIX"   # the key the app puts in front of every Redis key
```

- The berth's prefix is its name and two underscores in front of main's: `app_` becomes `feat_x__app_`, written to `REDIS_PREFIX`. Check that the app really reads that key for its Redis prefix; if it doesn't, berths still share keys.
- Main's `.env` must set the prefix to something non-empty, or `berth add` exits `1` saying to set it. Ask the user before changing main's prefix: main's existing keys stay under the old one.
- `prefix` is the only strategy; anything else exits `5`. Don't also set the prefix key under `[env.set]` or `[ports]`, as the `laravel` preset suggests for `berth init`: berth exits `5` naming the key, so remove that entry.
- `berth remove`, `prune` and `reset` delete every key under the berth's prefix on every logical database, and nothing else. A prefix keeps keys apart, not commands: `FLUSHDB` or `FLUSHALL` from anywhere still clears every berth's keys, and pub/sub channels are shared. When that matters, suggest a Redis service in compose instead.

## SQLite databases

When the app keeps a SQLite database in the worktree, every worktree would otherwise start with none, or with a copy that `[files] clone` could catch mid-write. List each database file under `[state.sqlite]`:

```toml
[state.sqlite]
files = ["database/app.sqlite"]    # paths inside the worktree, as main has them
```

- `berth add` copies each file from main as one point in time, even while main's app writes, WAL included, and instantly on copy-on-write filesystems. Elsewhere a database not in WAL mode is locked while its bytes copy, so main's app may see `database is locked`; `add` says so first. `berth status` lists each file and how it was copied.
- If main's app writes the database from a container through a Docker Desktop bind mount, berth can't hold its writes off and the copy may be torn: tell the user to stop that container before `berth add` or `berth reset`.
- Don't also list a database file, or a directory holding one, under `[files] clone`: berth exits `5` naming `state.sqlite.files`.
- A file main doesn't have is reported and the berth gets none. A file the worktree already has is left alone, and `add --wait` exits `4` naming it.
- `berth reset` copies each file from main again; `berth remove` deletes the berth's copies, with SQLite's `-wal`, `-shm` and `-journal` files beside them, and nothing else in the worktree.

## Hooks berth runs

- `[hooks] post-add` runs after cloning finishes, with `BERTH_PATH`, `BERTH_SOURCE` and `BERTH_CHANGED_FILES` set. Use it for installs and seeding, such as `composer install` or migrations.
- `[hooks] pre-remove` runs before `berth remove` deletes anything; if it fails, nothing is removed.
- On Windows, a dependency directory is cloned instantly only on a Dev Drive; on NTFS it's a full copy and `add` and `doctor` say so. A cloned path holding a symlink needs Developer Mode, or an administrator: without either, that path fails naming Developer Mode, and the other paths still finish. Turn it on and run `berth add` again. Junctions need neither.
- Hooks and `command` scripts run with `sh -c`; on Windows that is the `sh` Git for Windows installs, so write them as POSIX shell, not PowerShell or `cmd`. There, `pwd` prints `/c/...` paths while `BERTH_PATH` and `BERTH_SOURCE` are Windows paths; `cygpath -w` converts for a Windows program. Without Git for Windows each script fails naming `sh.exe`.

## The git hook is opt-in

- Without it, agents run `berth add` in a worktree when they are about to start the app or its tests. That is the default; leave it unless the user asks for more.
- `berth hook install` adds a `post-checkout` hook so every `git worktree add` gets a berth, including worktrees that only ever edit code. Install it only when the user wants that. `berth hook install --dry-run` says what it would do, and `berth hook uninstall` removes a hook berth wrote.
- When `core.hooksPath` points into the checkout, as husky and `.githooks/` setups do, `berth hook install` writes nothing, exits `1` and prints the line to add to that folder's `post-checkout`, which calls `berth hook run`. That folder is usually committed, so add the line only if the user agrees.
- `berth doctor` warns when a hook berth installed is gone, replaced, edited or no longer runs.
