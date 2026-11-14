# 0021. The background clone process

Status: accepted

## Context
A slow clone runs after `berth add` returns, and `status` shows how it stands (design, State cloning). Berth has no daemon. A clone killed by a crash, sleep or reboot must be reported as failed, never `cloning` forever, and running `add` again must recover it (design, Guarantees). `add --wait` returns only when every clone has finished and exits `4` if any failed. A clone may have paused main's services when it dies, and the source must never be harmed.

## Decision
- **Process:** `add` starts `berth __clone <id>`, a hidden command, in its own session with stdin closed and output appended to `<state dir>/clones/<id>.log`, so it outlives the terminal or hook and holds nothing a caller reads. Its state is `<state dir>/clones/<id>.json`, outside the registry, so a long copy never holds the registry lock.
- **Liveness:** a per-berth lock file, `<id>.lock`, taken with `flock`. `add` takes it, writes the entries as `cloning`, and passes it to the clone process as fd 3, so it is held from before the state says `cloning` until the process ends, with no gap. The process marks it close-on-exec so `docker` commands it runs never keep it alive after it dies. The kernel drops the lock however the process ends, `kill -9` included, so a lock that can be taken means no clone is running; there are no pids to go stale. The pid goes in `<id>.pid` for `status` to report.
- **Recovery:** `status`, `add` and `reset` take the lock if it is free. If entries are still `cloning`, the process died: it resumes the containers the process recorded as paused (written before pausing), removes the berth's helper containers by label, and marks those entries `failed` with the reason, naming `berth reset` for a clone `reset` started and `berth add` otherwise. `add` then clones every failed entry again from an emptied target; a `ready` entry is cloned again only by `reset` (0036). `remove` runs the same recovery (0026). So does `list`, for every berth whose clone lock is free: it is the command run from anywhere, main included, and it already reads every berth, so a main left paused by a killed copy is resumed without anyone going to that berth's worktree. A berth whose clone is still running holds its lock and is left alone.
- **Running:** `add` with the lock held by a clone process starts nothing and says a clone is running.
- **`--wait`:** after releasing the registry lock, `add` blocks on the clone lock, then recovers and reports each clone with its strategy, size, time and pause. Any clone volume that isn't ready makes it exit `4`. Without `--wait`, a clone that fails before it can start (Docker unreachable, a target berth didn't create) is warned about and recorded as `failed`, and `add` exits `0`, since the berth itself is usable.

## Options considered
- A pid and heartbeat in the record: a pid can be reused after a reboot, and a heartbeat needs a timeout that is either slow to report or wrong under load.
- Running the clone inside `add` with `--wait` only: `add` without it couldn't return while a clone runs.
- The clone process taking the lock itself once started: between `add` and that moment, `status` would see `cloning` with no lock and call it failed.
- Keeping clone state in the registry record: every progress write would take the registry lock, and the clone process would have to merge with whatever `add` wrote meanwhile.
- A helper container that pauses and resumes the source itself: it would need the Docker socket and an image with the Docker CLI.
- Recovering dead clones on every berth command: `env` and `exec` run on every shell prompt or command for some users, and would take a lock and read a file per berth each time. `list` is the one a person reaches for.
- Recovering from `berth status` in main: it exits `1` there by design, since main has no berth.

## Evidence
`e2e/volumes_test.go` covers: `add` returning within seconds while a 15 s clone runs, shown as `cloning`, then `ready`; `kill -9` while main's container is paused for the copy, giving `failed` in under a second with the container running again, after which `add --wait` gives a copy identical to main's; and `add --wait` returning only after a 5 s clone, ready right after, with exit `4` when its clone process is killed. `internal/volume` tests cover the lock and state files.

`e2e/teardown_gaps_test.go`: `berth list` while a copy runs leaves main's container paused and the copy `cloning`, and after `kill -9`, `berth list` in main has the container running again and the copy `failed`.

## Undoing it
The process and its fd are `spawnClone` and `runClone` in `internal/cli/volumes.go` with `detach` and `holdInheritedLock` in `internal/cli/detach_unix.go`; the lock and state are in `internal/volume`. Deleting `<state dir>/clones` loses only clone history: the next `add` sees no entry and, since every target is recorded, refuses none of berth's own volumes.

Recovery from `list` is `resumeDeadClones`, called from `runList`, in `internal/cli/volumes.go`; dropping the call leaves recovery to the commands run on the berth itself.
