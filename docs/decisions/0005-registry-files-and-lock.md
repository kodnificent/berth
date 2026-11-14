# 0005. Registry: one JSON file per berth under one machine-wide lock

Status: accepted; `add` holding the lock for the whole command is superseded by [0034](0034-bounded-image-check-outside-the-lock.md)

## Context
`berth list` shows every berth on the machine, across repositories, so berth keeps a registry outside any repo (design, Implementation > State on disk). `add` must be idempotent and concurrent-safe: running it twice, or twice at once, gives one berth.

## Decision
The registry lives in `BERTH_STATE_DIR` when set, else `$XDG_STATE_HOME/berth` when that is an absolute path, else `~/.local/state/berth` (`%LOCALAPPDATA%\berth` on Windows). Each berth is `berths/<id>.json`, holding its id, repository (the main worktree's path), git common and admin directories, last known worktree path and creation time, and whatever later records have berth write down as its own. Files are written to a temporary file and renamed into place.

Every command that reads or writes the registry first takes an exclusive lock on `<state dir>/lock`: `flock` on Unix, `LockFileEx` on Windows. `add` checks the worktree's marker (0004) and creates a berth only when there is none, under that lock; how long `add` holds it is 0034. The kernel releases the lock if the process dies, so a crash never leaves it held. A platform with neither call refuses to open the registry rather than run unlocked.

Repeated `add` keeps the record unchanged, so `berth status --json` is identical after both runs. When a worktree has moved, the next command that sees it updates the recorded path.

## Options considered
- One file for the whole registry: every write rewrites every berth, and a corrupt file loses them all.
- SQLite: a cgo or large pure-Go dependency for a handful of small records.
- Lock-free creation by linking the marker into place: makes the marker atomic but not the record, and a lock is needed anyway to hand out ports without collisions.
- A lock per worktree: enough for the registry alone, but port allocation must be serialised across all berths, and one lock is simpler to reason about.
- A lock file created with `O_EXCL`: a crash leaves it behind and every later command would need stale-lock recovery.

## Evidence
`e2e/identity_test.go` starts two `add`s at the same moment on each of eight worktrees. With the lock replaced by a no-op it failed repeatedly, leaving two berths at one path; with the lock it passes. It also compares `status --json` byte for byte after two runs of `add`.

## Undoing it
The layout and lock are private to `internal/registry`. Moving to another store means migrating `berths/*.json`; the lock must stay machine-wide, or be replaced by an equivalent, because port allocation depends on it (0010).
