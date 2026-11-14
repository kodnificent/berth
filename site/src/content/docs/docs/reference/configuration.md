---
title: Configuration
description: Every key in berth.toml, its default, and how layers combine.
sidebar:
  order: 2
---

Berth works with no config at all. Config only adds precision: which ports to keep stable, which volumes to copy, which env keys get the worktree's URL. Run `berth doctor` in your main checkout to check a config before the first worktree uses it.

To start, run [`berth init`](/docs/reference/commands/#init) in your main checkout. It writes the keys it can detect for certain, `[compose.ports]`, `[compose.volumes]` and dependency directories under `[files] clone`, and comments out everything else it found, with a reason, under the table the key belongs to. Uncomment a suggestion by deleting its `# `. Arrays put each suggested entry on its own commented line, so the list stays valid whichever you keep.

## Config layers

Config is layered like git's. Each layer overrides the ones below it, key by key; an array replaces the lower layer's array whole.

| Layer | Location | Committed | Use |
| --- | --- | --- | --- |
| Repo, personal | `berth.local.toml`, or `.git/berth.toml` | Never | Your own setup, or the whole config when the team hasn't adopted berth |
| Repo | `berth.toml` | Yes | Team defaults |
| User | `~/.config/berth/config.toml` | No | Machine defaults: port pool, hostname suffix, clone preferences |
| Built-in | In the binary | n/a | The defaults on this page |

Berth reads the personal layer from your main checkout, so one file serves every worktree, even though git doesn't copy ignored files into new worktrees. A `berth.local.toml` inside a worktree has no effect, and `berth doctor` warns about one.

`berth.local.toml` sits at the top of your main checkout, next to `berth.toml`, where your editor shows it, like `.env.local`. When `berth config edit` creates it, berth adds `/berth.local.toml` to `.git/info/exclude` so it never shows in `git status`; your `.gitignore` is left alone.

Because it is an ignored file, `git clean -fdX` deletes it, along with everything else git ignores. If you run that, keep your personal config in `.git/berth.toml` instead: it works the same way, stays out of sight and survives `git clean`. Use one or the other. With both, berth refuses the config with exit code `5` and names both files.

`berth config path` prints which file is in use, and `berth config edit` opens it, so scripts and agents never need to know which one you chose.

Berth refuses a config it doesn't understand. An unknown key, a value of the wrong type or a template naming an undefined port stops the command with exit code `5`, naming the file and the key, so a typo never falls back to a default without a word.

## `[berth]`

```toml
[berth]
host = "{name}.myapp.localhost"
url = "http://{host}:{port.web}"
```

| Key | Default | Meaning |
| --- | --- | --- |
| `host` | `{name}.localhost` | The worktree's hostname. Must contain `{name}`, so no two worktrees share a host or its cookies |
| `url` | `http://{host}` | The worktree's URL, available as `{url}` |

## `[env]` and `[env.set]`

Berth writes these keys into the managed block at the end of the worktree's `.env`, and never touches anything outside the block.

```toml
[env]
url  = ["APP_URL"]                   # receive the worktree's URL
host = ["SANCTUM_STATEFUL_DOMAINS"]  # receive the worktree's host

[env.set]
APP_URL = "{url}"
VITE_REVERB_HOST = "{host}"
VITE_REVERB_PORT = "{port.web}"
ALLOWED_ORIGINS = "{host},app.{host},console.{host}"
QUEUE_CONNECTION = "sync"            # plain values work too
```

`COMPOSE_PROJECT_NAME` and `COMPOSE_FILE` are berth's own and can't be set here.

Keys that bundlers inline into build output (`VITE_`, `NEXT_PUBLIC_`, `REACT_APP_`, `PUBLIC_`, `NUXT_PUBLIC_`) take effect only when the app is built. If you set one to a value that differs from main's, build output copied from main still carries main's value, and `berth doctor` warns about it. Build again in the worktree.

## `[ports]`

```toml
[ports]
vite = { env = "VITE_PORT" }   # a port for a process outside compose
```

Each entry gets a stable port from the pool, available as `{port.<name>}`. With `env`, berth also writes the port into that key.

## `[pool]` and `[clone]`

```toml
[pool]
ports = "20000-29999"   # default

[clone]
min_free = "10GB"       # default
```

`[pool] ports` is the range every worktree on the machine draws assigned ports from, usually set in the user config. `[clone] min_free` is the free disk below which `berth add` warns before a copy; the copy still runs.

## `[compose]`, `[compose.ports]`, `[compose.volumes]`

```toml
[compose]
files = [".worktree/compose.yml"]  # applied in worktrees only, after the repo's own files
exclude = ["mailpit"]              # never runs in a worktree

[compose.ports]
"web:80" = "assign"                # a stable port from the pool; {port.web}
"mailpit:8025" = "ephemeral"       # Docker picks; berth status shows it
"db:3306" = "none"                 # not published in worktrees

[compose.volumes]
mysql = "clone"                    # a copy of main's volume
composer_cache = "share"           # main's volume itself
redis = "fresh"                    # a new, empty volume (the default)
```

A published port not listed under `[compose.ports]` is `assign` when main publishes it on a fixed port, and `ephemeral` when main lets Docker pick. A worktree keeps its ports until it is removed. See [Docker Compose](/docs/stacks/docker-compose/) for what each policy and role does.

`berth doctor` in main reports a `[compose]` entry that names a service or volume your compose project doesn't have.

## `[state]`

```toml
[state.mysql]
from = "env:DB_*"                  # the server main's .env already points at
prefix = "env:DB_DATABASE_PREFIX"  # the app derives its database names from this key
default = "skip"                   # databases not listed are left alone

[state.mysql.databases]
central = "dump"                   # names after the prefix: app_central
"tenant_*" = "fresh"               # created empty; post-add seeds them
"1" = { from = "file:database/seeds/tenant.sql" }  # a seed file, plain SQL or gzip
"reports" = { command = "./scripts/copy-reports.sh" }  # your own script
```

Per-database control for database servers outside compose. Strategies are `dump`, `fresh`, `skip`, `none` (keep using main's database, which `doctor` warns about) and `snapshot`, or a table: `{ from = "file:<path>" }` loads a seed file, plain SQL or gzip, resolved in the main worktree, and `{ command = "<script>" }` runs your script once in the main worktree with `BERTH_SOURCE_DB`, `BERTH_TARGET_DB`, `BERTH_DB_HOST`, `BERTH_DB_PORT`, `BERTH_DB_USER` and `BERTH_DB_PASSWORD` in its environment (both run as written with main's login; see [local databases](../stacks/local-databases/#starting-from-a-seed-file-or-your-own-script)).

| Key | Meaning |
| --- | --- |
| `from` | `"env:<PREFIX>*"`: the keys in main's `.env` that hold the server and login, such as `DB_HOST`, `DB_PORT`, `DB_USERNAME` and `DB_PASSWORD`; or `"env:<KEY>"`: one key holding them as a URL, such as `DATABASE_URL`. Berth uses the same server and user as main, and never stores or prints the login |
| `prefix` | `"env:<KEY>"`: the key main's `.env` sets its database prefix in. The worktree gets its own prefix, `app_` becoming `app_feat_x__`, written to that key, and every database under main's prefix maps to the same name under the worktree's |
| `default` | The strategy for databases no entry names; `skip` when unset |
| `databases` | Each database by name, the part after the prefix when there is one, or a `*` pattern, which needs `prefix` |

Without `prefix`, each listed database `myapp` becomes `myapp__feat_x`. Names fit MySQL's 64 characters and stay unique, and the worktree's part of a name never holds two underscores in a row, so one worktree's prefix never begins another's or one of main's databases. A prefix the server already holds databases under, including a removed worktree's leftovers, is passed over for the next free one.

Any key in main's `.env` whose value names a database the worktree gets its own of, such as `DB_DATABASE=app_central`, is rewritten in the managed block. Those keys and the prefix key can't also be set under `[env]` or `[ports]`: berth exits `5`, naming the key. A `from` naming keys main's `.env` doesn't set, or a `prefix` key it doesn't set, exits `5` too.

Berth records each worktree's prefix, database names and server before it creates any of them. `remove`, `prune` and `reset` act on that record, never on the config as it is now: `remove` and `prune` drop what it names and every database under the recorded prefix, including ones the app created there, and nothing else. They read the login from the worktree's own `.env`, or for an orphan whose worktree is gone, from the shell's environment, and never need main's `.env`. When the server can't be reached they remove nothing and exit `1`, and running them again finishes the job. `reset` drops the worktree's databases and creates them again, and refuses with exit `1` when main's `.env` now points at another server.

### `[state.postgres]`

```toml
[state.postgres]
from = "env:DB_*"
prefix = "env:DB_DATABASE_PREFIX"
default = "skip"

[state.postgres.databases]
central = "template"
```

The same keys as `[state.mysql]`, for a Postgres server on the machine. Strategies are `template` (a copy of main's database made inside the server), `fresh`, `skip`, `none` and `{ command = "<script>" }`; `dump`, `snapshot` and `{ from = "file:<path>" }` exit `5`, naming the database. A template copy fails while main has another session on the database it copies, naming each one; `disconnect = true` makes berth end those sessions just before the copy instead, saying so first (see [local databases](../stacks/local-databases/#postgres)). `disconnect` is a `[state.postgres]` key only. Names fit Postgres's 63 bytes. `remove`, `prune` and `reset` drop a worktree's databases while its app is connected, ending only the sessions on those databases. A config naming both `[state.mysql]` and `[state.postgres]` exits `5`.

Under either table, `from = "env:<KEY>"` names one key holding the server as a URL, such as `"env:DATABASE_URL"`: berth reads the host, port, login and database from it, and writes the worktree's URL, main's with only the database changed, to that key. The database in the URL must have an entry, under main's prefix when there is one, or berth exits `5`, as it does for a URL naming more than one host or whose query sets the host, port or database. Berth shows that URL with the login and query string taken out, and never records it.

### `[state.sqlite]`

```toml
[state.sqlite]
files = ["database/app.sqlite", "storage/cache.db"]
```

SQLite database files in the worktree that each worktree gets its own copy of, taken from main's at `add`. The copy is one point in time even while main's app writes, WAL included, and shares main's blocks on copy-on-write filesystems, so it is instant. Elsewhere it is a byte copy, which `add` announces, and a database not in WAL mode stays locked while it runs, so main's app can see `database is locked`. Berth never writes to main's files. The exception to consistency is an app writing the database from a container through a Docker Desktop bind mount, whose locks berth can't see: stop it for the copy. A file main doesn't have is reported and the worktree gets none; a later `add` tries again. A file the worktree already has, which berth didn't copy, is left alone, and `add --wait` exits `4` naming it. Each path must stay inside the worktree, and none may also be under `[files] clone`, which would copy it as it is mid-write: either exits `5`, naming `state.sqlite.files`.

### `[state.redis]`

```toml
[state.redis]
strategy = "prefix"
from = "env:REDIS_*"               # REDIS_HOST, REDIS_PORT, REDIS_USERNAME, REDIS_PASSWORD
prefix = "env:REDIS_PREFIX"        # the key the app puts in front of every Redis key
```

A Redis server on the machine, shared with main through a key prefix of the worktree's own. All three keys are required, and `prefix` is the only strategy; anything else exits `5`. A Redis in your compose project is a volume instead, under `[compose.volumes]`.

| Key | Meaning |
| --- | --- |
| `strategy` | `"prefix"`: the worktree's keys live on main's server under a prefix of its own |
| `from` | `"env:<PREFIX>*"`: the keys in main's `.env` that hold the server and login. `<PREFIX>HOST` is required, `<PREFIX>PORT` defaults to 6379, and `<PREFIX>USERNAME` and `<PREFIX>PASSWORD` are sent only when set. A password the server says it has none for, such as `REDIS_PASSWORD=null` against a server without one, is passed over, as apps do. Berth never stores or prints the login |
| `prefix` | `"env:<KEY>"`: the key main's `.env` sets its key prefix in. The worktree's prefix is its name and two underscores in front of main's, `app_` becoming `feat_x__app_`, written to that key. When the worktree's name would give a prefix that begins with main's, as branch `app-x` would beside `app_`, berth puts a letter in front, `b_app_x__app_`, so main's keys never fall under it |

When main's prefix is empty or unset, `berth add` exits `1`, saying to set one: main's keys could then look like anything, and no prefix could be kept apart from them. A prefix that already has keys under it, on any logical database, or that another worktree on the server holds, is passed over for the next free one, and no worktree's prefix begins another's or main's. The prefix key can't also be set under `[env]` or `[ports]`, or be `[state.mysql]` or `[state.postgres]` `prefix` too: berth exits `5`, naming the key. This replaces an `[env.set]` entry for `REDIS_PREFIX`, which `berth init`'s `laravel` preset may have written.

Berth records the worktree's prefix and server before it writes the prefix to the worktree's `.env`. `remove`, `prune` and `reset` delete every key under the recorded prefix, on every logical database of the recorded server, and nothing else: the prefix is matched as plain text, so `*`, `?` or `[` in it never widen what is deleted. Like `[state.mysql]`, `remove` and `prune` read the login from the worktree's own `.env`, or the shell's environment for an orphan, never main's `.env`; when the server can't be reached they remove nothing, exit `1`, and running them again finishes the job. `reset` refuses with exit `1` when main's `.env` now points at another server. `berth status` shows the prefix under the managed env. A prefix keeps keys apart, not commands: `FLUSHDB` or `FLUSHALL` from main or any worktree still clears every worktree's keys on the server.

## `[files]`

```toml
[files]
clone = ["vendor", "node_modules", "storage/app", "phpunit.xml"]
```

Paths inside the worktree to copy from main when the worktree lacks them. Gitignored `.env*` files are copied without being listed. Symlinks that point into main are pointed into the worktree; file contents are never rewritten.

`berth doctor` warns about small gitignored files at the top of main, such as `phpunit.xml` or `auth.json`, that the worktree lacks and this list doesn't include.

## `[hooks]`

```toml
[hooks]
post-add = "composer install && npm ci"
pre-remove = "docker compose down"
```

`post-add` runs in the background once every copy is done; `berth status --wait` waits for it. `pre-remove` runs before `berth remove` deletes anything, and stops the removal if it fails. See [Hooks](/docs/guides/hooks/).

## `[doctor]`

```toml
[doctor]
probe = ["http://app.{host}:{port.web}/login"]
```

URLs `berth doctor --probe` requests besides the worktree's URL, as templates that may use every [template variable](#template-variables). Each must be `http` or `https` on the worktree's host or a name under it, written with `{host}`; any other host is a config error, since the probe only ever requests the worktree's own app. A URL on a port that isn't the worktree's is reported as not requested.

## `[proxy]` and `[proxy.routes]`

```toml
[proxy]
port = "web"                 # the port a worktree's host, and names under it, are sent to
cookies = "warn"             # or "rewrite"

[proxy.routes]
"vite.{host}" = "vite"       # a name under the host sent to another of its ports
```

What [`berth proxy`](/docs/reference/commands/#proxy) sends each worktree's names to. `port` and each route's value name a port as `{port.<name>}` does: an `assign` port or a `[ports]` entry. A route's key may use `{name}` and `{host}` and must be a name under the worktree's host, so one worktree's routes can never take another's names; anything else exits `5`, naming `proxy.routes`. The proxy only ever sends a worktree's requests to ports berth assigned that worktree.

`cookies` says what the proxy does with a cookie the app sets with a `Domain=` wider than the worktree's host, such as `.myapp.localhost`, which a browser would send to every worktree under that name. `"warn"`, the default, reports it on the proxy's stderr and in `berth doctor`. `"rewrite"` reports it too and replaces the domain with the worktree's host, so the cookie still reaches the worktree's subdomains and no other worktree. A cookie set by a routed name is the worktree's like any other. A domain of the worktree's own host or a name under it, or of an unrelated site, which browsers refuse, passes untouched either way. Any other value exits `5`, naming `proxy.cookies`.

```toml
# ~/.config/berth/config.toml
[proxy]
http = 8080                  # default 80
https = 8443                 # default 443
```

The ports the proxy listens on, on the loopback addresses only. One proxy serves every repository, so these are read from the user config alone; set in any other layer they exit `5`.

## Template variables

| Template | Value |
| --- | --- |
| `{name}` | The worktree's name: from its branch, a valid DNS label, unique on the machine, and fixed once created |
| `{host}` | `[berth] host` with `{name}` filled in |
| `{url}` | `[berth] url` filled in |
| `{port.<name>}` | A stable port: `{port.web:80}` for a compose port, `{port.web}` when it is the service's only assigned port, or a `[ports]` entry |

`[berth] host` may use only `{name}`; `[berth] url` may use `{name}`, `{host}` and `{port.<name>}`; `[env.set]` may use all four. An `ephemeral` or `none` port has no stable value, so naming one is an error.
