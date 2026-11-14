---
title: Guarantees
description: What berth promises in every worktree, and how each promise is checked.
sidebar:
  order: 3
---

These hold for every berth, on macOS, Linux and Windows. Each one has a test, and a guarantee without a passing test is not shipped.

## Native commands work unchanged

A command that works in your main checkout works in a worktree with a berth, and acts only on that worktree. You run `docker compose up`, your dev server and your tests exactly as you do in main, with no wrapper and no extra flags, because they read the files berth wrote. Berth itself never starts, stops or supervises your services.

Checked by running a repository's own commands in a worktree and confirming they touch that worktree alone.

## No collisions

No two worktrees, and no worktree and main, share a published host port, a container, a volume, a database or a hostname. You can have main and several worktrees up at the same time, and none of them takes another's port or writes to another's data.

Checked by bringing up main and three worktrees at once and looking for any duplicate port or name in `docker ps`.

## Only the host side differs

Inside its containers, a worktree looks like main: the same container ports, the same service names, the same configuration. What changes is what your machine sees: published ports, project and volume names, and any service you excluded. A healthcheck or a config value that reads one of your project's own port variables inside a container sees what it sees in main.

Checked by comparing `docker compose config` in main and in a worktree.

## The repo's own compose files still apply

`compose.override.yml`, and any compose files you list under `[compose] files` for worktrees only, apply in every worktree. Berth adds its own file on top of yours and replaces none of them.

Checked by confirming those files appear in the worktree's resolved compose config.

## Excluding a service never invalidates the project

When you exclude a service with `[compose] exclude`, the services that depend on it still start. `docker compose config` and `docker compose up` succeed in the worktree.

Checked by excluding a service another one depends on, then running both commands.

## A worktree builds only when main would

For the same build inputs, `docker compose up` in a worktree does no build work that the same command in main wouldn't do: it runs the image main already built. When the worktree's inputs differ, it builds as main would after the same change.

The one exception is a build context too large for berth to compare within a few seconds, such as the whole repository with no `.dockerignore`. That worktree builds on `up`. It costs a build, never a wrong image, and `berth add` and `berth status` name the service and the reason.

Checked by running `up` in a fresh worktree and confirming nothing is built, and by a context over the limit that is reported and builds.

## The source is never harmed

Copying never changes or loses main's data. If one of main's services has to pause for a consistent copy, berth says which one before it happens, and the pause lasts only as long as the copy.

This covers what berth does itself. Your own seed files, `command` scripts and hooks run as you wrote them, with main's login, so a bug in one can change main's data. The opt-in Postgres `disconnect = true` ends main's other sessions on the database being copied and rolls back their open transactions; berth says so first.

Checked by comparing checksums of main's data before and after a copy, and by logging how long each pause lasted.

## State is consistent

A worktree's database is a copy of main's as it was at one moment, with no half-written rows or torn files. That holds while main's app keeps writing during the copy.

Checked by copying a database under write load, then running an integrity check on the copy.

## Nothing points back at the source

A symlink in a copied directory that pointed into main points into the worktree instead, so a storage link or a workspace package never sends the worktree's reads and writes back to main. Berth never rewrites the contents of files. Instead, `berth doctor` reports any copied text file that still contains main's path, such as a compiled config cache, and names the file.

Checked with a fixture holding absolute symlinks and a compiled cache.

## A berth follows its worktree

Moving a worktree with `git worktree move` keeps its berth: the same ports, names and data. A new worktree never inherits the berth of one that was removed, even at the same path and under the same name.

Checked by moving a worktree, and by removing one and adding another under its name.

## `add` is safe to repeat and to run twice at once

Running `berth add` twice, or twice at the same moment, gives one berth. The second run keeps the ports, names and data the first one made. Other commands never wait for an `add` that is copying files or comparing build inputs, so `berth list` and `berth status` answer at once.

Checked by repeated and parallel `add`, and by running `list` and `status` during a slow one.

## Interrupted copies are never stuck

A copy killed by a crash, sleep or reboot is reported as failed, never left showing as cloning forever. Run `berth add` again and it recovers.

Checked by killing a copy with `kill -9` partway through.

## Assigned ports are really free

A port berth assigns is free on every network interface at that moment, not only on loopback, and stays reserved for its worktree until the berth is removed. Berth never hands out a port another process already holds.

Checked by holding a port with Docker on `0.0.0.0` and confirming berth never assigns it.

## Generated names are valid

Compose project names, database names and hostnames that berth builds from your branch name fit the rules and length limits of whatever they name. A long branch name, or one with slashes and capitals, still gives names that work.

Checked by generating names from randomised branch names.

## A worktree's database names are its own

On a shared database server, one worktree's databases are never mistaken for another's or for main's. No worktree's prefix begins another worktree's prefix or one of main's database names, and every exact name is unique on the server. Worktrees named `feat`, `feat-x` and `feat-x-y` can share a server, and removing any one leaves the others' databases alone. The same holds for Redis key prefixes and main's keys.

Checked with worktrees of those three names on one MySQL, Postgres and Redis server, removing each in turn.

## Berth deletes only what it created

`berth remove`, `berth prune` and `berth reset` never touch anything berth didn't record as its own. A volume or container that merely has a matching name is left alone. On a shared database server, berth drops the databases it recorded for the worktree and those under the worktree's own prefix, and nothing else. A file berth copied whose parent directory has since become a symlink is left where it is and reported, never deleted through the link.

Checked on a shared server that holds other databases.

## Shadowing is reported

A variable exported in your shell beats the same key in `.env` for compose and most frameworks. When one overrides a key berth manages, such as `COMPOSE_PROJECT_NAME`, `berth status` and `berth doctor` say so and show both values.

Checked by exporting a managed key and running `berth status`.

## Removal is clean

Removing a berth leaves your tracked files untouched. A `.env` the worktree had before berth arrived is restored byte for byte: only the managed block comes out.

Checked by comparing the worktree before `berth add` and after `berth remove`.

## The proxy is local

`berth proxy` accepts connections on the loopback interface only, so nothing else on your network can reach your worktrees through it. Its certificate authority can vouch only for names under `.localhost`, so trusting it can never be used to impersonate another site.

Checked by connecting from another interface, which is refused, and by verifying a certificate the authority signs for any other name, which fails.

## Idle pruning keeps the work

`berth prune --idle` deletes only what berth made for an idle berth. The worktree's tracked files, uncommitted changes and untracked files, ignored ones included, are left byte for byte, and a berth with a running container or a locked worktree is never pruned. Checked by pruning a worktree with staged, unstaged, untracked, ignored and symlinked files and comparing every file before and after, and by keeping a berth with a container up and one with a locked worktree beside an idle one that goes.
