---
title: Commands
description: Every berth command, flag and exit code.
sidebar:
  order: 1
---

## add

`berth add [path]` makes the worktree's berth: its ports, names and files, and the data copies, which continue in the background. Run it when you are about to start the app or its tests; worktrees that only edit code never need one. Running it again keeps the berth it already has. `--wait` returns once every copy has finished and exits `4` if one failed. In your main checkout it exits `1` with error code `main_worktree`. When the worktree has compose files and `docker compose version` is older than 2.24.4, which would misread the worktree's `compose.berth.yml`, it changes nothing and exits `1` with `compose_too_old` (see [Platforms](/docs/reference/platforms/#docker-compose)).

`add` ends with where the berth is and what to do next: its name, its URL (its host when it has none), each port berth assigned, each volume, database and SQLite file with its state, `post-add`, and the `next` hints `berth status` gives. Each row reads as it does in `status`. Run it again and it says `Already have berth` with the states as they are then.

```text
Added berth 9f2c4e1ab07d3355 for /code/shop/.worktrees/fix-checkout
name          fix-checkout
url           http://fix-checkout.localhost:20000
port web:80   20000 (assign)
port vite     20001 (assign)
volume mysql  cloning (12%, 130 MB of 1.1 GB)
next          volume mysql is still cloning; run berth status --wait before starting the app
```

On a terminal, `add` and `status` put a tick and a colour on each state, one for ready, one for cloning or still running and one for failed, and dim the labels. Through a pipe, with `NO_COLOR` set, with `TERM=dumb` or on a Windows console that doesn't take escape codes, the same rows print as plain text. `--json` never changes. When `add --wait` exits `4`, the summary still prints first, with the failed row.

For each `[files] clone` path it copies, `add` prints on stderr how and how fast: `berth: cloned vendor (cow) in 41ms`, where `cow` is copy-on-write and `copy` a full copy. On NTFS, a full copy is followed by a line naming Dev Drive.

## remove

`berth remove [path]` deletes the worktree's berth: its compose containers and networks, the volumes and images berth created for it, its databases on a host server (those berth recorded and any under its prefix), its copies of SQLite files with the files SQLite keeps beside them, and the files berth wrote or copied. A `.env` the worktree had before keeps everything outside the managed block. Runs `pre-remove` first and stops if it fails. Prints what it removed. A file berth copied whose parent directory has since become a symlink or junction, as a branch tracking one can make it, is left where it is and named on stderr, since deleting it would go through the link.

`-n`, `--dry-run` lists what would be removed. With `--json`: `{"id", "worktree", "dry_run", "removed": [{"kind", "name"}]}`, where `kind` is `container`, `network`, `volume`, `image`, `database`, `redis_keys`, `sqlite`, `file` or `env_block`; `redis_keys` is every key under the worktree's Redis prefix, named by the prefix, on every logical database. When the host database or Redis server can't be reached, `remove` deletes nothing, keeps the berth and exits `1`; run it again once the server is back.

## list

`berth list` shows every berth on the machine, across repositories: its id, name, host, whether it is orphaned or locked, when it was last used, its disk use, its worktree and its repository. Disk use is the apparent size of what berth copied and wrote, as it recorded it then, so `list` returns at once however large a worktree's dependencies are. Copy-on-write copies share blocks with main, so they take less real disk than this, and a database that grew since it was copied counts at its copied size.

Last used, the `USED` column, is the latest of three things: a berth command run on the berth (`status`, `env`, `add`, `exec`, `reset` or `doctor`, but not `list`, and not berth commands your hooks run), a change git sees in its worktree (an edited, created or deleted file, staging or a commit; ignored files such as dependencies don't count), and one of its containers running. A berth with a running container shows `in use`, and its last use is now. A berth made before berth kept last use counts from when it was created. If Docker can't be reached, `list` still answers, warns on stderr, and can't tell which berths are in use.

With `--json`: `{"berths": [{"id", "worktree", "repository", "source", "created_at", "name", "orphaned", "locked", "disk_bytes", "host", "ports", "last_used", "in_use"}]}`. `created_at` and `last_used` are RFC 3339 times. `in_use` is `true` or `false`, and `null` when Docker couldn't be reached.

## status

`berth status [path]` shows the worktree's ports, URL, host, compose project, managed env keys, the files berth wrote, each volume copy, host database and SQLite file, and `post-add`. With `--json`, each entry under `state` has a `kind`, `volume`, `database` or `sqlite`, with its `source`, `target` (the worktree's volume, database or file), `strategy` and `status`; volume entries also keep `volume`, and a SQLite file main doesn't have is `ready` with `source_missing`. A copy in progress shows how far it is, such as `cloning (62%, 680 MB of 1.1 GB)`; with `--json` it carries `progress` (0 to 1), `copied_bytes` and `total_bytes`. A database with a `command` names its log with `log` (its script's output). For each service the worktree builds, it shows whether the worktree runs main's image or builds on `up`, and why, such as `build web  builds on up: build inputs differ from main's: the Dockerfile differs from main's`; with `--json` these are `builds`, each with `service`, `shared`, `reason` and `read_bytes`.

Status ends with what to do next, one `next` line per hint, and `--json` carries the same hints as `next`, a list of strings:

```text
next             volume db is still cloning; run berth status --wait before starting the app
```

While a copy or `post-add` is still running, the hint is to run `berth status --wait`. Once everything is ready, it is to use the worktree's own URL, by name, rather than `localhost` or main's. Inside a hook berth runs, where `BERTH_PATH` is set, no hint suggests `--wait`, since berth is waiting on that hook to finish. In a worktree with no berth, `status` exits `1` with error code `no_berth` and a hint to run `berth add` before starting the app; in your main checkout it exits `1` with `main_worktree` and a hint that main is the source and berths live in linked worktrees.

`--wait` returns once every copy and `post-add` have finished, then prints the status. It exits `4` if a copy failed and `1` if `post-add` failed, and with `--json` adds `error` to the status. `berth add --wait` waits for copies only.

## env

`berth env [path]` prints the worktree's managed variables, the keys in the managed `.env` block, as `export` lines a POSIX shell can evaluate. Use it for tools that ignore `.env`:

```bash
eval "$(berth env)"
```

```text
export APP_URL='http://fix-checkout.localhost:20000'
export COMPOSE_FILE='compose.yml:compose.berth.yml'
export COMPOSE_PROJECT_NAME='shop-fix-checkout'
export VITE_PORT='20001'
```

Keys are sorted, and each value is single-quoted so the shell takes it as written. The values are the ones your app reads from `.env`, so a database URL keeps its login here.

With `--json`: `{"env": {"<KEY>": "<value>"}}`. There, a database URL has its login and query string replaced by `<redacted>`.

In a worktree with no berth, `env` prints nothing and exits `1` with error code `no_berth`; in your main checkout it exits `1` with `main_worktree`.

## exec

`berth exec [path] -- <command> [args]` runs a command with every variable `berth env` prints set, for tools that ignore `.env`. A managed key exported in your shell is overridden by the berth's value. The command runs in the current directory when that is inside the worktree, and at the worktree's top otherwise. It reads berth's stdin, writes its own stdout and stderr, receives SIGINT and SIGTERM sent to `berth exec`, and its exit code is `exec`'s.

In a worktree with no berth, `exec` runs nothing and exits `1` with `no_berth`. Because the command can exit `1` too, check `--json`: berth's own failures print the error envelope on stdout, and the command's own failures print nothing extra. Native commands that read `.env` are the normal path; `exec` is the escape hatch.

## reset

`berth reset [path]` throws away what the worktree wrote to its `clone` volumes and copies them again from main's volumes as they are now. Ports, name, host, compose project and the managed `.env` block stay the same, and so does each volume's name, so the worktree's compose project finds its data where it was. Like `add`, it says first which of main's services will pause for the copy, then returns while the copy runs in the background; `berth status --wait` waits for it. A volume main no longer has is left empty. `share` volumes, cloned files and dependency directories are left as they are.

Stop the worktree's containers first: while any of them run, `reset` changes nothing, names them and exits `1`. It does the same for a worktree with a compose project when Docker can't be reached, since it can't tell what is running. It only ever touches volumes berth created for this worktree, never another volume that happens to carry its compose project's name. If a copy is stopped partway, `status` reports it as failed, and running `berth reset` again fixes it.

On a host database server (`[state.mysql]` or `[state.postgres]`), `reset` drops every database berth recorded as the worktree's, and every one under its recorded prefix, then creates its `fresh` databases again, empty, copies its `dump` and `template` databases from main again in the background, as main is now, loads its seed files as they are now, and runs its `command` scripts again. Like `add`, it first names any of main's tables whose writes will pause for the copy, and any Postgres database whose new connections will. It checks first that main's `.env` still points at the server those databases are on; when it points at another, `reset` drops nothing and exits `1`. `--dry-run` and `--json` list the databases it discards, whether or not the project has volumes. While a copy is still running, `reset` changes nothing and exits `1`; a copy stopped partway reads as failed, and running `berth reset` again fixes it. A `snapshot` database with no snapshot, or a seed file that is missing, makes `reset` exit `1` before anything is dropped, `--dry-run` included.

With `[state.redis]`, `reset` deletes every key under the worktree's recorded Redis prefix, on every logical database of the recorded server, and the worktree starts again with none. When main's `.env` now points at another Redis server, it deletes nothing and exits `1`. `--dry-run` and `--json` list them as one `redis_keys` entry named by the prefix.

With `[state.sqlite]`, `reset` deletes each of the worktree's copies, with the files SQLite keeps beside them, and copies main's file again as it is now, and copies any listed file the worktree has none of. A file main no longer has leaves the worktree with none.

`-n`, `--dry-run` lists the volumes, databases and SQLite files that would be discarded and changes nothing. With `--json`: `{"id", "worktree", "dry_run", "discarded": [{"kind", "name", "volume", "target"}]}`, where `kind` is `volume`, `database`, `redis_keys` or `sqlite`; for a volume `name` is its key in the compose file and `volume` and `target` the worktree's volume, for a database `name` and `target` are the database, for Redis keys they are the worktree's prefix, and for a SQLite file they are its path in the worktree.

## snapshot

`berth snapshot` copies every database configured as `snapshot` under `[state.mysql]` as main has it now, and keeps it in berth's state directory, per repository, until the next `berth snapshot`. `add` and `reset` then clone from that copy instead of main, so main never pauses for them; a worktree made or reset from it is as old as the snapshot, and `berth status` says how old. Run it in the main worktree; anywhere else it exits `1` and changes nothing.

The copy is taken the way a [`dump`](../stacks/local-databases/) is: from one consistent read, with main's InnoDB tables taking writes throughout. A table of another engine has its writes held while it is read, and berth names it first. A database that can't be taken fails the whole run and exits `4`; the previous snapshot is kept whole. So does a database main no longer has when the current snapshot holds data for it: create it again, or remove it from the config. An interrupted run, even one killed with `kill -9`, leaves the previous snapshot as it was and the next `berth snapshot` cleans up after it. Files are readable only by you, and never hold a login.

`-n`, `--dry-run` lists what it would take, with each size and any table that would pause, and takes nothing. With `--json`: `{"dry_run", "databases": [{"key", "source", "bytes", "file_bytes", "seconds", "paused", "pause_seconds", "source_missing", "notes"}]}`.

## prune

`berth prune` deletes every orphaned berth, those whose worktree git no longer has, as `remove` would, without `pre-remove`. `-n`, `--dry-run` lists each one and what would be deleted.

`berth prune --idle <duration>` also deletes every berth unused for that long, given in days, hours or minutes: `7d`, `12h`, `30m`. Unused means its last use, as `list` shows it, is older than the duration and none of its containers runs; a berth whose worktree is locked is never idle. Unlike `list`, it checks every worktree for changes however long git takes. Each idle berth is removed as `berth remove` would: `pre-remove` runs first, in its worktree, and may run `berth status` or `berth env`; if it fails, that berth is kept, the others go on, and `prune` exits `1`. At each berth's turn, before its `pre-remove`, berth judges it again and keeps a berth that was used, started a container or had its worktree locked since the list was made, saying so on stderr and running no hook for it; a berth removed meanwhile is skipped. After the hook, what `pre-remove` did in the worktree doesn't count as use, but a container of the berth running, or a berth command run on it, still keeps it. The worktree and your work in it stay; only what berth made for it goes, and `berth add` there later makes a fresh berth from main. If Docker can't be reached, no idle berth is deleted: each is reported as unknown and `prune` exits `1`. An invalid duration exits `1` and deletes nothing. `--dry-run` lists orphans and idle berths with what would be deleted, and `prune --idle` then deletes exactly that list.

With `--json`: `{"dry_run", "berths": [...]}`, each entry shaped like `remove`'s plus `reason` (`orphan` or `idle`) and `last_used`, an RFC 3339 time.

## init

`berth init [path]` writes `berth.toml` at the top of your main checkout from what berth can detect there, reading the compose project main uses:

- each published port as `assign` under `[compose.ports]`, or `ephemeral` when main lets Docker pick its host port;
- each named volume under `[compose.volumes]`: `share` for a package-manager cache such as `composer_cache` or a volume mounted at `~/.npm`, `fresh` for one berth can't copy, `clone` for the rest;
- `vendor/` or `node_modules/` under `[files] clone` when they sit next to a tracked `composer.lock`, `package-lock.json` or `yarn.lock` and git ignores them.

What needs your intent is written as commented-out lines with a reason above each, so you only delete the `# `:

- `.env` keys whose value in main holds `localhost` or a port main publishes, with the template that would replace it, such as `# APP_URL = "http://{host}:{port.web}"`, and a matching `[berth] url`;
- services under compose profiles, for `[compose] exclude`;
- compose files main doesn't read, for `[compose] files`;
- small gitignored files at the top of main, such as `phpunit.xml`, for `[files] clone`;
- an install command for `post-add` when the lockfile is `pnpm-lock.yaml`, `bun.lock`, `uv.lock` or `poetry.lock`.

Keys your compose files read, such as `WEB_PORT` in `${WEB_PORT}:80`, are never suggested: berth leaves them alone. A value with a password in its URL gets the reason line only, so the password never lands in the file.

Presets add what berth can't learn from compose: `laravel` and `vite` ship with berth, each naming the files that show it applies and the config it suggests. `init` applies every preset whose files it finds and writes its suggestions under a comment naming it. A suggestion a file in the repository confirms is written active, and the rest are commented out with their reason, as above: `laravel`'s `DB_DATABASE_PREFIX` is active when a file in `config/` reads it and main's `.env` sets it. `laravel` covers `APP_URL`, `SESSION_DOMAIN` (left host-only), a `serve` port on `SERVER_PORT`, `[state.mysql]` from `DB_*`, and `REDIS_PREFIX` and `CACHE_PREFIX`; `vite` adds a `vite` port on `VITE_PORT`. A key detection also suggests is written once, with detection's template where it has one. A preset only adds config you could have written yourself; it changes nothing but `init`'s output. `--preset <name>` applies that one preset alone, whether or not its files are there, and an unknown name exits `1`; `--no-presets` applies none.

`--private` writes the personal `berth.local.toml` instead and keeps it out of `git status`. `-n`, `--dry-run` prints the file and writes nothing. When it has written the file, `init` prints its path and what `berth doctor` finds in main's config.

`init` never overwrites a config. With `berth.toml`, or for `--private` either `berth.local.toml` or `.git/berth.toml`, already there, it exits `1` with error code `config_exists`, naming the file. It runs only in the main checkout: in a linked worktree it exits `1`, and outside a git repository `3`. With `--json`: `{"path", "written", "config", "findings"}`, where `config` is the file's text and `findings` is what doctor found in the config, shaped as in `berth doctor --json`; a dry run leaves out `findings`.

## doctor

`berth doctor [--probe] [path]` checks a berth and reports each problem with where it is: invalid config, a docker compose older than 2.24.4, which misreads `compose.berth.yml`, a compose project compose rejects, a hardcoded `container_name`, an assigned port another process holds, exported shell variables that override managed keys, and copied text files that contain main's path. What it can't check is reported as not verified.

It also warns, without failing, about build-time env keys (`VITE_`, `NEXT_PUBLIC_` and the like) the worktree sets apart from main, since build output copied from main still carries main's values, about small gitignored files at the top of main that the worktree lacks, such as `phpunit.xml`, about a `berth.local.toml` in the worktree, which berth never reads, about dependency directories under `[files] clone` that the filesystem will copy in full rather than copy-on-write, and about a git hook `berth hook install` wrote that is now gone, replaced, edited or no longer run. It never warns about the hook in a repository where you never installed it.

In your main checkout, `berth doctor` checks what a worktree would be made from before any exists: the config, the compose files a worktree would read, the docker compose version that will read them, `container_name` in them, and `[compose]` entries naming a service or volume the project lacks. It warns about a service that builds with the whole repository as its context and no `.dockerignore`, and, when `[files] clone` lists dependency directories, about a filesystem that will copy them in full rather than copy-on-write.

`--probe` also requests the running app, at the worktree's URL and at each URL under `[doctor] probe`, and reports a cookie whose `Domain=` reaches more than the worktree's host, such as `.myapp.localhost`, and a redirect to main's host or one of main's ports, each naming the URL that sent it. Requests go to `127.0.0.1` with the worktree's host name, so they need no DNS, and carry no cookies or credentials. A URL on one of main's ports, or on a port berth didn't assign the worktree, is never requested and is reported as not verified; that includes the worktree's own URL, so give `[berth] url` one of its ports, such as `{port.web}`. Redirects are followed, up to ten, while they stay on the worktree's host, or a name under it, and its ports; a redirect anywhere else is not followed: one to main is a problem, one to the worktree's host on a port it wasn't assigned is not verified, and one to another site is not reported. When nothing answers, or the app takes longer than ten seconds, the URL is reported as not verified: berth never starts the app, so start it first. Without `--probe`, doctor makes no request. In your main checkout there is no worktree to probe, and `--probe` says the checks are skipped.

Without `--probe`, doctor also reports each wide cookie [`berth proxy`](#proxy) saw the worktree's app set, as the same `cookie_domain` problem, naming the cookie, its domain and the page that set it, never its value. A finding stays until the app sets that cookie again through the proxy within the worktree's host, or the worktree is removed.

Exits `0` when it finds no problem and `1` when it does; warnings and not verified findings alone exit `0`. With `--json`: `{"findings": [{"check", "level", "location", "message", "fix"}]}`, where `level` is `problem`, `warning` or `not_verified`, plus `error` when there are problems. `fix` is there only when berth knows the change that resolves the finding: `{"config": "<TOML>"}` to merge into your berth config, such as `[env.set]` setting the `.env` key that holds a shared cookie domain to `"{host}"`, offered only when exactly one key holds it.

## config path

`berth config path [path]` prints the path of the personal config: `berth.local.toml` or `.git/berth.toml`, whichever exists, else where `berth config edit` would create it. `--repo` prints the committed `berth.toml` of the worktree instead, and `--user` the user config. With `--json`: `{"layer", "path", "exists"}`, where `layer` is `personal`, `repo` or `user`.

## config edit

`berth config edit [path]` opens the personal config in `$VISUAL`, or `$EDITOR` when `$VISUAL` is unset. When neither file exists it creates `berth.local.toml` first, with a short commented header, and keeps it out of `git status`. The editor command runs through the shell, so one that needs a flag to wait, such as `code --wait`, works as it does for git.

When the editor exits, berth checks the whole config and reports each problem as `file: key: problem`, exiting `5` if there is one. With no editor set it exits `1`, naming `$VISUAL`, `$EDITOR` and the file to edit by hand. With `--json`: `{"path", "created"}`.

## hook install

`berth hook install [path]` is optional. It adds a `post-checkout` hook to the folder git runs hooks from, so every `git worktree add` runs `berth add`, for repos where every worktree runs the app. That is `.git/hooks`, or wherever `core.hooksPath` points when that is inside `.git` or outside the repository. It never replaces a hook berth didn't write. When `core.hooksPath` points at a folder inside your checkout, such as `.husky` or `.githooks`, it writes nothing, exits `1` and prints the line to add to that folder's `post-checkout` yourself:

```sh
if command -v berth >/dev/null 2>&1; then berth hook run "$@" || true; fi
```

On Windows with no `sh`, it writes nothing, exits `1` and names `sh.exe`.

`-n`, `--dry-run` says what it would do. With `--json`: `{"hook", "changed", "dry_run"}`.

## hook uninstall

`berth hook uninstall [path]` removes the `post-checkout` hook if berth wrote it and it hasn't been edited since, from the same folder `install` uses. It refuses any other hook, and does nothing when there is none. When `core.hooksPath` points inside your checkout it exits `1` and leaves the folder alone: delete the `berth hook run` line yourself. `-n`, `--dry-run` and `--json` work as for `install`.

## hook run

`berth hook run <post-checkout arguments>` does what berth's own hook does, for a `post-checkout` hook berth didn't write: it runs `berth add` only in a worktree `git worktree add` just created, and does nothing on any other checkout. Given those arguments it always exits `0`, so it never fails git; if `add` fails it says how to retry. Call it as `berth hook run "$@"`; with no arguments at all it exits `1`.

## docs agents

`berth docs agents` prints the agent guides built into the binary, as markdown: working in a worktree, then setting berth up in a repository. They are the guides in berth's `docs/agents/` as they were when the binary was built, so they match the installed version, and printing them needs no network or repository. With `--json`: `{"guides": [{"name", "markdown"}]}`.

## proxy

`berth proxy` serves every worktree on the machine at its own host with no port, such as `https://feat-x.myapp.localhost` or `http://feat-x.myapp.localhost`, until you stop it with Ctrl-C. It runs in the foreground; berth never starts it, and it starts nothing itself. Run it from any directory.

A request for a worktree's host, or any name under it such as `app.feat-x.myapp.localhost`, goes to the worktree's port that [`[proxy] port`](/docs/reference/configuration/#proxy-and-proxyroutes) names, and a name listed under `[proxy.routes]` to the port it names, always with the `Host` header the browser sent. The proxy sets `X-Forwarded-Proto` (`https` over HTTPS), `X-Forwarded-Host` and `X-Forwarded-For`, and first drops every `X-Forwarded-` header, `Forwarded`, `X-Real-IP` and `X-Original-Host` the client sent, so the app never sees an origin a client claimed. WebSocket upgrades pass through, over HTTP and HTTPS. Worktrees added and removed while it runs are served or dropped within a second, without a restart, and it says so on stderr.

A host no worktree has gets a 404 page listing every worktree's URL. A worktree whose app isn't running gets a 502 naming the worktree and its port, and the proxy keeps serving the others. A worktree the proxy has no port for gets a 502 saying why: its config sets no `[proxy] port`, names a port berth didn't assign it (run `berth add` again), or the port is one main uses, since the proxy never sends a worktree's requests to main.

**HTTPS.** The first time it needs one, berth makes a certificate authority in its state directory, with a key only you can read, and prints where. The proxy signs a certificate from it for each name under `.localhost` a browser asks for; a name outside `.localhost` gets none, and the authority itself can vouch only for TLS servers with names under `.localhost`: never for another site, an IP address, an email address, signed code or a client, so trusting it can't be used to impersonate anything else. Until you trust it, browsers show a warning before each worktree's HTTPS page. If the key's file ever gives its group or others any permission at all, or the authority lacks any of those limits, as one made by an earlier berth does, berth refuses to use it and says how to replace it: `berth proxy untrust`, delete the directory it names, and `berth proxy trust` again.

**Cookies.** The proxy reports, on stderr and to [`berth doctor`](#doctor), each cookie a worktree's app sets with a `Domain=` wider than the worktree's host, such as `.myapp.localhost` or `localhost`, naming the cookie and the domain, never its value. Berth reads the header as a browser does, so a cookie Go's own parser would reject, such as `cart[items]=3`, counts too. A cookie whose `Domain=` is the worktree's host or a name under it passes untouched, byte for byte. With [`[proxy] cookies = "rewrite"`](/docs/reference/configuration/#proxy-and-proxyroutes) the proxy also replaces a wider domain with the worktree's host, changing nothing else in the header, so the cookie still reaches the worktree's own subdomains and no other worktree. It never renames a cookie or removes its domain.

It listens on the loopback addresses `127.0.0.1` and `::1` only, on port 80 for HTTP and 443 for HTTPS unless the user config sets `[proxy] http` and `https`, and it answers nothing that reaches it from another address, so nothing on your network can reach it. When a port is taken, on any address including `0.0.0.0`, it exits `1` naming the port and how to find what holds it. Where the system keeps the port for administrators, it exits `1` with the command that allows it, and changes nothing itself: on Linux, `sysctl` for `net.ipv4.ip_unprivileged_port_start` or `setcap cap_net_bind_service`; on macOS, running it with `sudo` (see [Platforms](/docs/reference/platforms/#the-proxy-on-ports-80-and-443)). Run with `sudo`, it becomes the user who ran `sudo` as soon as its ports are open, before it reads your worktrees or writes anything, and it can't become root again; run as root any other way, or with a state directory that user doesn't own, it exits `1` and serves nothing. Either way you can set `[proxy] http` and `https` in the user config to ports above 1023, and the URLs then carry them. With `--json` it prints `{"http": [...], "https": [...], "authority"}`, the addresses it listens on and the authority's certificate, once it is serving.

## proxy trust

`berth proxy trust` adds the proxy's certificate authority to the system's trust store, making the authority first if there is none, so browsers that read that store trust the proxy's HTTPS. It goes through the system's own tool and prompt: the login keychain on macOS, trusted for SSL only, which asks you to allow the change; the CA certificate directory on Linux, through `sudo`, which asks for your password, then `update-ca-certificates` or your distribution's equivalent; your user's Trusted Root Certification Authorities store on Windows, which asks you to confirm. Berth never trusts the authority on its own; only this command does. Run it as yourself: as root it exits `1`, since it uses `sudo` itself where the system needs it. On Linux the certificate reaches `sudo install` on its standard input, and berth checks what landed before the system's bundle is rebuilt, removing it unrebuilt if it isn't berth's certificate.

Firefox keeps its own store, so `trust` prints how to import the authority there, with the path of its certificate. On Linux, Chrome and Chromium read their own store too, and `trust` prints the `certutil` line that adds it. Berth records what it is about to add before it adds it, then checks the store holds it. If a step fails, such as a cancelled prompt, running `trust` again finishes it, and `untrust` removes whatever was added. Running `trust` once it has finished does nothing and says the authority is already trusted. With `--json`: `{"authority", "store", "added", "next"}`.

## proxy untrust

`berth proxy untrust` removes from the system's trust store the certificate `trust` added, found by its fingerprint, never by its name, and nothing else. When `trust` added nothing, it says so and changes nothing. When the store no longer holds it, because it was removed some other way, it says so and forgets it. On Linux it deletes only a file in the CA directory, named as berth names its file and holding that certificate, or a file berth itself was writing there when a `trust` failed; a file at berth's path that berth didn't write is left alone, and `untrust` names it, keeps its record and exits `1`. Run it as yourself, as for `trust`: as root it exits `1`. It doesn't reach Firefox's own store, or Chrome's on Linux: remove the authority there yourself. With `--json`: `{"store", "removed", "not_found"}`.

## Exit codes

| Code | Meaning | Error code with `--json` |
| --- | --- | --- |
| `0` | Success | |
| `1` | Any other error | `error`, `usage`, `unknown_command`, `unknown_flag`, `main_worktree`, `no_berth`, `config_exists`, `problems_found` |
| `2` | No free port in the pool | `no_free_ports` |
| `3` | The path doesn't exist, or isn't inside a git worktree | `worktree_not_found` |
| `4` | A copy failed | `state_clone_failed` |
| `5` | The config is invalid | `config_invalid` |

`2` comes from `berth add`, when every port in the pool is assigned, published by a main checkout or in use. The message says to widen `[pool] ports` in your user config.

`4` comes from `berth add --wait` and `berth status --wait`, when a volume, database or SQLite copy failed, and from `berth snapshot`, when a database couldn't be taken. Without `--wait`, `add` returns `0` while copies run in the background, so a failure shows later, in `berth status`.

`5` comes from any command that reads your config: `add`, `status`, `env`, `exec`, `remove`, `reset`, `snapshot` and `config edit`. The message names the file and the key. `berth doctor` is the exception: it reports an invalid config as a finding and exits `1`, like any other problem it finds.

`berth exec` exits with its command's code. `berth hook run` exits `0` whatever `add` did.

Scripts should branch on the exit code, or with `--json` on the error's `code`, never on the message. See [JSON output](../json-output/#errors) for the error object.

## Global flags: `--json`, `--dry-run`

| Flag | Where | What it does |
| --- | --- | --- |
| `--json` | Any command | Prints one JSON object on stdout in place of the human output |
| `-h`, `--help` | Any command, or `berth` alone | Prints usage and exits `0` |
| `--version` | `berth` alone | Prints the version |
| `-n`, `--dry-run` | `remove`, `prune`, `reset`, `snapshot`, `init`, `hook install`, `hook uninstall` | Says what would change and changes nothing |

`--json` works anywhere on the command line, before or after the command: `berth --json status` and `berth status --json` are the same. Errors come as JSON too, on stdout. Progress lines beginning `berth:` still go to stderr. See [JSON output](../json-output/) for each command's shape.

`-n` is the short form of `--dry-run`, as in git, so `-n` never means "name". `berth add` has no dry run.

Anything after a `--` separator is left alone, so `berth exec -- mytool --json` passes `--json` and `--help` to `mytool`, not to berth.

`--version` and a bare `--help` are the only flags berth takes before the command, besides `--json`. Any other flag there, or a flag a command doesn't know, exits `1` with error code `unknown_flag`. Running `berth` with no command prints the help on stderr and exits `1`.

Most commands take one optional worktree path, as git does, and default to the worktree containing the current directory. `list`, `prune`, `snapshot` and `docs agents` take none.

Berth never prompts and never reads stdin itself: `exec` hands it to the command it runs, and `config edit` to your editor.

`-h` or `--help` after any command prints its usage and exits `0`; `berth --help` lists every command.
