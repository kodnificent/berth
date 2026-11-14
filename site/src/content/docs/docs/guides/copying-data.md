---
title: Copying data
description: What copies fast, what runs in the background, and how to choose per database.
sidebar:
  order: 2
---

## Copy-on-write and when it is available

The paths under `[files] clone`, such as `vendor/` and `node_modules/`, are copied copy-on-write where the filesystem can: the worktree's files share their blocks with main's until either side writes, so a copy takes a moment and almost no disk. A write in the worktree never reaches main's file.

| Filesystem | Dependency directories |
| --- | --- |
| APFS (macOS) | Copy-on-write, the whole directory at once |
| btrfs, XFS (Linux) | Copy-on-write, file by file |
| ext4, and any filesystem without reflink | A full copy, byte for byte |

Main and the worktree have to sit on the same filesystem: a worktree on another disk always gets a full copy. XFS shares blocks only when it was made with reflink, the default for years.

`berth add` says which path ran for each one, and how long it took:

```
berth: cloned vendor (cow) in 41ms
berth: cloned node_modules (copy) in 12.3s
```

`berth doctor` warns before you find out the slow way: when `[files] clone` lists a dependency directory and a test clone from main into the worktree shares nothing, it reports `ext4: dependency clones (node_modules) will be full copies`. On such a filesystem, consider installing dependencies in `post-add` from the package manager's cache instead of cloning them.

Docker volumes are always a full copy, on Docker Desktop and on native Linux alike.

## Background copies and `berth status --wait`

`berth add` does the quick work before it returns: ports, names, the `.env` block, the paths under `[files] clone` and any `[state.sqlite]` files. Two kinds of copy are slower, and run in a background process after `add` returns:

- compose volumes set to `clone` under `[compose.volumes]`;
- databases on a [server on your machine](/docs/stacks/local-databases/), copied with `dump`, `template`, a seed file, your own `command` or a `snapshot`.

`add` says what it started, and its summary shows each one as `cloning`:

```text
$ berth add
berth: main's mysql will pause while volume mysql is copied, so the copy is consistent, and resume as soon as the copy ends
berth: cloning volume mysql in the background; berth status shows how far it is
Added berth 9f2c4e1ab07d3355 for /Users/you/shop/.worktrees/fix-checkout
name          fix-checkout
url           http://fix-checkout.localhost
volume mysql  cloning
next          volume mysql is still cloning; run berth status --wait before starting the app
```

`berth status` shows how far each copy is, then how it went:

```text
volume mysql  cloning (62%, 410.0 MB of 660.0 MB)
```

```text
volume mysql  ready (copy, 660.0 MB, main's mysql paused 31.4s)
```

Until a copy is `ready`, the worktree's volume or database may hold only part of main's data. Don't start the app on it. Wait instead:

```sh
berth status --wait
```

It returns once every copy and your [`post-add` hook](/docs/guides/hooks/) have finished, then prints the usual status. It exits `4` if a copy failed and `1` if `post-add` did. `berth add --wait` is the same wait made part of `add`: it returns once the copies have finished, prints a line for each, and exits `4` if one failed. It doesn't wait for `post-add`.

```text
berth: cloned volume mysql (copy, 660.0 MB) in 31.6s; main's mysql paused for 31.4s
```

Nothing else waits. `berth list`, `berth status` and `berth add` in another worktree all answer at once while a copy runs. With `--json`, each copy is an entry under `state`, with `status`, `strategy`, and while it is cloning `progress`, `copied_bytes` and `total_bytes`.

**If a copy stops.** A copy killed by a crash, sleep or reboot never reads as `ready`. The next berth command reports it as `failed`:

```text
volume mysql  failed: the clone process stopped before it finished; run berth add to clone again
```

Run `berth add` again. It keeps the worktree's berth, leaves the copies that are `ready` alone and starts the failed ones afresh.

**What berth runs.** Volumes are measured and copied by small helper containers that mount main's volume read-only and exit when they are done. Berth never starts your services.

## When main pauses, and for how long

Copying files never pauses anything. Copying data that something is writing to sometimes has to, so the copy is consistent and not torn halfway through a write. Berth always says so before it happens.

| What is copied | What pauses in main | For how long |
| --- | --- | --- |
| `[files] clone` paths | Nothing | |
| A `clone` volume | Main's running containers that use the volume | The copy of that volume |
| MySQL `dump` | Writes to tables that aren't InnoDB. InnoDB tables keep taking writes | While each such table is read |
| Postgres `template` | New connections to the database being copied | The copy of that database |
| A `[state.sqlite]` file | Writers to a database not in WAL mode, on a filesystem without copy-on-write | The copy of that file |

For a volume, berth pauses the containers with `docker pause`, copies, and resumes them. A volume copy is a full copy, so the pause grows with the volume: expect about as long as copying the same bytes with `tar`. If main's containers aren't running, nothing pauses. `berth status` shows how long the pause was, and `berth add --wait` prints it.

A paused container that has a health check reads as unhealthy to Docker until its next check after the pause, and berth's announcement names it. See [troubleshooting](/docs/guides/troubleshooting/#mains-database-reads-unhealthy-during-a-copy).

If the copy is killed while main is paused, the next `berth list`, run anywhere, resumes main's containers, and so does the next `berth status`, `add`, `reset` or `remove` for that worktree.

To keep main from pausing at all:

- Stop main's containers before `berth add`, if you weren't using them anyway.
- Set the volume to `fresh` and fill it in `post-add`, with migrations and seeds.
- For a MySQL server on your machine, clone from a [snapshot](/docs/stacks/local-databases/#cloning-from-a-snapshot), which never reads main during `add`.

[Local databases](/docs/stacks/local-databases/) has the detail for MySQL, Postgres and SQLite.

## Choosing per database: clone, share, fresh

A database in your compose project keeps its data in a named volume, so you choose per volume. Each one gets a role under `[compose.volumes]`:

```toml
[compose.volumes]
mysql = "clone"            # a copy of main's data
composer_cache = "share"   # main's own volume, used by every worktree
redis = "fresh"            # new and empty
```

| Role | The worktree gets | Use it for |
| --- | --- | --- |
| `clone` | Its own volume, filled with a copy of main's | Databases you want real data in |
| `share` | Main's volume itself | Package-manager caches that are safe to share |
| `fresh` | Its own volume, empty | Caches, queues, search indexes, or a database your `post-add` hook migrates and seeds |

A volume you don't list is `fresh`. `berth init` writes every named volume it finds as `clone`, as `share` when it is a package-manager cache, and as `fresh` with the reason when berth can't copy it, so check the list it wrote: a large volume you don't need a copy of is cheaper as `fresh`.

Never `share` a database. Two worktrees writing one data directory is exactly the collision berth exists to prevent.

Berth copies only volumes Docker stores itself. A `clone` volume is not copied, and `berth add` says why, when:

- it uses a volume driver other than `local`: `volume mysql uses the nfs driver; berth only copies local volumes`;
- it mounts a host path through a `device` option, where a copy would be the same storage as main's;
- it is external to main's compose project, so main has no volume of its own to copy.

A volume main hasn't created yet is not an error: the worktree's starts empty, as main's would.

A database on a server installed on your machine isn't a volume. It is configured per database under `[state.mysql]` or `[state.postgres]`, with `dump`, `template`, `fresh`, `skip`, `none`, a seed file, a script or a snapshot. See [local databases](/docs/stacks/local-databases/).

## The free disk check

A full copy needs as much disk as its source. Before a copy starts, `berth add` measures it and warns when it would leave less free disk than `[clone] min_free`, `10GB` by default:

```text
berth: cloning volume mysql (48.2 GB) would leave 6.1 GB of free disk, less than the 10.0 GB that [clone] min_free asks for
```

It is a warning. The copy still runs, so stop it yourself if you need the space: `berth remove` deletes what was copied.

The check covers `clone` volumes, database copies on a server on your machine, SQLite files and `berth snapshot`. It counts the copies ahead of each one, so three volumes that fit one by one but not together are still reported. For volumes it measures the disk Docker stores them on, and on Docker Desktop for Mac the disk that holds Docker's data too. A database server on another machine can't be measured, and berth says so. Paths under `[files] clone` aren't checked.

Raise or lower the threshold in your user config, since it is about your disk and not the repository:

```toml
[clone]
min_free = "25GB"
```

`berth list` shows what each berth holds in its `DISK` column, from the sizes berth recorded when it made the copies. Copy-on-write copies share blocks with main, so the real use is lower than the figure.

## Refreshing a worktree with `berth reset` and `berth snapshot`

A worktree's data is a copy from the moment `berth add` made it. When main has moved on, or the worktree's data is broken, `berth reset` throws the copy away and makes it again from main as it is now:

```text
$ berth reset --dry-run
Would reset berth 9f2c4e1ab07d3355 (/Users/you/shop/.worktrees/fix-checkout), discarding these and making them again:
  volume shop-fix-checkout_mysql
```

```sh
docker compose stop
berth reset
berth status --wait
```

`reset` copies in the background the way `add` does, so wait before starting the app again. It:

- empties each `clone` volume and copies main's again;
- drops and remakes the worktree's databases on a server on your machine, each by its own strategy, and copies its `[state.sqlite]` files again (see [local databases](/docs/stacks/local-databases/#starting-over-from-main));
- deletes the keys under the worktree's Redis prefix.

It keeps the worktree's ports, names, hostname and `.env`, and leaves `share` and `fresh` volumes and the paths under `[files] clone` as they are. It doesn't run `post-add` again, so run your migrations yourself if the fresh copy needs them.

Reset refuses, and discards nothing, while the worktree's containers are running or a copy is still in progress, whatever it would reset:

```text
berth: berth 9f2c4e1ab07d3355's containers shop-fix-checkout-mysql-1 are running, so reset discarded nothing; stop them with docker compose stop and run berth reset again
```

`berth snapshot` is for MySQL databases on your machine configured as `snapshot`. Run in your main checkout, it takes a consistent copy of each one and keeps it until the next `berth snapshot`. `add` and `reset` then build worktrees from that copy without reading main at all, and `berth status` shows how old it is. See [cloning from a snapshot](/docs/stacks/local-databases/#cloning-from-a-snapshot).
