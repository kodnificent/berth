---
title: Troubleshooting
description: Fixing what berth doctor reports.
sidebar:
  order: 7
---

## Start with `berth doctor`

When a worktree misbehaves, run `berth doctor` in it. It checks what berth wrote against what is really there, and prints one line per finding: the check, where it applies, and what to do.

```text
$ berth doctor
shadowing                  shell: APP_PORT  APP_PORT is exported as "9999" in this shell, which overrides the berth's "20001" for compose and most frameworks; unset it here
ignored_file (warning)     phpunit.xml      main has the gitignored file phpunit.xml, which this berth lacks and [files] clone doesn't list; if the berth needs it, add it to [files] clone and run berth add
```

| Level | Meaning | Exit code |
| --- | --- | --- |
| No label | A problem. Fix it | `1` |
| `(warning)` | Worth a look, but may be what you intended | `0` |
| `(not verified)` | Berth couldn't check, and says why. It is never reported as a pass | `0` |

With nothing to report it prints `No problems found.` and exits `0`.

- **In a worktree** it checks exported variables, the compose project, assigned ports, database names, copied files, build-time env keys, missing gitignored files and copy-on-write.
- **In your main checkout** it checks the config and the compose files a worktree would use, so you can check a config before the first worktree exists. An invalid config is listed finding by finding, one per bad key.
- **With `--probe`**, in a worktree whose app is running, it also requests the app and checks its cookies and redirects. Berth never starts the app: with nothing listening, the URL is reported as not verified.
- **With `--json`** it prints `findings`, each with `check`, `level` (`problem`, `warning` or `not_verified`), `location` and `message`. A finding berth knows the fix for carries `fix.config`, TOML to merge into your config as it is.

## Values still pointing at main

Berth writes only the keys it knows are per worktree: the compose project, the ports it assigned, database names, and the keys you mapped under `[env]`. Any other key in the worktree's `.env` is a copy of main's. If main's `.env` holds main's own address under a key berth doesn't know, the worktree keeps it:

```dotenv
SANCTUM_STATEFUL_DOMAINS=localhost:8000
FRONTEND_URL=http://localhost:8000
```

The symptom is a worktree that half works: pages load on the worktree's URL, then a login, an API call or an asset goes to main. Map each such key so the worktree gets its own value:

```toml
[env]
url = ["FRONTEND_URL"]                          # receives the worktree's URL

[env.set]
SANCTUM_STATEFUL_DOMAINS = "{host}:{port.web}"  # or build the value from templates
```

Run `berth add` again to rewrite the managed block. `berth status` lists every key berth manages as an `env` row, so a key missing from that list still holds main's value.

To find the keys, run `berth init --dry-run` in your main checkout: it suggests every key in main's `.env` whose value contains main's host or a port main publishes, with the template that would replace it. With the app running, `berth doctor --probe` catches what is left as [redirects to main](#redirects-to-main).

`berth doctor` itself reports one case without the app running, a key that still names one of main's databases when the worktree has its own:

```text
env  .env DB_DATABASE  DB_DATABASE=shop still names main's database shop, so the app here may use main's data; the berth's own is shop__fix_checkout
```

Run `berth add` again, which picks up a key main's `.env` gained since the berth was made. If the finding stays, the worktree's own `.env` sets the key outside the managed block: change that line.

## Shared cookie domains

Browsers send a cookie set with `Domain=.myapp.localhost` to every host under `myapp.localhost`, so worktrees share sessions and CSRF tokens again. With the app running in the worktree, `berth doctor --probe` requests it and reports each cookie whose domain reaches more than the worktree's host, naming the cookie and the page that set it. A cookie with no `Domain=`, or one set to the worktree's host or a name under it, such as `app.feat-x.myapp.localhost`, stays in the worktree and is not reported.

Apps usually read the cookie domain from `.env`. Set that key to the worktree's host with `[env.set]`, for example `SESSION_DOMAIN = "{host}"`, and run `berth add` again; when exactly one key in the worktree's `.env` holds the shared domain, `berth doctor --probe --json` gives that change as the finding's `fix`. When several do, often `localhost` in `SESSION_DOMAIN` and `DB_HOST` alike, the finding names them and offers no fix: change only the one your app reads as its cookie domain. Or leave the domain unset so the cookie is host-only.

## Redirects to main

An app that builds absolute URLs from a value still pointing at main, such as `APP_URL=http://localhost:8000`, redirects a browser from the worktree to main, often right after login. `berth doctor --probe` follows the app's redirects within the worktree and reports one that leads to main's host or one of main's ports, naming the URL that sent it. It never requests main. Map the key that holds main's URL under `[env] url`, or template it under `[env.set]`, so the worktree gets its own, and run `berth add` again. List pages that redirect, such as a login page, under `[doctor] probe` so the probe reaches them. The probe only requests the worktree's own ports: with `[berth] url` on main's port, or on a port berth didn't assign, such as the default `http://{host}`, it reports the URL as not requested. Use `url = "http://{host}:{port.web}"`.

## Hardcoded container names and ports

A service with `container_name:` has one name for main and every worktree. Berth resets it in each worktree's compose project, but a script that runs `docker exec mysql` still reaches main's container. Drop `container_name` and use `docker compose exec mysql` instead.

`berth doctor` also reports a port berth assigned to the worktree that another process now holds, naming the process. `docker compose up` in the worktree can't publish that port until it is free.

## Exported shell variables that override `.env`

Compose and most frameworks prefer a variable exported in your shell over the value in `.env`. If `COMPOSE_PROJECT_NAME` or any other key berth manages is exported with another value, the worktree quietly uses that instead. `berth status` and `berth doctor` both name such variables; unset them in that shell. Values exported by `eval "$(berth env)"` match the worktree's and are not reported.

## Copied files that contain main's path

A copied file that stores main's absolute path, such as a compiled config cache, sends the worktree's reads and writes back into main. `berth doctor` searches the text files berth copied for that path, including the `/var` form of a `/private/var` path on macOS, Windows backslashes and JSON's `\/`, and names each file and line. Berth never rewrites file contents: leave the file out of `[files] clone`, or regenerate it in your `post-add` hook.

## Build output with main's values

Bundlers such as Vite and Next.js write env keys like `VITE_API_URL` or `NEXT_PUBLIC_HOST` into the files they build. If the worktree sets such a key to a value that differs from main's, build output copied from main, or from a volume shared with it, still points at main. `berth doctor` warns about it. Berth never rewrites build output: build again in the worktree, in your `post-add` hook or by hand, or run the dev server there instead.

## Gitignored files the worktree lacks

Local config is often gitignored: `phpunit.xml`, `auth.json`. A worktree without it quietly falls back to defaults, such as tests reaching the wrong database. `berth doctor` warns about small gitignored files at the top of main that the worktree lacks. If the worktree needs one, add it to `[files] clone` and run `berth add` again; that also silences the warning.

## The git hook no longer runs

After `berth hook install`, `berth doctor` warns when the hook it wrote is gone, has been replaced or edited, or no longer runs because `core.hooksPath` now points somewhere else. `git worktree add` may then stop creating a berth. Run `berth hook install` again to put it back; it rewrites a hook berth wrote, dropping any edits, and refuses one it didn't. If `core.hooksPath` now points inside your checkout, add the line the warning prints to that folder's `post-checkout`. Repos where you never installed the hook get no warning.

## Main's database reads unhealthy during a copy

To copy a volume consistently, berth pauses main's containers that use it, and says so first. Docker reports a paused container with a health check as unhealthy, from the pause until its next health check, so `docker compose ps` shows `unhealthy` for a moment and a service that waits on `service_healthy` would wait. Nothing is wrong with the database. The pause lasts only for the copy, and `berth status` shows how long it was.

## Slow copies on your filesystem

`berth add` names the path it took for each directory under `[files] clone`. `cow` shares main's blocks and is done in a moment; `copy` reads and writes every byte:

```text
berth: cloned node_modules (copy) in 12.3s
```

`berth doctor` warns about it ahead of time, when `[files] clone` lists a dependency directory and a trial copy from main into the worktree shares nothing:

```text
copy_on_write (warning)  /Users/you/shop/.worktrees/fix-checkout  ext4: dependency clones (node_modules) will be full copies, as a copy-on-write clone from main into this berth is not possible
```

- **Main and the worktree are on different filesystems.** The warning names both. Keep worktrees on the same disk as main, for example in `.worktrees/` inside the repository.
- **The filesystem has no copy-on-write,** such as ext4 or NTFS. Take the directory out of `[files] clone` and install in your `post-add` hook instead; the package manager's own cache makes that mostly cache hits. On Windows, a Dev Drive clones instantly, and the warning says so.
- **It is a Docker volume.** Volumes are always full copies, on every platform. Set the ones you don't need data in to `fresh`. See [copying data](/docs/guides/copying-data/#choosing-per-database-clone-share-fresh).

## A command says there is no berth, or that you are in main

```text
berth: worktree /Users/you/shop/.worktrees/fix-checkout has no berth; run berth add
next: run berth add before starting the app or its tests here
```

A worktree has no berth until `berth add` runs in it. Exit code `1`.

```text
berth: /Users/you/shop is the main worktree: it is the source environment, not a berth; run berth status in a linked worktree
next: main is the source berths clone from; berths live in linked worktrees, so make one with git worktree add and run berth there
```

Main is what worktrees are copied from, so it never has a berth of its own. Make a worktree with `git worktree add` and run berth there, or pass the worktree's path: `berth status .worktrees/fix-checkout`.

```text
berth: /Users/you/Downloads is not inside a git worktree
```

Exit code `3`. Run berth inside a repository, or give it a worktree's path.

## The config is refused

Berth stops with exit code `5` rather than guess at a config it doesn't understand. The message names the file and the key:

```text
berth: /Users/you/shop/berth.toml: compose.volumes.mysql: unknown volume role "copy"; want one of clone, share, fresh
```

- **`unknown key`.** A typo, or a key from a newer berth than the one installed.
- **`{port.api} names no defined port`.** A template can only name a port with a stable value: list it under `[compose.ports]` as `"assign"`, or under `[ports]`.
- **`berth.host: must contain {name}`.** Without `{name}` every worktree would share one host and its cookies.
- **`is a personal config too; keep one and delete the other`.** Both `berth.local.toml` and `.git/berth.toml` exist. See [using berth privately](/docs/guides/private-setup/).
- **`is written by berth`.** `COMPOSE_PROJECT_NAME`, `COMPOSE_FILE` and the prefix keys berth writes for your databases and Redis can't also be set under `[env]` or `[ports]`.

`berth doctor` lists each problem in the config as a finding of its own, in your main checkout or in a worktree.

## No free port

```text
berth: no free port for web in the pool 20000-29999: every port is assigned to a berth, published by a main worktree or in use; widen [pool] ports in the user config
```

Exit code `2`. Every berth on the machine draws from one pool, and a berth keeps its ports until it is removed. `berth list --json` shows who holds them. Free some with `berth remove` in worktrees you are done with, or `berth prune --idle 7d` for every worktree unused for a week, or widen the pool in `~/.config/berth/config.toml`:

```toml
[pool]
ports = "20000-39999"
```

## `.env` is tracked by git

```text
berth: /Users/you/shop/.worktrees/fix-checkout/.env is tracked by git, and berth only writes gitignored files; untrack it (git rm --cached .env) and add it to .gitignore
```

Berth writes its managed block into the worktree's `.env`, and it never changes a committed file. `berth add` stops before it creates anything. Untrack `.env` as the message says, and commit a `.env.example` instead if the team needs a template.

## A copy failed

`berth status` shows a failed copy with its reason, and `berth add --wait` and `berth status --wait` exit `4`:

```text
berth: cloning failed for volume mysql: the clone process stopped before it finished; run berth add to clone again
```

| Reason | What to do |
| --- | --- |
| `the clone process stopped before it finished` | The copy was cut short by a crash, sleep or reboot. Run `berth add` again |
| `Docker is not reachable` | Start Docker and run `berth add` again |
| `the berth's volume ... already exists and berth didn't create it, so berth won't overwrite it` | A volume with the worktree's name was there before berth. Remove it with the `docker volume rm` command in the message, then `berth add` |
| `uses the ... driver; berth only copies local volumes` | Set the volume to `fresh` or `share` under `[compose.volumes]` |

`berth add` again always retries: it keeps the berth, leaves finished copies alone and starts the failed ones afresh. For a database on a server on your machine, the reasons are in [local databases](/docs/stacks/local-databases/).

When a copy fails, `post-add` reads `skipped`, since it would have run on incomplete data. It runs after the retry succeeds. A `post-add` that itself failed shows its exit code and the end of its output: see [hooks](/docs/guides/hooks/#when-a-hook-fails).

## `berth remove` or `berth reset` refuses

Both stop before deleting anything when they can't finish the job, and say what to do:

```text
berth: Docker is not reachable, so berth can't delete berth 9f2c4e1ab07d3355's containers, volumes and images; start Docker and run this again (...)
```

```text
berth: berth 9f2c4e1ab07d3355's containers shop-fix-checkout-mysql-1 are running, so reset discarded nothing; stop them with docker compose stop and run berth reset again
```

```text
berth: pre-remove failed (exit status 1), so nothing was removed; fix [hooks] pre-remove or take it out, and run berth remove again
```

A remove that got partway keeps the berth's record and lists what it couldn't delete. Fix that and run the same command again; it picks up where it stopped.

## `git worktree add` printed a berth error

```text
berth: berth add failed in the post-checkout hook; the worktree is fine, run berth add in it to retry
```

The git hook never fails git: the worktree was created, and only the berth is missing. The line above it says why. Fix that and run `berth add` in the new worktree.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success |
| `1` | An error, or `berth doctor` found a problem |
| `2` | No free port in the pool |
| `3` | The path is not a git worktree |
| `4` | A copy failed |
| `5` | The config is invalid |
