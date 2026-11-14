# 0033. core.hooksPath, berth hook run, and knowing a hook was installed

Status: accepted

## Context
0024 refused `berth hook install` whenever `core.hooksPath` was set, on the reasoning that the folder it names holds committed hooks (husky, `.githooks/`). That also refused repositories whose setting just names git's own private folder, such as `core.hooksPath = /path/to/repo/.git/hooks`, or a personal hooks folder outside the repository. What the design rules out is berth writing into the repository's own files, not the setting itself. Doctor also had no way to say the hook had stopped working.

## Decision
This supersedes where the hook goes in 0024. The rest of 0024 (when the script acts, what it runs, failure, ownership) stands.

- **Which folder:** git's hooks folder is `<common git dir>/hooks` when `core.hooksPath` is unset. When it is set (read with `git config --type=path`, so `~` expands as git expands it), a relative value is joined to the main worktree, and symlinks are resolved in the longest part of the path that exists, following a link whose target doesn't exist yet. Git resolves a relative value against the worktree it runs in, and the `git worktree add` that matters is the one run from main; from a linked worktree, `.git` is a file and a relative `.git/hooks` finds nothing.
- **Install there** when that folder is inside the common git directory or this worktree's git directory, or outside every work tree `git worktree list` names. A folder outside the repository may be a personal folder that every repository uses, set in `~/.gitconfig`; berth installs there as it would in `.git/hooks`, and its hook in another repository runs `berth add`, which fails without disturbing git.
- **Refuse** when the folder is inside any of the repository's work trees and outside its git directory, whether or not it is tracked and whether or not it already has a `post-checkout`. `install` exits `1` with the existing `error` code, writes nothing, names `core.hooksPath`, the file that set it and the resolved folder, and prints the line to add (or, when there is no `post-checkout` yet, the two lines to create it with):

  ```sh
  if command -v berth >/dev/null 2>&1; then berth hook run "$@" || true; fi
  ```

  The line finds berth on `PATH` instead of naming the installing binary, because such a folder is usually shared with everyone who clones the repository, and can never fail a teammate's checkout: it does nothing where berth isn't installed, and `|| true` covers an older berth without `hook run`, which exits with a usage error that git would otherwise make the checkout's own exit status. The same line now replaces the `'<berth>' add` line 0024 printed for someone else's hook in a private folder, which ran `add` on every checkout.
- **`berth hook run`** takes post-checkout's arguments and does what the installed script does: it returns at once unless the previous HEAD is all zeros and `.git` in the working directory is a regular file, then runs `add` in process, and always exits `0`, printing the retry line when `add` fails. With no arguments it is a usage error. The installed script is unchanged, so hooks written by earlier versions still compare equal to `Script` for `uninstall` and doctor.
- **Uninstall** uses the same folder: it removes berth's own hook there as 0024 describes, and in a work-tree folder exits `1` naming it and telling the user to delete the `berth hook run` line themself. No hook is still success.
- **Knowing it was installed:** `install` (including one that finds the hook already in place) writes `<state dir>/hooks/<first 16 hex digits of sha256(common dir)>.json` holding `{"common_dir", "hook"}`; `uninstall` deletes it, including when there was no hook to remove. Doctor treats a repository as having installed the hook when that record exists or the effective hook carries berth's marker, so hooks installed before this record existed are still checked when edited.
- **Doctor** reports a `hook` warning, in main and in a berth, when berth installed the hook and: the hook is gone; `core.hooksPath` now points elsewhere and the new folder has no hook; the hook there no longer has berth's marker; it has the marker but isn't exactly `Script` for some berth path; or the folder is now inside the work tree and its `post-checkout` doesn't call `berth hook run`, in which case the warning carries the line to add. Warnings never fail doctor (0030).

## Options considered
- Keep refusing whenever `core.hooksPath` is set: refuses repositories where nothing committed is involved, which is the bug that prompted this.
- Write into the work-tree folder and keep the file out of git with `.git/info/exclude`: berth never writes into the repository's own files, tracked or not.
- Compare the folder with `git rev-parse --git-path hooks`: it returns the setting as written, relative to wherever it runs, so it still needs the same resolving.
- Print a line that calls the installing binary by absolute path: right for one machine, wrong for a committed hook every teammate runs.
- Change the installed script to call `berth hook run`: every hook installed by an earlier berth would then read as edited, and `uninstall` would refuse it.
- Record the install in the repository's `.git/config` (`berth.hook`): edits the user's git config for berth's own bookkeeping, where the state directory is berth's already.
- Warn whenever the effective folder lacks berth's hook: every repository that never installed it would warn.

## Evidence
`e2e/hookspath_test.go` covers three cases. Where git runs private hooks (unset; `.git/hooks` absolute and relative; a folder outside the repository; a symlink outside the repository to `.git/hooks`): install lands in the resolved folder, the work tree is unchanged, `git worktree add` creates a berth, doctor is quiet before install and after, uninstall removes it and a second uninstall says there is none. A folder inside the work tree (`.husky` with and without a committed hook, an absolute untracked `.githooks`, a symlink outside the repository into the work tree): install, dry run and uninstall exit `1` naming the folder, install prints the line, nothing changes; with the printed line added, `git switch` and `git checkout` succeed, also with an older berth on `PATH` that rejects `hook run`, and `git worktree add` makes exactly one berth. Doctor's warning: for a hook deleted, edited, replaced or bypassed by `core.hooksPath`, quiet again once restored or once `.husky` calls `berth hook run`, and quiet after uninstall. Unit tests in `internal/hook` classify each setting from main and from a linked worktree.

A spike with git 2.50 showed a relative `core.hooksPath = .git/hooks` runs the hook for `git worktree add` from main and not from a linked worktree.

## Undoing it
`HooksDir`, `Problem` and the record helpers in `internal/hook/githook.go`; `runHookRun` in `internal/cli/hooks.go`; `hookFindings` in `internal/cli/doctor.go`. Restoring the `core.hooksPath` check at the top of `Install` brings back 0024's refusal; deleting `<state dir>/hooks/` makes doctor forget every install.
