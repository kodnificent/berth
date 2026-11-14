---
title: Removing and pruning
description: Freeing ports, deleting data, and how berth knows what is its own.
sidebar:
  order: 4
---

## `berth remove`

Run `berth remove` in a worktree, or `berth remove <path>`, before or after you are done with it. It deletes what berth made for that worktree, then prints each thing it removed:

- the containers and networks of the worktree's compose project;
- the volumes berth created for it, both copies of main's and fresh ones;
- images berth named for it;
- files it copied or wrote, such as `.env` and `compose.berth.yml`.

If the worktree had its own `.env` before berth arrived, only the managed block comes out, and the file is left exactly as it was. Tracked files are never touched, and nothing of main's is deleted, shared volumes included.

A `pre-remove` hook runs first, in the worktree. If it fails, berth stops and deletes nothing.

```bash
berth remove --dry-run   # list what would go, delete nothing
berth remove
git worktree remove .worktrees/fix-checkout
```

## `berth prune` and `--dry-run`

When a worktree is deleted without `berth remove`, its berth becomes orphaned. `berth prune` deletes every orphaned berth the same way `remove` would, without running `pre-remove`, since the worktree is gone. Check first:

```bash
berth prune --dry-run
berth prune
```

The dry run lists each orphan and everything that would be deleted. `prune` then deletes exactly that list.

## Idle berths and `--idle`

A worktree left on disk after its work is done keeps its berth, ports and data, since git still lists it. `berth prune --idle 7d` removes every berth unused for seven days, the same way `remove` would, `pre-remove` included, and leaves the worktree and your work in it alone. A berth with a running container or a locked worktree is never removed.

```bash
berth prune --idle 7d --dry-run
berth prune --idle 7d
```

Durations are in days, hours or minutes: `7d`, `12h`, `30m`. Run `berth add` in a pruned worktree to get a fresh berth cloned from main.

## Leftovers in `berth list`

The first berth command after a worktree disappears frees its ports, so other worktrees can use them at once. Its name stays reserved until the berth is pruned. Its volumes and files stay, and `berth list` shows the berth as orphaned with the disk space it still holds. Nothing is deleted until you prune it.

Berth only deletes what it recorded as its own. A volume or container that merely has a matching name is left alone, and so is a volume labelled with the worktree's compose project by hand.

## Locked and moved worktrees

A worktree locked with `git worktree lock` keeps everything, ports included, even while its folder is missing, for example on an unplugged drive. A worktree moved with `git worktree move` keeps its berth.
