---
title: Local databases
description: Postgres, MySQL or Redis installed on your machine, and SQLite files in the worktree, kept separate for each worktree.
sidebar:
  order: 2
---

## One server, one database per worktree

A MySQL server installed on your machine (DBngin, Herd, brew) is one server for every worktree, so without berth they all use main's databases. A shared server is fine; a shared database is not. With `[state.mysql]`, each worktree gets its own databases next to main's on the same server, and berth points the worktree's `.env` at them:

```toml
[state.mysql]
from = "env:DB_*"        # the server and login main's .env already sets
default = "skip"

[state.mysql.databases]
myapp = "dump"           # this worktree gets myapp__feat_x, a copy of main's myapp
logs = "fresh"           # and logs__feat_x, created empty
```

| Strategy | What the worktree gets |
| --- | --- |
| `dump` | A copy of main's database, as it was at one moment |
| `fresh` | An empty database, for your `post-add` hook to migrate or seed |
| `skip` | Nothing: berth leaves the database alone |
| `none` | Main's own database, shared on purpose (see below) |
| `{ from = "file:<path>" }` | A database loaded from a seed file (see below) |
| `snapshot` | A database copied from the snapshot `berth snapshot` took (see below) |
| `{ command = "<script>" }` | A database your own script fills (see below) |

Berth reads the server from the keys main's `.env` sets under that prefix, `DB_HOST`, `DB_PORT`, `DB_USERNAME` and `DB_PASSWORD`, and uses the same server and user as main. It never asks for a login of its own, and never stores or prints one.

## How the copy is made, and how long it takes

`berth add` returns in seconds and copies `dump` databases in the background. `berth status` shows each one as `cloning` with how far it is, then `ready`; `berth status --wait` returns once every copy has finished, and exits `4` if one failed. Your `post-add` hook runs after the copies, so it can migrate the copy rather than an empty database.

```text
database app_feat_x__central  cloning (62%, 410 MB of 660 MB)
```

Berth talks to the server itself, so you don't need `mysqldump` or the `mysql` client installed, and Docker doesn't have to be running. The copy is linear: expect it to take about as long as a `mysqldump` of the same database.

**Main keeps working.** Berth reads InnoDB tables, the default engine, from one consistent snapshot, the way `mysqldump --single-transaction` does, so main's InnoDB tables keep taking writes the whole time and the copy still has no half-written transactions. A table in another engine, such as MyISAM, has no snapshot, so berth holds writes to that table alone while it reads it, and says so before it starts:

```text
berth: writes to main's legacy_log (MyISAM) in database app_central will pause while the copy reads those tables, ...
```

Writes to it wait rather than fail, and flow again as soon as that table is read. Converting the table to InnoDB (`ALTER TABLE legacy_log ENGINE=InnoDB`) removes the pause. Berth never pauses a table it didn't name: if a table stops being InnoDB between `add` and the copy, the copy fails and asks you to run `berth reset`, which names it first.

**What is copied.** Tables with their rows, indexes and foreign keys, views and triggers, created by the user berth connects as. The copy never points back at main: a foreign key into another database is dropped, and a view or trigger that names another database, such as a trigger writing to `app_central.audit`, is left out rather than copied, since in the worktree it would read or write main's data. So is a table whose engine keeps its rows somewhere else, such as MERGE or FEDERATED. Stored procedures, functions and events are not copied, as `mysqldump` leaves them out by default, and a view calling a stored function is left out with them. `berth status` lists everything left out under the database's notes, and the database is still ready, so your `post-add` hook can recreate what the app needs.

A migration on main during a long copy waits at most for the table being read, never for the whole copy.

**Disk.** Before copying, `berth add` warns when a copy would leave less free disk than `[clone] min_free` (10 GB by default) where the server keeps its data, naming the database and its size. The copy still runs.

**If a copy stops.** A copy killed by a crash or sleep shows as `failed`, never `ready`; run `berth add` again to copy it afresh. Until a copy is `ready`, the worktree's database may hold only part of main's data, so wait for `berth status --wait` before running the app.

## Starting from a seed file or your own script

A large or private database doesn't have to be copied from main. Point a database at a seed file, or at a script of your own:

```toml
[state.mysql.databases]
central = "dump"
"1" = { from = "file:database/seeds/tenant.sql" }     # a seed file, plain SQL or gzip
reports = { command = "./scripts/copy-reports.sh" }   # a script of yours
```

The path is resolved in the main worktree, so a different file at that path on a branch is not the one loaded. Both run in the background the way `dump` does: `add` returns, `berth status` shows the database as `cloning`, a seed file with its progress, `berth status --wait` waits, a crash reads as `failed` and `berth add` again recovers.

**Seed files.** Plain SQL and gzip both work, including `mysqldump` output with its triggers and views. The worktree's database is the default one, so a file that doesn't name a database loads there. A failure names the file and the line, and fails only that database.

A seed file is your own SQL, and it runs as written, with main's login. Berth doesn't inspect it, so a seed that names another database, with `USE` or `other_db.table`, reaches that database, main's included, the same as running the file with the `mysql` client. Dump a single database without `--databases`, so the file holds no `USE` or `CREATE DATABASE`. A dump also keeps each view's and trigger's `DEFINER`; loading one defined by another user needs the same privilege it would with the `mysql` client.

**Your script.** A `command` database is created empty, then your script runs once, in the main worktree, with these variables in its environment, never its arguments:

| Variable | Holds |
| --- | --- |
| `BERTH_SOURCE_DB` | Main's database name |
| `BERTH_TARGET_DB` | The worktree's database name |
| `BERTH_DB_HOST`, `BERTH_DB_PORT` | The server main's `.env` points at |
| `BERTH_DB_USER`, `BERTH_DB_PASSWORD` | The login main's `.env` sets |

Exit `0` reads `ready`, anything else `failed`. The script's output goes to a log `berth status` names under the database, so `cat` it for what went wrong. Berth itself never prints the password. The script runs with main's login, the same way `post-add` does: it is trusted code, and it can reach anything that login can, main's databases included, so write it to touch only `BERTH_TARGET_DB`.

## Cloning from a snapshot

A database configured as `snapshot` clones from a copy of main's data taken ahead of time, so main never pauses for `add` or `reset`:

```toml
[state.mysql.databases]
central = "snapshot"
```

Run `berth snapshot` in the main worktree, whenever you want a fresh copy:

```text
$ berth snapshot
Took a snapshot of database app_central (640 MB of data, 180 MB on disk)
```

It reads main the way `dump` does, from one consistent snapshot with main's InnoDB tables taking writes throughout, and leaves out the same things: views and triggers naming another database, and tables that keep their rows outside it. `berth add` and `berth reset` then build each worktree from that copy in the background, like a `dump`, without touching main's tables at all. Main can change or be locked for as long as it likes; `reset` rebuilds a worktree from the snapshot as it was taken. `berth status` shows how old each one is:

```text
database app_feat_x__central  ready (snapshot, 640 MB, from app_central, taken 3h ago)
```

Before any `berth snapshot`, a `snapshot` database fails with a message saying to run one, and the others finish; `berth reset` refuses without dropping anything until there is one. Snapshots live in berth's state directory, readable only by you, one per repository, until the next `berth snapshot` replaces it; `berth remove` and `prune` never touch them. A snapshot that is killed partway leaves the previous one in place, and the next run cleans up after it.

## Starting over from main

`berth reset` drops the worktree's databases, including any the app created under its prefix, and copies its `dump` databases from main again, as main is now, loads its seed files as they are now, runs its `command` scripts again, restores its `snapshot` databases from the current snapshot, keeping the worktree's names and ports. `fresh` databases come back empty. `berth reset --dry-run` lists what it would drop first. If a reset is interrupted, run `berth reset` again.

## Pointing your app at its own database

Any key in main's `.env` whose value names one of the databases, such as `DB_DATABASE=myapp`, gets the worktree's name in the managed block, so the app connects to its own database with no change.

If the app derives its database names from a prefix, as multi-tenant packages do, set `prefix` to the key it reads:

```toml
[state.mysql]
from = "env:DB_*"
prefix = "env:DB_DATABASE_PREFIX"

[state.mysql.databases]
central = "dump"         # app_central is copied into app_feat_x__central
"*" = "fresh"            # every other database under app_, created empty
```

The worktree gets its own prefix, `app_feat_x__`, written to `DB_DATABASE_PREFIX`. A database the app creates under it later, such as a new tenant, belongs to the worktree: `berth remove` lists and drops it with the rest. Nothing outside the prefix is ever dropped, and no worktree's prefix begins another's, so `feat` and `feat-x` never mistake each other's databases.

`berth remove` and `berth prune` work from what berth recorded when it made the databases, not from your config or main's `.env` as they are now, so changing either never redirects a drop. They read the login from the worktree's own `.env`; for an orphan whose worktree is already gone, export the login in your shell (`DB_USERNAME`, `DB_PASSWORD`) and run `berth prune`. If the server isn't running, nothing is removed and running the command again once it is finishes the job.

## Sharing a database on purpose

`none` keeps a worktree on main's own database:

```toml
[state.mysql.databases]
reports = "none"
```

The worktree's `.env` keeps main's name for it, `berth remove` never drops it, and `berth doctor` warns that state is shared, since a migration in the worktree changes main's data. `skip` leaves a database out entirely: berth creates nothing and writes nothing for it.

## Postgres

A Postgres server on your machine works the same way, under `[state.postgres]`: the server and login from main's `.env`, names and prefixes, what a worktree owns, and `remove`, `prune` and `reset` working from berth's record.

```toml
[state.postgres]
from = "env:DB_*"
prefix = "env:DB_DATABASE_PREFIX"
default = "skip"

[state.postgres.databases]
central = "template"               # copied inside the server from app_central
"*" = "fresh"
```

- **Strategies.** `template`, `fresh`, `skip`, `none` and `{ command = "<script>" }`, with the same `BERTH_*` variables for your script. `dump`, seed files and `snapshot` are MySQL's in this version: each exits `5`, naming the database.
- **`template` copies inside the server.** The server itself makes the worktree's database from main's, so nothing is dumped or sent over the network, in the background as `dump` does, with `add --wait`, `status --wait`, recovery after a crash and `reset`. Postgres 18 with its data directory on APFS, btrfs or XFS shares blocks with main's database, so even a large one copies in about a second; anywhere else the files are copied in full. On Linux berth can only tell the filesystem when the server runs as your user; a server run by another user, such as a packaged one, or one in a container, shares blocks when you set `file_copy_method = clone` in its `postgresql.conf`. An XFS made without reflink copies in full even then, so on XFS berth reports the path from the disk the copy took. `berth add --wait` says which path ran, `copy-on-write` or `full copy`, and when the server couldn't clone and copied in full instead.
- **Main's database has to be quiet for the copy.** Postgres copies a database only while no other session is connected to it, and holds new connections to it back until the copy ends. When main's app has a session open on it, that database fails, naming each session's application and client address, the others finish, and nothing of main's is touched: stop main's app, or its workers, and run `berth add` again. To have berth end those sessions instead, set `disconnect = true` under `[state.postgres]`: `add` and `reset` say so before the copy, their open transactions roll back, and main's app reconnects as it would after a server restart. Only sessions on the databases being copied end, never another database's, and a session under a role your login may not end fails that database with nothing ended. Either way, `add` says first that new connections to main's database will pause for the copy, and they go through as soon as it ends.
- **Names** are cut to Postgres's 63 bytes and stay unique. Berth always quotes them, so a name keeps its case.
- **Connected apps don't block cleanup.** `berth remove`, `prune` and `reset` drop a worktree's databases while its app is still connected, ending the sessions on those databases and on no other. A session on one of them under another login is not berth's to end, so that drop fails and running the command again finishes it.
- **Versions.** Postgres 13 and later.

A project names one host server: `[state.mysql]` or `[state.postgres]`, not both.

## One URL key

If main's `.env` holds the server as one URL, such as Prisma's or Rails's `DATABASE_URL`, name that key instead of a prefix, for MySQL or Postgres:

```toml
[state.postgres]
from = "env:DATABASE_URL"   # postgres://app:secret@127.0.0.1:5432/myapp

[state.postgres.databases]
myapp = "fresh"
```

The database in the URL is the one listed. The worktree's `DATABASE_URL` is main's with only the database changed, `postgres://app:secret@127.0.0.1:5432/myapp__feat_x`, so a percent-encoded login and the query string are kept as they are. A URL naming a database with no entry exits `5`, since the worktree would keep using main's; with `prefix`, the URL's database has to be one under main's prefix. So does a URL naming more than one host, or whose query sets the host, port or database, such as `?dbname=`, since your app's driver would follow it to main's database; parameters such as `sslmode` are fine. The URL holds main's login, as main's does, so `berth status`, `berth env --json` and `berth doctor` show it with the login and query string taken out, and berth's own records never hold it.

## SQLite in the worktree

A SQLite database kept in the worktree, such as `database/app.sqlite`, is gitignored, so a new worktree has none. List each file and every worktree gets its own copy of main's:

```toml
[state.sqlite]
files = ["database/app.sqlite", "storage/cache.db"]
```

`berth add` copies each file as it stands at one commit, even while main's app is writing to it, WAL included, so the copy is never torn. Berth only reads main's files; it holds main's writers off with SQLite's own file locks. Those locks don't cross a Docker Desktop bind mount, so if main's app writes the database from a container on a Mac or Windows, stop it before `berth add` or `berth reset`, or the copy may be torn. On APFS, btrfs and XFS the copy shares main's blocks and is instant; elsewhere berth copies the bytes and says so first. For a database not in WAL mode that copy keeps the database locked until it ends: main's writes to it, and reads queued behind a waiting write, wait and then fail with `database is locked` once the app's busy timeout passes. In WAL mode main's app carries on. `berth status` lists each file with how it was copied.

A file main doesn't have yet is reported, and the worktree gets none until a later `berth add` finds it. A file the worktree already has, which berth didn't copy, is never overwritten. `berth reset` copies each file from main again, and `berth remove` deletes the worktree's copies, with the `-wal`, `-shm` and `-journal` files SQLite keeps beside them, and nothing else: another database you made in the worktree stays. Don't list a database file under `[files] clone` as well, since that copies it without SQLite's locks; berth refuses the config and exits `5`.

## Redis on your machine

A Redis server installed on your machine is one key space for every worktree, so their caches, sessions and queues mix with main's. With `[state.redis]`, each worktree keeps its keys under a prefix of its own on the same server:

```toml
[state.redis]
strategy = "prefix"
from = "env:REDIS_*"          # REDIS_HOST, REDIS_PORT, REDIS_USERNAME, REDIS_PASSWORD
prefix = "env:REDIS_PREFIX"   # the key your app puts in front of every Redis key
```

The worktree's prefix is its name and two underscores in front of main's, so main's `app_` becomes `feat_x__app_`, written to `REDIS_PREFIX` in the managed block. When the worktree's name would give a prefix that begins with main's, as branch `app-x` would, berth puts a letter in front, `b_app_x__app_`. Every key of main's starts with `app_`, so none falls under a worktree's, and no worktree's prefix begins another's. A prefix that already has keys under it, on any logical database, is passed over for the next free one. The worktree starts with no keys; its caches and queues fill as the app runs.

Main's `.env` has to set the prefix: with `REDIS_PREFIX` empty or unset, main's keys could look like anything, so `berth add` exits `1` and says to set it. Your app has to put the prefix in front of its keys too, as Laravel does with `REDIS_PREFIX`.

`berth remove`, `prune` and `reset` delete every key under the worktree's prefix on every logical database of the server, and nothing else. A key whose name holds `*`, `?` or `[` is matched as plain text, so it never widens what is deleted. `remove` and `prune` work from what berth recorded at `add`, never main's `.env`, and read the login from the worktree's own `.env`; if the server isn't running, nothing is removed and running the command again finishes the job. `reset` also reads main's `.env`, and deletes nothing and exits `1` when it now names another Redis server than the recorded one.

A prefix keeps keys apart, not commands. `FLUSHDB` or `FLUSHALL`, from main or any worktree, still clears every worktree's keys on that server, and some cache-clearing commands send one; pub/sub channels are shared unless your app prefixes them too. When that matters, run Redis in your compose project instead, where each worktree gets its own server.
