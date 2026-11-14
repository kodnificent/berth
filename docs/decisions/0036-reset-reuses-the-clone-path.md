# 0036. berth reset reuses the clone path, marking entries before any volume is touched

Status: accepted

## Context
`berth reset` gives a berth's `clone` volumes main's data again and keeps its ports, names, host, compose project and managed `.env` block (design, Commands and State cloning). It deletes only what berth recorded as its own (design, Guarantees), announces any pause of main as `add` does, returns while the copy runs, and a reset killed mid-copy must read as failed, never ready while its volume is empty, with running `reset` again as the way back.

## Decision
- **Same path as `add`:** under the worktree lock, `reset` takes the clone lock and recovers a dead clone as `add` does (0021), then hands the same lock to the code `add` uses (`cloneUnder`), which builds every entry afresh, announces pauses, warns about disk and starts `berth __clone`. The clone process empties each target before copying into it (0020), so `reset` itself never deletes or recreates a volume: the volume's name, labels and place in the record stay as they were.
- **Marked first:** before that, every existing entry of a `clone` key is set to `cloning` with `reset` on it and the state saved, while the lock is held and before any volume is touched. Whatever dies after that point leaves entries `cloning` with no lock holder, which every reader recovers as `failed`; no volume reset began emptying can still read `ready`. Entries started by `reset` say "run berth reset to clone again" when recovered; `add`'s still name `add`.
- **The record keeps every volume:** nothing is ever dropped from `volumes`. A target `reset` copies into is one already listed there; one that is missing is listed and the record saved before it is created, for `add` and `reset` alike, so measuring and spawning the copy, the slow and killable steps, come after berth already knows the volume is its own. A crash at any step leaves berth knowing every volume it owns, so `remove` and the next `reset` still find it.
- **Only berth's own:** a target is emptied only when the record lists it and Docker labels it with the berth's compose project, the rule `remove` uses (0026), checked by the shared code for `add` too. Up front, a recorded volume relabelled for another project stops the whole reset before anything changes. An existing target that isn't recorded is refused and reported as failed. `reset`'s output lists as discarded only the volumes it handed to the clone process, so a refused one is never claimed.
- **Refusals before marking:** with any of the berth's compose containers up, paused or restarting, with a clone process still running, or with Docker unreachable, `reset` exits `1` naming why and changes nothing. The running check (`refuseRunning`) comes first and covers every berth with a compose file, whatever state it holds, so a berth with only host databases, Redis keys or SQLite files is never reset under a running app. Docker unreachable refuses as it does for `remove` and `prune --idle`: the same failure covers a stopped daemon, a `docker` that isn't on `PATH`, a `DOCKER_HOST` pointing elsewhere and a timeout, and only the first means nothing is running. A running clone is not killed: the user may be waiting on it.
- **Main without the volume:** when main has no such volume, or its compose project no longer declares one, a target that already exists is emptied by the clone process with only the target mounted, so main's volume is never created, and reported `ready` with `source_missing`. This applies to `add` retrying a failed clone too, which used to leave the old contents in place.
- **Dry run:** `-n`/`--dry-run` makes the same checks and lists the recorded targets that would be emptied, without taking the clone lock.

## Options considered
- Removing and recreating each volume: a crash between the two would leave a recorded volume gone, and recreating it means copying main's labels again; the clone process already empties targets.
- Dropping targets from the record before cloning and adding them back after: a crash in between would leave berth forgetting a volume it owns, so neither `remove` nor `reset` would touch it again.
- Teaching the shared code to redo `ready` entries behind a flag: the same result, but the rule that no entry reads `ready` once a reset has begun would rest on the order of writes deep in code `add` also changes, rather than on one save at the top of `reset`.
- Stopping the berth's containers: berth never stops or starts the user's services.
- Killing a running clone, as `remove` does: `remove` throws the data away, while `reset` would replace a copy the user may be waiting on.
- Checking running containers only for the kinds of state berth holds in Docker: a compose app that talks to a host database would be reset while it runs.
- Letting `reset` pass when Docker is unreachable, with or without a warning: it saves starting Docker to reset a host database, at the price of dropping one under a running app whenever the check fails for another reason.

## Evidence
`e2e/reset_test.go` covers a reset matching main at reset time, keeping ports, names and the env block, leaving the source unchanged, announcing the source's pause, refusing while containers run, sparing unrecorded volumes, the dry run and returning while the copy runs. It also kills a reset with `kill -9` mid-copy: the volume never reads `ready`, it reads `failed` naming `berth reset`, and `reset` then gives main's checksum. `e2e/reset_source_test.go` and `e2e/reset_record_test.go` cover a volume main no longer has or no longer declares, and `kill -9` while `add` measures the copy, held there by a `docker` shim on `PATH`: `remove -n` lists the volume and `reset` then gives main's checksum.

`e2e/teardown_gaps_test.go`: a berth with only a SQLite file is refused `reset` while a container is up, and again when Docker is unreachable, with the file untouched, then reset once stopped.

## Undoing it
`runReset` and `refuseRunning` are in `internal/cli/reset.go`; `lockClones` and `cloneUnder` in `internal/cli/volumes.go`; the `reset` flag on entries, `empty` and the recovery text in `internal/volume`. Reverting leaves entries with `reset` set, which older code ignores.
