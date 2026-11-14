# 0024. The post-checkout hook

Status: accepted; where the hook goes when `core.hooksPath` is set, and the line printed for someone else's hook, are in [0033](0033-hooks-path.md)

## Context
`berth hook install` makes every `git worktree add` create a berth, whatever tool runs git (design, Lifecycle). Git has no hook for worktree creation itself: `post-checkout` fires on `git worktree add` with an all-zero previous HEAD and doesn't fire with `--no-checkout` (spike). It also fires on every `git checkout` and `git switch`, which must not run `add`. A repository with `core.hooksPath` runs hooks from that directory instead, usually committed files.

## Decision
- **Where:** with no `core.hooksPath`, the hook is `<common git dir>/hooks/post-checkout`, which every linked worktree shares and git never commits. With one set, 0033 decides.
- **When it acts:** the script exits at once unless its first argument (the previous HEAD) is all zeros, which matches SHA-1 and SHA-256 repositories, and unless `.git` in its working directory is a file, which is true only in a linked worktree. So a normal checkout never runs berth, and neither does a first checkout in a main worktree.
- **What it runs:** `berth add` with stdin from `/dev/null`, in the new worktree, which is where git runs the hook. `add` already returns while clones run, and the processes it starts write to their own logs, so git's output pipes close when `add` exits.
- **Which berth:** the absolute path of the binary that ran `install`, falling back to `berth` on `PATH` if that file is gone, so the hook works under GUI tools with a minimal `PATH` and survives a reinstall elsewhere.
- **Failure:** the script always exits `0`, since git makes a hook's exit status its own. A failed `add` prints a line saying the worktree is fine and how to retry.
- **Ownership:** the script carries a marker line. `install` rewrites a hook with the marker (a new binary path, say) and leaves an identical one alone; it refuses to replace a `post-checkout` hook without the marker, printing the line to add to it (0033). `-n`/`--dry-run` reports what it would write.
- **Uninstall:** `berth hook uninstall [path]` deletes the hook only when it is exactly the script `install` writes for the berth path it names, so a hook from an older or moved binary still counts. A hook without berth's marker is refused as someone else's, and one with the marker but other changes is refused as edited, both exit `1` with the file untouched. No hook is success with nothing to do. `-n`/`--dry-run` and `--json` (`{"hook", "changed", "dry_run"}`) work as for `install`. Which folder it looks in is in 0033.

## Options considered
- Chaining onto an existing hook automatically: rewriting someone else's script can break it in ways berth can't test; printing the line keeps them in control.
- `berth` from `PATH` only: Git GUIs and agent harnesses often run hooks with a reduced `PATH`.
- Letting the hook's exit follow `add`: a config typo would make `git worktree add` fail in tools that then discard the worktree.
- Checking `git rev-parse --git-dir` against `--git-common-dir`: needs `--path-format=absolute` (git 2.31) to compare reliably, where the `.git` file test needs nothing.
- Uninstall removing any hook with berth's marker: deletes lines a user added to berth's hook.
- Uninstall removing only berth's lines from an edited hook: rewriting a user's script is the edit berth refuses to make on install.

## Evidence
`e2e/hook_test.go` covers: `git worktree add` creating a berth; `git checkout` and `git switch` not running `add`; the hook and a manual `add` giving one berth; git returning within seconds while a 15 s clone runs, its output read through pipes; and no berth created after `uninstall`. `internal/hook` tests run the script with a stand-in berth for each previous HEAD and worktree kind, and cover uninstall's dry run, removal, idempotence and both refusals. Spike: `post-checkout` fires on `git worktree add` with an all-zero previous HEAD and doesn't fire with `--no-checkout`.

## Undoing it
The script, `Install`, `Uninstall` and `written` are in `internal/hook/githook.go`; the commands are `runHook` and `runHookUninstall` in `internal/cli/hooks.go`. Deleting `.git/hooks/post-checkout` removes the hook from a repository.
