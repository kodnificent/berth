---
title: Hooks
description: Run your own commands after a berth is created or before it is removed.
sidebar:
  order: 6
---

## `post-add` and `pre-remove`

Berth copies files and data. It doesn't install dependencies, run migrations or seed anything: that is what your hooks are for.

```toml
[hooks]
post-add = "composer install && php artisan migrate"
pre-remove = "./scripts/archive-uploads.sh"
```

Each is one shell command, run with `sh -c` in the worktree. For anything longer, point it at a script the repository tracks, such as `post-add = "./scripts/post-add.sh"`, so every worktree has it.

**`post-add`** runs once per berth, after `berth add` and after every volume and database copy has finished, so it can migrate the copy rather than an empty database. It runs in the background: `berth add` returns without waiting for it, `add --wait` included.

```text
$ berth add
berth: cloning volume mysql in the background; berth status shows how far it is
berth: running post-add in the background once every clone is ready; berth status shows how it went
Added berth 9f2c4e1ab07d3355 for /Users/you/shop/.worktrees/fix-checkout
name            fix-checkout
url             http://fix-checkout.localhost
volume mysql    cloning
post-add        pending
next            volume mysql is still cloning and post-add is still running; run berth status --wait before starting the app
```

`berth status --wait` returns once the copies and `post-add` have finished. Wait for it before you start the app.

Once `post-add` has succeeded, running `berth add` again does not run it a second time, and neither does `berth reset`. A run that failed, was skipped or was interrupted is run again by the next `berth add`.

**`pre-remove`** runs in the foreground before `berth remove` deletes anything, with its output in your terminal. If it exits non-zero, nothing is removed. `berth remove --dry-run` doesn't run it. `berth prune --idle` runs it for each idle berth it removes; plain `berth prune` doesn't, since an orphan's worktree is already gone.

Hooks are your own code and run as you, with your login. On Windows they run with the `sh` that Git for Windows installs, so write them as POSIX shell.

A hook set in `berth.toml` is the team's. To change it for yourself, set the same key in your [personal config](/docs/guides/private-setup/); an empty string, `post-add = ""`, turns it off.

## Variables berth sets for hooks

Both hooks get the environment `berth` itself was run in, plus three variables:

| Variable | Holds |
| --- | --- |
| `BERTH_PATH` | The worktree's absolute path. The hook's working directory is the same place |
| `BERTH_SOURCE` | Your main checkout's absolute path |
| `BERTH_CHANGED_FILES` | Tracked files that may differ between main and the worktree, one path per line, relative to the top of the repository |

The worktree's own values, such as its ports and URL, are not exported to the hook. They are in the worktree's `.env`, which most tools read on their own. For a tool that doesn't, run it through berth inside the hook:

```sh
berth exec -- ./bin/seed
```

Don't call `berth status --wait` from `post-add`. It can't wait for the hook it is running in, and it exits `1` because `post-add` hasn't finished.

Berth removes the variables git sets for its own hooks, such as `GIT_DIR`, before it runs yours, so a `git` command in a hook acts on the worktree even when `berth add` was started by `git worktree add`.

## Skipping installs when lockfiles match

When `[files] clone` copies `vendor/` or `node_modules/` from main, the copy already matches main's lockfile. An install is only needed when the worktree's lockfile differs, and `BERTH_CHANGED_FILES` tells you:

```sh
#!/bin/sh
set -e
changed() { printf '%s\n' "$BERTH_CHANGED_FILES" | grep -qx "$1"; }

if changed composer.lock; then composer install; fi
if changed package-lock.json; then npm ci; fi
php artisan migrate
```

The list holds every tracked file the worktree changes from main's last commit, and every tracked file main has changed since that commit without committing. So it also catches a lockfile you edited in main and didn't commit, which is what the copied `vendor/` matches. A file that is listed may still turn out to match; a tracked file that isn't listed always matches. Untracked files are never listed. The list is worked out when `berth add` schedules the hook, and is empty when nothing differs.

## When a hook fails

A failed `post-add` doesn't undo the berth. Its ports, names and files are in place, and `berth status` shows what happened: the exit code, the last 20 lines of output, and the log holding all of it.

```text
post-add         failed (exit 1)
                 | Installing dependencies from lock file
                 | Your lock file does not contain a compatible set of packages.
post-add log     /Users/you/.local/state/berth/hooks/9f2c4e1ab07d3355.post-add.log
```

| State | Meaning |
| --- | --- |
| `pending` | Waiting for the copies to finish, or about to start |
| `running` | The command is running |
| `ok` | It exited `0` |
| `failed` | It exited non-zero, couldn't be started, or was interrupted by a crash, sleep or reboot |
| `skipped` | A volume or database copy it waited for failed, so it never ran. The row says which |

`berth status --wait` exits `1` when `post-add` failed or was skipped, and `4` when a copy failed. With `--json`, the same details are under `post_add`: `command`, `status`, `exit_code`, `output`, `error` and `log`.

Fix the cause and run `berth add` again. It keeps the berth, retries any failed copy, then runs `post-add` again.

When `pre-remove` fails, the removal stops before anything is deleted:

```text
berth: pre-remove failed (exit status 1), so nothing was removed; fix [hooks] pre-remove or take it out, and run berth remove again
```

## The git hook: `berth hook install`

By default a worktree gets a berth when you run `berth add` in it. If you want one for every worktree the moment it is created, install berth's git hook:

```text
$ berth hook install
Installed the post-checkout hook at /Users/you/shop/.git/hooks/post-checkout; git worktree add now creates a berth
```

From then on `git worktree add` runs `berth add` in the new worktree and prints its summary, whichever tool created the worktree. This is opt-in for a reason: every worktree then holds copies of your data and a share of the port pool, including worktrees that only ever edit code.

- The hook acts only on `git worktree add`. An ordinary `git checkout` or `git switch` does nothing.
- It never fails git. If `berth add` fails, the worktree is still created and the hook prints `berth: berth add failed in the post-checkout hook; the worktree is fine, run berth add in it to retry`.
- It calls the `berth` binary that installed it, and falls back to the one on your `PATH` if that has moved.
- It lives in git's own hooks folder, normally `.git/hooks`, so it is yours alone and is never committed.
- If a `post-checkout` hook berth didn't write is already there, berth leaves it alone, exits `1` and prints the line to add to it yourself.

`berth hook install --dry-run` says what it would do. `berth hook uninstall` removes the hook, but only one berth wrote and nobody has edited since; anything else it leaves and names. [`berth doctor`](/docs/guides/troubleshooting/#the-git-hook-no-longer-runs) warns when a hook you installed has stopped running.

## Repos that use `core.hooksPath`

`core.hooksPath` tells git to run hooks from another folder. Where that folder is git's own `.git/hooks`, or one outside the repository, `berth hook install` puts its hook there as usual.

Where it is a folder inside your checkout, as husky and `.githooks/` setups do, those files belong to the repository, usually committed and shared, and berth never writes into them. `berth hook install` exits `1` and prints the line to add to that folder's `post-checkout`:

```sh
if command -v berth >/dev/null 2>&1; then berth hook run "$@" || true; fi
```

If there is no `post-checkout` there yet, create it with `#!/bin/sh` as its first line and make it executable. The line finds berth on your `PATH`, and never fails a checkout, even for anyone who hasn't installed berth or has a version without `hook run`, so it is safe to commit. `berth hook run` acts only on `git worktree add`, never on a normal checkout.
