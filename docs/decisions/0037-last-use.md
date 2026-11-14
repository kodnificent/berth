# 0037. Last use: a file per berth, git status for worktree changes, one Docker call

Status: accepted; amended by [0039](0039-pruning-idle-berths.md) for `prune --idle`

## Context
`berth list` shows when each berth was last used: the latest of a berth command run on it, one of its containers running, and a change in its worktree (design, Idle berths). For disk use, `list` reads only the registry and recorded state and never asks Docker (0022), so it returns at once however large a worktree is. Two of the three signals can't be recorded ahead of time by berth: containers start and files change without berth running. This record has `list` ask for those two, and keeps disk use as 0022 decided.

## Decision
- **Where it is kept.** A berth command's use is written to `lastuse/<id>` in berth's state directory, one RFC 3339 time per file, never into the registry record, so recording a use never rewrites the registry. It is written while the command holds the registry lock with the berth's record found, and teardown deletes it under the same lock, so a use is never recorded for a berth that doesn't exist or is being removed. Nothing here creates a berth.
- **What counts.** `status`, `env`, `add`, `exec` (before it replaces the process), `reset` and `doctor` run on the berth. `list` doesn't, and no berth command does while `BERTH_PATH` is set, inside berth's own hooks, so `pre-remove` can't refresh the berth it is removing. A berth with no file, as one made before last use was recorded, counts from its `created_at`.
- **Worktree changes.** For each live berth, `list` runs `git --no-optional-locks status --porcelain=v1 -z --untracked-files=all --no-renames` and takes the latest modification time of the paths it reports (a deleted path's nearest existing directory), and of the worktree's `index` in git's administrative directory, which staging moves, and the time on the newest entry of its `logs/HEAD`, which a commit or checkout adds. The reflog's own modification time is not used: `git gc` and `git reflog expire --all`, run anywhere in the repository, rewrite every worktree's reflog; neither touches a worktree's index. Neither time is capped by another: the newest entry counts at the time it records whatever the reflog file's time says, and an untracked symlink at its own time, never its target's or its directory's. A cap could only make last use earlier than the truth, and `prune --idle` (0039) deletes on it, so every signal errs toward more recent use. git compares tracked files against the stat data its index caches and never enters ignored directories, so dependency trees cost nothing and berth itself stats only the handful of paths git reports. `--no-optional-locks` keeps git from refreshing the index, which would itself look like a change on the next `list`. At most four worktrees are checked at once, all within 1.5 s; a worktree not checked in time keeps its other signals and `list` names it in one warning on stderr.
- **Running containers.** One `docker ps --filter status=running` per `list`, grouped by the `com.docker.compose.project` label, bounded at 3 s and run alongside the git checks. A berth whose compose project has a running container is `in_use: true` and last used at the time of the `list`. When Docker can't be reached, `list` still answers with `in_use: null` for every berth, warns once on stderr, and exits 0.
- `last_used` is reported to the second, as `created_at` is.

## Options considered
- **A field in the registry record:** every `status` and `env` would rewrite the record, racing other writers and churning a file meant to change only when the berth's allocation does.
- **Walking the worktree and stat'ing every file:** 100,000 files a worktree is a walk per berth per `list`; 0022 keeps `list` off that path for this reason.
- **Directory modification times only, skipping ignored directories:** catches creating, deleting and atomic-rename saves, but an edit in place leaves its directory's time alone, so it misses the most common change.
- **A file watcher or git's fsmonitor daemon:** berth runs nothing long-lived. A user who enables `core.fsmonitor` gets it anyway, since berth runs their git.
- **A `docker ps` per berth:** ten berths would be ten Docker round trips; one call answers for all.
- **Checking every worktree at once:** measured below, ten heavy worktrees at once all miss the bound, where four at a time finish four.

## Evidence
Apple M3 Pro, APFS, git 2.50.1, Docker 29.8.1.

- 10 berths with 100,000 ignored files each (the e2e test behind `BERTH_E2E_LARGE=1`): `list --json` took 370 ms.
- `docker ps --filter status=running`: 40 to 50 ms.
- One worktree with 100,000 tracked files: `git status` as above 156 ms once git's index is fresh; 370 ms to 1.1 s right after `git worktree add`, while every file is racily clean and git rehashes it. Ten such worktrees at once took 3.3 to 3.9 s, and one after another 4.3 s: a million `lstat`s outrun the kernel's vnode cache. With the 1.5 s bound, four at a time checked 4 of 10; ten at a time checked none.
- The e2e tests cover each signal moving last use (a created file, an edited file, a commit, a berth command, a running container), berth commands inside hooks leaving it alone, `git gc` in main leaving it alone, a berth with no recorded use counting from its creation, recording a use leaving the registry unchanged, `remove` and `prune` leaving no last-use file, and `list` without Docker. The e2e tests that age a berth rewrite the times in its HEAD log's entries and set a symlink's own time back without following it, so nothing in berth has to trust a time set back on a file.

Not covered: a change git doesn't report goes unseen: an edit inside an ignored directory, or an edit that puts a file back as committed. Worktrees with tens of thousands of tracked files each, several at once, can miss the bound; `list` then warns and shows their last use without worktree changes, which may be earlier than the truth.

## Undoing it
`usage` in `internal/cli/lastused.go` combines the signals and `noteUse` records a command's use; `internal/lastuse` keeps the files, `worktree.LastChange` checks a worktree and `docker.RunningProjects` asks Docker. Going back means dropping `usage` from `runList`; the `lastuse` directory can be deleted with no effect on anything else.
