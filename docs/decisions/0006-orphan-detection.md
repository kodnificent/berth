# 0006. A berth is orphaned when git has forgotten its worktree, or would on the next prune

Status: accepted

## Context
`berth list` shows whether each berth is orphaned. The design says a berth becomes orphaned when git reports its worktree as gone, that a missing directory alone doesn't prove the work was thrown away, and that a locked worktree keeps everything. Git has no hook for `git worktree remove`, so berth checks lazily whenever it lists berths.

## Decision
A berth is orphaned when any of these holds:

- its worktree's admin directory no longer exists (`git worktree remove`, or a prune);
- the admin directory's marker names a different berth (git reused the id for a new worktree, 0004);
- the worktree is unlocked and the `.git` file its admin directory points at is missing, which is exactly when `git worktree list` calls it prunable.

A locked worktree is never orphaned by a missing directory, because git never prunes it. Berth reads the admin directory's `gitdir` and `locked` files directly, resolving a relative `gitdir` against the admin directory. Orphaned is computed on every read, never stored, so a worktree that comes back is no longer orphaned.

## Options considered
- Parse `git worktree list --porcelain`: it reports prunable and locked but not the admin directory, so it can't tell an original worktree from a new one that reused the id; it also needs one git process per repository.
- Orphaned only when the admin directory is gone: a worktree deleted with `rm -rf` would hold its ports for months until git's automatic prune, against the design's "free its ports and hostname" for gone worktrees.
- Orphaned whenever the directory is missing, locked or not: contradicts the design's rule that a locked worktree keeps everything.

## Evidence
`gitdir` and `locked` are documented in gitrepository-layout(5). `e2e/identity_test.go` covers the cases against real git; `internal/worktree/worktree_test.go` covers relative `gitdir` and the locked and unlocked missing cases.

## Undoing it
The rule is `registry.check` and `worktree.Inspect`. Dropping the prunable case means only `git worktree remove` or prune orphans a berth; freeing its ports (0027) would then wait for git.
