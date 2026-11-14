# 0027. Freeing an orphan's ports on the next command, and pruning orphans

Status: accepted; amended by [0039](0039-pruning-idle-berths.md) for `prune --idle`

## Context
Git has no hook for `git worktree remove`, so the next berth command frees a gone worktree's ports and hostname, and lists the berth as orphaned with its disk use. Its data stays until `berth prune` or `berth remove` (design, Lifecycle). `prune --dry-run` lists every orphan and what would be deleted; `prune` deletes exactly that. 0006 decides when a berth is orphaned.

## Decision
- **When:** opening the registry, which every command that reads it does under the lock, checks each record that still holds ports or a host. An orphaned one loses both and is saved, so the pool and the host are free for the next `add`. Orphaned itself is still computed on every read, never stored (0006).
- **What stays reserved:** the orphan's name and compose project. Its volumes still carry the project's name until it is pruned, and a new berth under that project would adopt them, inheriting a removed worktree's data. With `{name}` in every host, the freed host can't come back under another name either, so in practice it is free only once the orphan is pruned or `[berth] host` changes.
- **A worktree that comes back:** its berth has no ports until the next `add` assigns them again and rewrites its files.
- **Prune:** every orphaned berth is torn down as `remove` does (0026), but without `pre-remove`, since the worktree it would run in is gone. Recorded files are deleted only when the old directory still exists and is no longer inside any git worktree; a path git knows may be a new worktree whose files are another berth's. `--json` gives `{dry_run, berths: [{id, worktree, dry_run, removed, reason, last_used}]}`, where `reason` is `orphan` or `idle` and `last_used` is the berth's last use as `list` reports it (an orphan's from its recorded use or creation, since its worktree is gone). A berth that can't be pruned (post-add running, Docker unreachable, a failed deletion) is reported and the others go on; the command then exits 1, and with `--json` the failure is `error` in the same object, beside `berths` (0026).
- **Idle berths:** with `--idle <duration>`, prune first deals with orphans exactly as above, under one registry lock, then releases it and removes each idle berth as 0039 decides. Without `--idle` nothing else happens, and no `pre-remove` runs.
- `list --json` reports each berth's `host` and `ports`, empty for an orphan.

## Options considered
- Freeing ports only in `prune`: an orphan would hold ports for as long as its data stays, against the design.
- Freeing the name too: a new berth on the same branch would get the orphan's compose project and its volumes.
- Running `pre-remove` from the main worktree during prune: it would act on main, not on the berth it was written for.

## Evidence
`e2e/remove_test.go` covers: with a one-port pool, `list` after `git worktree remove` shows the orphan with its 1 MiB clone counted and no host or ports, and the next `add` gets the freed port; and for a removed and an `rm -rf` worktree, `prune --dry-run` lists both and deletes nothing, then `prune` deletes exactly those items while a live berth and main's volume stay.

`e2e/teardown_gaps_test.go`: an orphan pruned with Docker unreachable exits `1` with one object holding `berths` and `error`. `internal/cli/prune_test.go` covers a failed prune printing one object.

## Undoing it
`registry.reconcile` runs from `registry.Open`; prune is `runPrune`, `pruneOrphans`, `orphanDir` and `pruner.finish`, which prints the one object, in `internal/cli/prune.go`.
