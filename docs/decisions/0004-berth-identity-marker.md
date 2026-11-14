# 0004. A berth is identified by a marker in the worktree's git admin directory

Status: accepted

## Context
The design guarantees that a berth follows its worktree: moving a worktree keeps its berth, and a new worktree never inherits a removed worktree's berth, even under the same name. Neither the worktree's path nor git's worktree id is enough: the path changes on `git worktree move`, and git reuses the id (the directory name under `.git/worktrees/`) when a worktree is removed and another is added with the same name.

## Decision
`berth add` gives each berth a random 64-bit id and writes it to `berth-id` inside the worktree's git administrative directory, `<common git dir>/worktrees/<id>/`. A worktree's berth is whichever berth that marker names. Berth finds the directory with `git rev-parse --path-format=absolute --show-toplevel --git-dir --git-common-dir`; the worktree is the main one when the git dir and the common dir are the same.

The marker is written before the registry record (0005), so a crash between the two leaves a marker the next `add` adopts, never a record no worktree points at.

## Options considered
- Key on the worktree path: breaks on `git worktree move`.
- Key on git's worktree id: reused after `git worktree remove`, so a new worktree under the same name would inherit the old berth.
- Path plus the worktree's creation time: `move` keeps the directory but not every filesystem keeps a usable birth time, and nothing ties the time to git's view.
- A marker file in the worktree itself: git does not move or delete it with the worktree, it would need an ignore entry, and `git worktree remove` refuses to remove a worktree with untracked files.
- A marker in the admin directory: chosen. Git moves that directory with the worktree, keeps it for a locked worktree whose directory is missing, and deletes it on `git worktree remove` and `git worktree prune`, which is exactly the lifetime a berth needs. Git ignores files it doesn't know in that directory.

## Evidence
Spikes found git worktree ids are stable across `git worktree move` and reused after removal. `e2e/identity_test.go` checks both guarantees against real git (2.50.1). `--path-format` needs git 2.31 or later.

## Undoing it
Identity lives in `registry.readMarker` and the marker write in `Registry.Add`. A replacement must still tell a reused git worktree id apart from the original; existing berths would need their markers migrated or would show as orphaned.
