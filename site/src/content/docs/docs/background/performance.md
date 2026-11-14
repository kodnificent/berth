---
title: Performance
description: What costs time and disk when a berth is made and removed, with the few measured figures.
sidebar:
  order: 3
---

Making a berth costs time and disk in proportion to what it copies, and nothing else about it is slow. This page says what each part scales with, which parts `berth add` waits for, and what removal costs.

The few figures here are single measurements on one machine, each with its method. They show the shape of a cost, not what your machine will do. Where nothing was measured, the page says so and gives no number.

## What costs nothing to speak of

Assigning ports, choosing names, writing the managed block in `.env` and writing `compose.berth.yml` are small file writes. They take the same time for a large project as for a small one.

One step in this group reads more than it writes. For a compose service that builds an image, `berth add` compares the worktree's build inputs with main's to decide whether the worktree can run main's image. That check is capped, at 100 MB of file content and 100,000 directory entries per build context, which keeps it to about a second however large the context is. Past the cap berth stops reading, the worktree builds its own image on `up`, and `add` names the service and the reason. That costs a build, never a wrong image.

## What `berth add` waits for, and what it doesn't

| Copied | When | Scales with |
| --- | --- | --- |
| Gitignored `.env*` files | Before `add` returns | Nothing worth measuring |
| `[files] clone` paths | Before `add` returns | Number of files with copy-on-write, bytes without |
| `[state.sqlite]` files | Before `add` returns | Nothing with copy-on-write, bytes without |
| `clone` volumes | In the background | Bytes in the volume |
| Databases on a server on your machine | In the background | Depends on the engine, see below |

So the only thing that can make `berth add` itself slow is a large `[files] clone` path, or a large SQLite file, on a filesystem without copy-on-write. Everything else that is slow runs after it returns, and `berth status --wait` is how you wait for it. See [Copying data](/docs/guides/copying-data/).

## Files and dependency folders

This is the copy every project has: `vendor/`, `node_modules/` and whatever else `[files] clone` lists.

**With copy-on-write**, the worktree's files share their blocks with main's until one side writes. No file content is read or written, so the cost follows the number of files, not their size:

- On APFS, berth asks for the whole directory in one call. The filesystem still creates an entry for every file.
- On btrfs and XFS, and on ReFS (a Windows Dev Drive), berth clones file by file, one request per file.
- On every platform, berth then walks the copy once to repoint symlinks that led into main and to record its size.

**Without copy-on-write**, every byte is read and written: ext4, NTFS, XFS made without reflink, or main and the worktree on different filesystems. The cost follows total bytes, and for many small files the per-file overhead of the filesystem too.

`berth add` prints which path ran and how long each listed path took, and `berth doctor` warns ahead of time when a dependency folder will be a full copy.

**One measurement.** A fixture of 30,000 files of 2 KB each in 300 folders (117 MB on disk), on an Apple M3 Pro laptop running macOS 26.5, with berth built from source in November 2026 and three runs of each:

| Case | `berth add` reported | `berth remove` took |
| --- | --- | --- |
| Main and worktree on the internal APFS disk | `cow` in 0.50 to 0.57 s | 1.1 to 1.2 s |
| Worktree on an HFS+ disk image, which has no copy-on-write | `copy` in 19.5 to 24.6 s | 1.7 to 1.8 s |

The second row is a deliberately poor target, since a disk image is slow at creating small files. Don't read it as a figure for ext4. It is there for the shape: the same folder moves from under a second to tens of seconds once copy-on-write is gone. The reflink path on btrfs and XFS and block cloning on ReFS are tested for correctness and have not been timed.

When a dependency folder would be a full copy, installing from the package manager's cache in `post-add` can be the faster choice. `berth init` already suggests that for pnpm and bun, whose installs from their global stores are quick on any filesystem.

## SQLite

A `[state.sqlite]` file is one file, so with copy-on-write the copy takes a millisecond or two whatever its size. Without it, the copy takes as long as the bytes take, and for a database not in WAL mode main's writes wait that long, failing with `database is locked` if their busy timeout runs out first. `berth add` says so before such a copy starts.

## Postgres

A `template` copy is made by the server itself. Nothing is dumped and nothing crosses the network, so the cost is the server copying its own files.

- **Postgres 18 with its data directory on APFS, btrfs or XFS** can clone those files. Berth's test for this path copied a 1 GB database in 1.57 s, and in 0.78 s on a second run, on Homebrew's Postgres 18.6 with its data on APFS.
- **Anywhere else** the files are copied in full, and the time follows the database's size on disk: Postgres 13 to 17 on any filesystem, and Postgres 18 on ext4.
- **A server in a container counts as anywhere else.** Docker keeps volumes on ext4, so there is nothing to clone. One measurement from berth's design work: a 659 MB database took 13.7 s to copy on Postgres 18 inside Docker Desktop on a Mac.

`berth add --wait` says which path ran, `copy-on-write` or `full copy`.

Two costs here aren't time on your side. Postgres holds new connections to main's database back for as long as the copy runs, so a full copy of a large database is a pause of that length for main's app. And the server reports nothing while it copies, so `berth status` shows a template copy as `cloning` with no percentage.

## MySQL

MySQL has no clone inside the server, so `dump` is always a logical copy: berth reads every row from main's database and inserts it into the worktree's, and the server fills every index again as the rows arrive. It scales with the amount of data and the number of indexes, on every platform and filesystem. Expect about what a `mysqldump` piped into `mysql` would take for the same database.

Berth's own copier has not been benchmarked. The one recorded figure is from an early trial that ran `mysqldump` into a second server inside Docker Desktop on a Mac: 595 MB in 30.6 s. Treat it as an order of magnitude.

What the copy does not cost is main's time. InnoDB tables are read from one snapshot and keep taking writes throughout. Only tables in another engine, such as MyISAM, have their writes held, one table at a time, while that table is read.

Three ways to pay less:

- **`snapshot`** moves the read off `berth add`. `berth snapshot` reads main once and keeps a compressed copy outside the repository; every `add` and `reset` then loads from that copy and never reads main. Loading is still linear, and the snapshot takes disk of its own.
- **A seed file** is linear in the size of the file, which is usually much smaller than main's data.
- **`fresh`** creates an empty database and leaves filling it to your `post-add` hook.

[Local databases](/docs/stacks/local-databases/) covers each of these.

## Docker Desktop volumes

A `clone` volume is always a full copy. Docker Desktop keeps volumes on an ext4 disk inside its virtual machine, where there is no copy-on-write and your machine's filesystem can't help. Docker on native Linux gets the same full copy, so this section applies there too.

The copy runs in a short-lived helper container that streams main's volume into the worktree's with `tar`. Its time follows the bytes in the volume. In order:

1. Before any copying, `berth add` measures each source volume for the disk check and the progress figure. That is a walk of every file in the volume, done in one helper container for all of them.
2. The helper for the copy starts. Starting a container took about two seconds on the machine berth was developed on, and it happens before anything pauses.
3. Main's running containers that use the volume are paused while `tar` reads it, and resumed straight after. The pause is the read, so it grows with the volume.
4. The copy is moved into place in the worktree's volume.

Volumes that no running container uses are copied with no pause at all. `berth status` shows progress while the copy runs, refreshed every two seconds, and afterwards how long main was paused.

For a database in a volume, this file copy was chosen over a dump because the same early trial found it faster. If the pause matters more to you than the data, set the volume to `fresh` and seed it in `post-add`.

## What removing a berth costs

Removal is cheaper than creation, and it is not free.

- **Cloned files** are deleted file by file. A copy-on-write clone took almost no disk, but it still has as many files as the original, so deleting it follows the file count. In the measurement above, removing the cloned folder took about twice as long as cloning it.
- **Volumes** are removed by Docker, after the worktree's containers and networks.
- **Databases** on a server on your machine are dropped by the server.
- **Redis keys** under the worktree's prefix are found by scanning the server's keys, so that step follows the size of the whole server's key space, not only the worktree's share of it.

If a copy is still running, `berth remove` stops it first instead of waiting for it. Nothing here pauses main.

`berth reset` costs the same as the copies did the first time: it empties each volume and database and copies again. It leaves `[files] clone` paths as they are.

## Disk

- **Copy-on-write clones** take almost no disk when made. They grow only as the worktree or main rewrites files, by the size of what was rewritten.
- **Full copies** take their full size at once: every volume, every `dump` database, every Postgres copy outside the Postgres 18 clone path, and file copies on filesystems without copy-on-write.
- **On Docker Desktop**, volumes live in a disk image that grows on your machine as it fills, so the free space Docker reports can be more than your disk really has. Berth's check uses the smaller of the two.
- **A snapshot** is kept once per repository, compressed, in berth's state directory.

Before a full copy starts, `berth add` warns when it would leave less free disk than `[clone] min_free`, `10GB` by default. The warning covers volumes, database copies, SQLite files and snapshots, and does not cover `[files] clone` paths.

`berth list` shows what each berth holds without measuring anything: it reports the sizes recorded when the copies were made, which keeps it quick however large the berths are. Two things follow. A copy-on-write clone is counted at its apparent size, so the real use is lower. And data that grew after it was copied is counted at its copied size, so the real use can be higher.

## How to see what a copy cost you

Berth reports its own timings, so you don't have to guess which path your machine took:

- `berth add` prints the strategy and time for each `[files] clone` path.
- `berth add --wait` prints the strategy, size and time for each volume and database, and how long main was paused.
- `berth status` shows the strategy, size and pause for each copy once it is `ready`, and progress while it is `cloning`. `berth status --json` adds the time as `seconds`.
- `berth doctor` warns when dependency folders will be full copies.
