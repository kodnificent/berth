# 0029. status --wait and clone progress

Status: accepted

## Context
The design's agent guidance tells agents to run `berth status --wait` while state is still cloning, and `status` shows clone progress (design, State cloning and Agent guidance). Before this record `status` showed a cloning volume as `cloning` with no figure, and `add --wait` waits for clones only (0021), not for `post-add` (0025), so polling `status --json` was the only way to wait for `post-add`.

## Decision
- **`status --wait`:** before reading the berth's state, `status` blocks on the clone lock (0021), then on the post-add lock (0025), then prints status as usual. Clones go first because post-add holds its lock from before the clones it waits for end. A clone volume that isn't ready makes it exit `4`, as `add --wait` does; a `post-add` that failed or was skipped makes it exit `1`. With `--json` the status object then also carries `error: {code, message}`, so 0002's envelope rule holds. Run from inside a berth hook (`BERTH_PATH` is set), it doesn't wait: berth holds those locks for the hook, so the wait could never end. `add --wait` is unchanged.
- **Progress:** the size of each source volume, which `add` already measures for its disk warning (0022), is recorded on the clone entry as `total`. While the copy runs, the clone process runs `du -sk` on the berth's staging directory in its helper every 2 seconds and records it as `copied`. `status --json` reports `progress` (`copied / total`, at most `0.99` while cloning, since the source may grow), `copied_bytes` and `total_bytes` for a cloning entry; the human output reads `cloning (62%, 680 MB of 1.1 GB)`. When the size couldn't be measured, there is no progress figure.
- **Test delay:** `BERTH_TEST_CLONE_DELAY` makes the clone process sleep at the end of each copy, inside the pause, so a test sees a copy in progress with its data in place without a volume too large for the test machine's disk.

## Options considered
- Counting bytes as `tar` streams them: busybox has no `pv` or tar checkpoints, and a counting stage in `sh` is far slower than the copy.
- Measuring the source again in the clone process: `add` already has the figure, and measuring a large `node_modules` twice costs seconds.
- `status --wait` waiting on the post-add lock only: when a berth has clones but no `post-add`, nothing holds it, so the wait would return while cloning.
- Making `add --wait` wait for `post-add` too: changes a settled exit-code contract (0021) and makes the hook's `add` slower for people who don't want to wait.

## Evidence
`e2e/volumes_test.go` covers progress and bytes while cloning, the human `cloning (N%, … of …)` line, and `ready` after `status --wait`. `e2e/hook_test.go` covers `status --wait` returning only once a 4 s clone and a 3 s `post-add` finish, and exit `1` with the status and an `error` when `post-add` fails. `internal/volume` tests cover the progress figure.

## Undoing it
`waitSettled` and `unsettled` in `internal/cli/berths.go`; `measure` and `progressScript` in `internal/volume/clone.go`; `Entry.Total`, `Entry.Copied` and `Progress` in `internal/volume/state.go`. The `progress`, `copied_bytes`, `total_bytes` and `error` fields are part of the `--json` schema.
