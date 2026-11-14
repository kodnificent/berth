# 0025. Running post-add and pre-remove

Status: accepted

## Context
`post-add` runs after cloning finishes, with `BERTH_PATH`, `BERTH_SOURCE` and `BERTH_CHANGED_FILES` set so it can skip work when lockfiles match; `pre-remove` runs before teardown (design, Lifecycle). Clones run in a background process after `add` returns (0021), and `add` doesn't wait for `post-add`. It must run once per berth, even when the hook and a manual `add` both run, and a failure must show in `status` without making the berth unusable.

## Decision
- **Process:** when `[hooks] post-add` is set and it hasn't succeeded for this berth, `add` writes the post-add state as `pending` and starts `berth __post-add <id>` in its own session, after starting any clone process. The process waits for the clone lock to be free, then runs the command if every clone volume that had a clone entry when it was scheduled is `ready`; otherwise it records `skipped` with the reason. A clone volume berth couldn't start cloning at all (main has no such volume, say) has no entry and was already warned about, so it doesn't hold `post-add` back. State and output are `<state dir>/hooks/<id>.post-add.json` and `.log`.
- **Once:** a per-berth lock, `<id>.post-add.lock`, works as the clone lock does (0021): `add` takes it before writing `pending` and passes it to the process as fd 3, which holds it until the command ends. `add` schedules nothing while the lock is held, nor after a run that ended `ok`. A `failed`, `skipped` or interrupted run is scheduled again by the next `add`, after its clones are retried. The lock fd is close-on-exec, so a daemon the command leaves behind can't keep it held.
- **Recovery:** `status` and `add` take the lock when free; a state still `pending` or `running` then means its process died, and it is reported `failed` as interrupted.
- **Command:** `sh -c <command>` in the worktree, stdin from `/dev/null`, stdout and stderr together into the log. Git's hook variables (`GIT_DIR`, `GIT_INDEX_FILE` and the like) are removed so the command acts on the worktree it runs in. `status` reports `pending`, `running`, `ok`, `failed` or `skipped`, the exit code, and the last 20 lines (at most 4 KB) of output, with the log's path.
- **`BERTH_CHANGED_FILES`:** newline-separated paths, relative to the repository root, of tracked files the berth's working tree changes from the source's HEAD plus those the source's working tree changes from its HEAD, worked out when `add` schedules the run. A listed file may still match; an unlisted tracked file always does. Untracked files aren't listed.
- **`pre-remove`:** `hook.PreRemove` runs the command in the foreground in the worktree with the same variables and returns an error on a non-zero exit. `berth remove` calls it before deleting anything (0026).

## Options considered
- Running `post-add` inside the clone process: `add --wait` waits on the clone lock and would then wait for `post-add` too, and a berth without clones would need a second path anyway.
- Running `post-add` in `add` when nothing clones: `add` from the hook would then block `git worktree add` on a dependency install.
- A pid file to tell whether post-add is still running: the same pid reuse and heartbeat problems 0021 rejected.
- Rerunning `post-add` on every `add`: the hook and a manual `add` would run it twice, and `add` would stop being idempotent.
- `BERTH_CHANGED_FILES` from `git diff` between the two HEADs only: misses uncommitted lockfile changes in main, which is what the cloned `vendor/` matches.

## Evidence
`e2e/hook_test.go` covers: the command sees every clone ready, runs once in the worktree and gets the three variables; a failing command shows exit `3` and the tail in `status` while `env` and compose still work; and one run across the hook and two adds, one of them while it ran. `internal/hook` tests cover the state, the lock, the tail and the environment.

## Undoing it
`schedulePostAdd` and `runPostAdd` are in `internal/cli/hooks.go`; state, lock and execution in `internal/hook/postadd.go`. Deleting `<state dir>/hooks` loses only post-add history: the next `add` runs `post-add` again.
