# 0022. Disk warnings before a clone, and disk use in list

Status: accepted; `list` asks git and Docker about last use, in [0037](0037-last-use.md), and disk use stays as decided here

## Context
A clone can fill a disk that is already nearly full, so `add` warns before it would (design, Removal and pruning). On Docker Desktop, volumes live in a VM disk image that grows on the host as it fills, so the free space inside the VM overstates what is really left.

`berth list` shows each berth's disk use, orphans included, so leftover state is always visible (design, Removal and pruning). Measuring it on every call means walking every path the berth created in its worktree and running `du` over its volumes in a helper container: with `vendor` and `node_modules` cloned, one berth made `list` take about 9 s, 3.5 s of it system time walking hundreds of thousands of files. Those clones are copy-on-write, so the apparent size a walk adds up overstates the disk they take anyway.

## Decision
- **Threshold:** `[clone] min_free`, a size with a unit (`"10GB"`, `"512MiB"`), `10GB` by default. A size without a unit is refused (exit `5`), so `"10"` never means ten bytes.
- **Warning:** before starting the clone process, `add` measures each source with `du` in one read-only helper container, and the free space as the smaller of `df` inside Docker and the host disk holding Docker Desktop's data. Each copy that would leave less than `min_free`, counting the copies before it, is named on stderr with its size, what it would leave and the threshold. The clone still runs: berth warns, the user decides.
- **Disk use in `list`:** `list` reads only the registry and recorded state, and reports apparent size. The warning above is the only thing that measures.
  - A directory berth cloned into the worktree counts at its size when it was cloned, recorded in the record's `sizes`. The size is counted on the walk that repoints the clone's symlinks (0015), which `add` already does, so recording it costs one `lstat` per file and no second walk.
  - A file berth wrote or cloned, such as `.env` or the override, counts at its size now, from one `lstat`.
  - A cloned volume counts at what its clone copied, from the clone state (0021). Docker isn't asked.

## Options considered
- `docker system df -v`: it took over two minutes and didn't finish when tried.
- Free space inside the VM only: in one measurement Docker reported about 30 GB free while the host had 17 GB.
- Refusing the clone below the threshold: the design asks for a warning, and a half-full disk is still the user's call.
- Measuring disk use on every `list`: it counts a database that grew after cloning, at the cost of a `list` that slows with every berth's size.
- Measuring only on request, with a `list --disk` flag: plain `list` would stop showing orphans' disk use, which the design asks for.
- Shared versus exclusive bytes: APFS reports a file's private size per file, so it still needs a walk, and other filesystems have no cheap equivalent.
- Measuring in the background clone process: berth's own files have no clone process, and `list` would still need a place to read the result from.

## Evidence
`e2e/volumes_test.go`: no warning with `min_free = "1MB"`; with `100000TB`, a warning naming the volume and its size before the clone starts, and the clone still ends ready. `internal/bytesize` and `internal/config` tests cover the sizes.

For `list`: a berth with 20,000 cloned files reports at least their size, and a 1 GB sparse file added to the clone afterwards doesn't change what `list` reports. `list --json` took 3.7 s when it measured and 8 ms reading what was recorded. Unit tests in `internal/clone` cover the recorded sizes.

Not covered: a volume or a cloned directory that grew after cloning is reported at its cloned size.

## Undoing it
The warning is `warnDisk` in `internal/cli/volumes.go`; measuring is `Measure` in `internal/docker/volumes.go`. Dropping the key means removing `[clone]` from the config schema.

`diskUse` in `internal/cli/volumes.go` reads the recorded sizes; `Merge` and `Repoint` in `internal/clone` count them and `recordSizes` in `internal/cli/files.go` keeps them. Measuring in `diskUse` instead needs no migration; `sizes` can stay in records.
