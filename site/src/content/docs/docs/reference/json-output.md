---
title: JSON output
description: What every command prints with --json.
sidebar:
  order: 4
---

Add `--json` to any berth command and it prints one JSON object on stdout, for agents and scripts to read in place of the human output. The flag works anywhere on the command line, before or after the command, up to a `--` separator: `berth --json status` and `berth status --json` are the same.

The object is indented and never styled, whatever the terminal. Progress lines beginning `berth:` still go to stderr, so read stdout alone. Times are RFC 3339, sizes are bytes, and paths are absolute unless a field says otherwise.

## `status`

`berth status --json` is the first thing an agent should read in a worktree: where the app runs, whether its data is ready, and what to do next.

```json
{
  "id": "9f2c4e1ab07d3355",
  "worktree": "/code/shop/.worktrees/fix-checkout",
  "repository": "/code/shop",
  "source": "/code/shop",
  "created_at": "2026-11-10T15:45:19Z",
  "name": "fix-checkout",
  "host": "fix-checkout.localhost",
  "url": "http://fix-checkout.localhost:20000",
  "compose_project": "shop-fix-checkout",
  "ports": [
    { "name": "web:80", "service": "web", "container_port": 80, "host_port": 20000, "policy": "assign" },
    { "name": "vite", "host_port": 20001, "policy": "assign" }
  ],
  "env": {
    "APP_URL": "http://fix-checkout.localhost:20000",
    "COMPOSE_FILE": "compose.yml:compose.berth.yml",
    "COMPOSE_PROJECT_NAME": "shop-fix-checkout",
    "DB_DATABASE": "shop__fix_checkout",
    "VITE_PORT": "20001"
  },
  "files": [".env", "compose.berth.yml", "vendor"],
  "state": [
    {
      "name": "shop",
      "kind": "database",
      "source": "shop",
      "target": "shop__fix_checkout",
      "strategy": "dump",
      "status": "cloning",
      "pid": 48213,
      "progress": 0.62,
      "copied_bytes": 409200000,
      "total_bytes": 660000000
    }
  ],
  "post_add": {
    "command": "composer install",
    "status": "pending"
  },
  "shadowed": [],
  "builds": [
    { "service": "web", "shared": true, "read_bytes": 48211 }
  ],
  "next": [
    "database shop is still cloning; run berth status --wait before starting the app"
  ]
}
```

| Field | Type | What it holds |
| --- | --- | --- |
| `id` | string | The berth's id |
| `worktree` | string | The worktree's path |
| `repository` | string | Your main checkout's path |
| `source` | string | Where the berth was copied from: the main checkout, so the same path as `repository` |
| `created_at` | time | When the berth was made |
| `name` | string | The berth's name, built from the branch and unique on the machine |
| `host` | string | The worktree's hostname |
| `url` | string | The worktree's URL; left out when it can't be worked out yet (see `unresolved`) |
| `compose_project` | string | The compose project name the worktree uses |
| `ports` | array | Each port berth knows about (see below) |
| `env` | object | Every key in the managed `.env` block with its value. A database URL has its login and query string replaced by `<redacted>` |
| `unresolved` | object | Templated values berth can't fill in yet, keyed `url` or `env.<KEY>`, each with its template; left out when there are none |
| `files` | array of strings | Paths berth wrote or copied, relative to the worktree |
| `state` | array | Each volume, host database and SQLite file (see below); empty when there are none |
| `post_add` | object | Your `post-add` hook (see below); left out when the berth has none |
| `shadowed` | array | Managed keys your shell exports with another value, each as `{"key", "exported", "value"}`: what the shell has and what the berth set |
| `builds` | array | For each service the worktree builds: `service`, `shared` (`true` when it runs main's image), `reason` when it doesn't, and `read_bytes`, how much of the build context berth read to decide |
| `next` | array of strings | What to do next (see [`next` hints](#next-hints)) |
| `error` | object | Only with `--wait`, when a copy or `post-add` failed: `{"code", "message"}` as in [Errors](#errors) |

Each entry under `ports`:

| Field | Type | What it holds |
| --- | --- | --- |
| `name` | string | The name `{port.<name>}` uses: `<service>:<container port>` for a compose port, the `[ports]` key otherwise |
| `service` | string | The compose service; left out for a `[ports]` entry |
| `container_port` | number | The port inside the container; left out for a `[ports]` entry |
| `host_port` | number | The port on your machine. Left out for `none`, and for an `ephemeral` port until its container is running and Docker has picked one |
| `policy` | string | `assign`, `ephemeral` or `none` |

Each entry under `state`:

| Field | Type | What it holds |
| --- | --- | --- |
| `name` | string | The volume's key in the compose file, the database's key in your config, or the SQLite file's path in the worktree |
| `kind` | string | `volume`, `database` or `sqlite` |
| `source` | string | Main's volume, database or file |
| `target` | string | The worktree's own volume, database or file |
| `volume` | string | For a volume, the same as `target`; left out otherwise |
| `strategy` | string | How it was made, such as `copy`, `cow`, `dump`, `template` or `fresh` |
| `status` | string | `cloning`, `ready` or `failed`. A database berth has recorded but not yet made reads `creating` |
| `progress` | number | While `cloning`, how far the copy is, from 0 to 1; left out when the total isn't known |
| `copied_bytes`, `total_bytes` | number | While `cloning`, bytes copied so far and bytes to copy |
| `pid` | number | While `cloning`, the process doing the copy |
| `bytes` | number | Once made, its size |
| `seconds` | number | How long the copy took |
| `paused`, `pause_seconds` | array of strings, number | What of main's was paused for the copy, and for how long |
| `source_missing` | boolean | `true` when main has no such volume, database or file |
| `method` | string | For a Postgres `template` copy, which path the server took |
| `snapshot_taken_at`, `snapshot_age_seconds` | time, number | For a `snapshot` database, how old its data is |
| `notes` | array of strings | Anything worth knowing about the copy |
| `log` | string | For a database with a `command`, the path of the log holding its script's output |
| `error` | string | Why it failed |

Fields past `status` appear only when they have something to say.

`post_add` holds `command` and `status`: `pending`, `running`, `ok`, `failed` or `skipped`. Once the hook has started it also holds `started_at` and `log`, the path of its full output. Once it has ended it holds `finished_at`, `exit_code` and `output`, the last lines of what it printed. `error` says why it failed or was skipped.

`berth status --wait --json` waits for every copy and `post-add`, then prints the same object. When one failed it adds `error` and exits `4` for a copy or `1` for `post-add`. Nothing else is printed, so a waiting script reads one object either way.

### `next` hints

`next` is a list of plain sentences saying what to do now. It is always there, and empty when berth has nothing to suggest.

| When | Hint |
| --- | --- |
| A copy or `post-add` is still running | `volume mysql is still cloning; run berth status --wait before starting the app` |
| Everything is ready and the worktree has a URL | `use http://fix-checkout.localhost:20000 to reach this berth's app, not localhost or main's URL` |
| Everything is ready, with assigned ports and no URL | `use the ports listed here to reach this berth's services, not main's` |
| A copy or `post-add` failed | None: read `state` and `post_add` |

Inside a hook berth runs, where `BERTH_PATH` is set, no hint suggests `--wait`: the hint says the data is not complete yet.

## `list`

```json
{
  "berths": [
    {
      "id": "9f2c4e1ab07d3355",
      "worktree": "/code/shop/.worktrees/fix-checkout",
      "repository": "/code/shop",
      "source": "/code/shop",
      "created_at": "2026-11-10T15:45:19Z",
      "name": "fix-checkout",
      "orphaned": false,
      "locked": false,
      "disk_bytes": 1181116006,
      "host": "fix-checkout.localhost",
      "ports": [
        { "name": "web:80", "service": "web", "container_port": 80, "host_port": 20000, "policy": "assign" }
      ],
      "last_used": "2026-11-10T16:02:41Z",
      "in_use": false
    }
  ]
}
```

| Field | Type | What it holds |
| --- | --- | --- |
| `id`, `worktree`, `repository`, `source`, `created_at`, `name`, `host` | | As in `status` |
| `orphaned` | boolean | `true` when git no longer has the worktree |
| `locked` | boolean | `true` when the worktree is locked with `git worktree lock` |
| `disk_bytes` | number | The apparent size of what berth copied and wrote, as recorded when it was made |
| `ports` | array | The ports the berth holds, shaped as in `status` |
| `last_used` | time | When the berth was last used |
| `in_use` | boolean or null | `true` when one of its containers is running; `null` when Docker couldn't be reached |

With no berths, `berths` is an empty array.

## `add`

`berth add --json` prints the berth and whether this run made it. Read `berth status --json` for its ports, URL and data.

```json
{
  "id": "9f2c4e1ab07d3355",
  "worktree": "/code/shop/.worktrees/fix-checkout",
  "repository": "/code/shop",
  "source": "/code/shop",
  "created_at": "2026-11-10T15:45:19Z",
  "created": true
}
```

| Field | Type | What it holds |
| --- | --- | --- |
| `id`, `worktree`, `repository`, `source`, `created_at` | | As in `status` |
| `created` | boolean | `false` when the worktree already had its berth |

When `berth add --wait --json` finds a failed copy, it prints the [error object](#errors) with code `state_clone_failed` in place of this one, and exits `4`.

## `env`

```json
{
  "env": {
    "APP_URL": "http://fix-checkout.localhost:20000",
    "VITE_PORT": "20001"
  }
}
```

`env` holds every key in the managed `.env` block. Unlike the `export` lines `berth env` prints without `--json`, a database URL here has its login and query string replaced by `<redacted>`.

## `exec`

`berth exec --json -- <command>` prints nothing of its own when the command runs: stdout is the command's. When berth itself fails, for example in a worktree with no berth, it prints the [error object](#errors), which is how a caller tells berth's exit `1` from the command's.

## `remove`

```json
{
  "id": "9f2c4e1ab07d3355",
  "worktree": "/code/shop/.worktrees/fix-checkout",
  "dry_run": false,
  "removed": [
    { "kind": "container", "name": "shop-fix-checkout-web-1" },
    { "kind": "volume", "name": "shop-fix-checkout_mysql" },
    { "kind": "file", "name": "compose.berth.yml" },
    { "kind": "env_block", "name": ".env" }
  ]
}
```

| Field | Type | What it holds |
| --- | --- | --- |
| `id`, `worktree` | string | The berth and its worktree |
| `dry_run` | boolean | `true` when nothing was deleted |
| `removed` | array | Each thing deleted, or that would be: `kind` and `name`. Empty when there was nothing to delete |
| `error` | object | Only when something could not be deleted: the [error](#errors), beside what was removed. The command exits `1` and the berth keeps its record, so running it again finishes |

`kind` is `container`, `network`, `volume`, `image`, `database`, `redis_keys`, `sqlite`, `file` or `env_block`. A `redis_keys` entry is named by the worktree's key prefix, and `env_block` by the `.env` the managed block came out of.

## `prune`

```json
{
  "berths": [
    {
      "id": "4be1d07a9c2f6e80",
      "worktree": "/code/shop/.worktrees/feat-x",
      "dry_run": true,
      "removed": [
        { "kind": "volume", "name": "shop-feat-x_mysql" }
      ],
      "reason": "idle",
      "last_used": "2026-10-28T09:14:03Z"
    }
  ],
  "dry_run": true
}
```

Each entry under `berths` is shaped like `remove`'s output, plus `reason`, `orphan` or `idle`, and `last_used`. With nothing to prune, `berths` is an empty array. When a berth could not be pruned, or only partly, the object also carries `error`, as `remove`'s does, and the command exits `1`.

## `reset`

```json
{
  "id": "9f2c4e1ab07d3355",
  "worktree": "/code/shop/.worktrees/fix-checkout",
  "dry_run": false,
  "discarded": [
    { "kind": "volume", "name": "mysql", "volume": "shop-fix-checkout_mysql", "target": "shop-fix-checkout_mysql" },
    { "kind": "database", "name": "shop__fix_checkout", "target": "shop__fix_checkout" }
  ]
}
```

| Field | Type | What it holds |
| --- | --- | --- |
| `id`, `worktree` | string | The berth and its worktree |
| `dry_run` | boolean | `true` when nothing was discarded |
| `discarded` | array | Each thing discarded and made again, or that would be |

Each entry has a `kind`: `volume`, `database`, `sqlite` or `redis_keys`. For a volume, `name` is its key in the compose file and `volume` and `target` the worktree's volume. For a database, `name` and `target` are the database. For a SQLite file they are its path in the worktree, and for Redis keys the worktree's prefix.

## `snapshot`

```json
{
  "dry_run": false,
  "databases": [
    {
      "key": "shop",
      "source": "shop",
      "bytes": 660000000,
      "file_bytes": 91200000,
      "seconds": 28.4
    }
  ]
}
```

| Field | Type | What it holds |
| --- | --- | --- |
| `key` | string | The database's key in your config |
| `source` | string | Main's database |
| `bytes` | number | The size of its data |
| `file_bytes` | number | The size of the snapshot on disk; left out of a dry run |
| `seconds` | number | How long it took; left out of a dry run |
| `paused`, `pause_seconds` | array of strings, number | The tables whose writes were held, and for how long; in a dry run, `paused` is those that would be |
| `source_missing` | boolean | `true` when main has no such database yet |
| `notes` | array of strings | Anything worth knowing about the copy |

## `doctor`

```json
{
  "error": {
    "code": "problems_found",
    "message": "berth doctor found 1 problem(s)"
  },
  "findings": [
    {
      "check": "cookie_domain",
      "level": "problem",
      "location": "http://fix-checkout.shop.localhost:20000/login",
      "message": "cookie shop_session sets Domain=.shop.localhost, so a browser sends it to every berth under shop.localhost, not only this one; scope it to fix-checkout.shop.localhost or leave Domain out; the berth's .env sets SESSION_DOMAIN to it",
      "fix": {
        "config": "[env.set]\nSESSION_DOMAIN = \"{host}\"\n"
      }
    }
  ]
}
```

| Field | Type | What it holds |
| --- | --- | --- |
| `findings` | array | Everything doctor found; empty when it found nothing |
| `check` | string | Which check made the finding, such as `config`, `ports`, `shadowing`, `source_path` or `probe` |
| `level` | string | `problem`, `warning` or `not_verified` |
| `location` | string | Where it is: a file, a key, a URL |
| `message` | string | The finding, for a person |
| `fix` | object | Only when berth knows the change that resolves it: `{"config": "<TOML>"}` to merge into your berth config |
| `error` | object | Only when there is at least one `problem`: code `problems_found` and a count. Doctor then exits `1` |

Warnings and `not_verified` findings alone leave out `error` and exit `0`.

## `init`

```json
{
  "config": "[compose.ports]\n\"web:80\" = \"assign\"\n",
  "findings": [],
  "path": "/code/shop/berth.toml",
  "written": true
}
```

`config` is the whole text of the file. `findings` is what doctor found in main, shaped as in `doctor`. With `--dry-run`, `written` is `false` and `findings` is left out.

## `config`

`berth config path --json`:

```json
{
  "exists": false,
  "layer": "personal",
  "path": "/code/shop/berth.local.toml"
}
```

`layer` is `personal`, `repo` or `user`, and `exists` says whether the file is there yet.

`berth config edit --json` prints `{"path", "created"}` once the editor has exited and the config is valid; `created` is `true` when berth made the file. While it runs, the editor's own stdout is sent to stderr, so stdout holds the object alone.

## `hook`

`berth hook install --json` and `berth hook uninstall --json`:

```json
{
  "changed": true,
  "dry_run": false,
  "hook": "/code/shop/.git/hooks/post-checkout"
}
```

`hook` is the hook's path. `changed` says whether the hook was written or removed, or with `--dry-run` whether it would be.

## `docs agents`

`berth docs agents --json` prints `{"guides": [{"name", "markdown"}]}`: each agent guide built into the binary, with its text.

## Help and version

`berth --version --json` prints `{"version": "<version>"}`. `berth --help --json` prints `{"commands": [{"name", "summary"}]}`, and `berth <command> --help --json` prints `{"command", "summary", "usage"}`.

## Errors

With `--json`, a command that fails prints its error as one object on stdout, not as a message on stderr:

```json
{
  "error": {
    "code": "no_berth",
    "message": "worktree /Users/you/shop/.worktrees/fix-checkout has no berth; run berth add",
    "next": [
      "run berth add before starting the app or its tests here"
    ]
  }
}
```

`code` is stable and never renamed, and each code maps to one exit code. `message` is for people and may change. `next`, when present, lists what to do about the error, one hint per entry; berth leaves it out when it has nothing to suggest. In your main checkout, `berth status` fails with `main_worktree`, and its `next` says main is the source and berths live in linked worktrees.

| Code | Exit | When |
| --- | --- | --- |
| `error` | `1` | Any failure without a code of its own |
| `usage` | `1` | Wrong arguments, or a command run in the wrong place |
| `unknown_command` | `1` | No such command |
| `unknown_flag` | `1` | No such flag |
| `main_worktree` | `1` | The command needs a linked worktree and was run in your main checkout |
| `no_berth` | `1` | The worktree has no berth |
| `config_exists` | `1` | `berth init` found the config it would write already there |
| `problems_found` | `1` | `berth doctor` found a problem |
| `no_free_ports` | `2` | The port pool has no free port |
| `worktree_not_found` | `3` | The path doesn't exist or isn't inside a git worktree |
| `state_clone_failed` | `4` | A copy failed |
| `config_invalid` | `5` | The config is invalid |

Two commands put the error inside their usual object instead, so you get the detail and the failure together: `berth status --wait` adds `error` to the status, and `berth doctor` adds it beside `findings`.

Without `--json`, the message goes to stderr as `berth: <message>`, followed by one `next: <hint>` line per hint.
