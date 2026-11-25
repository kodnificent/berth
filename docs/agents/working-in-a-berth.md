# Working in a berth

You are in a git worktree. Berth gives a worktree its own ports, names, files and data, so the repository's usual commands (`docker compose up`, the dev server, the test runner) work here unchanged and act only on this worktree. A worktree has no berth until `berth add` runs in it.

## Start here

- Run `berth status --json` first, before you start the app, run its tests or use any URL. It reports this worktree's URL, host, ports, compose project, managed env keys, the files berth wrote and how far each clone has got.
- Read `next` in the output and do what it says. Each entry is one hint: wait for a clone, use the berth's URL, or run `berth add`.
- With no berth, `status` exits `1` with error code `no_berth`, and `error.next` says to run `berth add` before starting the app.
- In the main worktree, `status` exits `1` with `main_worktree`. Main is the source berths are cloned from; berths live in linked worktrees.

## Making the berth

- Run `berth add` only when you are about to start the app or run its tests. Reading code, editing it and running linters need no berth, and every berth holds copies of the data and a share of the port pool.
- `berth add` returns in seconds and slow clones carry on in the background. Running it again keeps the berth the worktree already has.
- If `berth add` exits `1` with error code `compose_too_old`, this machine's docker compose is older than 2.24.4 and would run the worktree on main's ports. Berth made nothing. Don't start the app with compose here; tell the user what `error.next` says.
- `berth add` ends with a summary: the berth's name, its URL, the ports berth assigned, each volume, database and SQLite file with its state, and the same `next` hints `status` gives. Read the URL and ports from it, or from `berth status --json`.
- `berth add` prints how it copied each cloned path, such as `vendor` or `node_modules`: `cow` shares main's blocks and takes a moment, `copy` is a full copy.
- While a volume or database is still cloning, run `berth status --wait` before you start the app or its tests. It returns once every clone and `post-add` have finished, and exits `4` if a clone failed or `1` if `post-add` did.

## Running the app

- Use the berth's URL and ports from `berth status`, never `localhost:8000` or the ports main uses. Main and other worktrees may be running on those.
- Start services with the repository's own commands: `docker compose up`, `docker compose exec`, the dev server, the tests. They read the `.env` and compose files berth wrote.
- Never pass `-p` or `--project-name` to `docker compose`. The managed block sets `COMPOSE_PROJECT_NAME` and `COMPOSE_FILE`; overriding them points compose at another worktree's containers and data.
- If `status` lists a shadowed key, a variable exported in your shell is overriding the berth's value. Unset it before running compose or the app.
- If the berth's data is broken or stale, stop its containers and run `berth reset`. It copies the `clone` volumes, `dump` and `template` databases and `[state.sqlite]` files again from main, restores `snapshot` databases from the current snapshot, reloads seed files, reruns `command` databases' scripts, empties `fresh` databases, deletes the keys under the berth's Redis prefix, and keeps ports, names and `.env`; `berth status --wait` waits for the copy.
- A `snapshot` database is as old as main's snapshot, and `berth status` says how old. Taking a new one is `berth snapshot` in the main worktree, which needs doing only if you were asked for current data; `berth reset` then picks it up, and refuses, exit `1`, dropping nothing, while a database has no snapshot. Running `berth snapshot` in a berth exits `1`.
- For a tool that ignores `.env`, run it as `berth exec -- <command>`, or load the variables into your shell with `eval "$(berth env)"`. `exec` exits with the command's own code; with `--json`, berth's own failures print the error envelope and the command's failures print nothing extra.

## What not to touch

- Never edit the managed block in `.env`, the lines between `# >>> berth (managed, do not edit)` and `# <<< berth`. Berth rewrites it. Edit `.env` outside the block, which berth never touches, or change the config in the main worktree.
- Don't commit the files berth writes, such as `compose.berth.yml` and cloned dependency directories. Berth keeps them out of `git status`.
- Never remove a berth you did not make. `berth remove` and `berth prune` delete cloned databases, volumes and files. Run `berth remove` only in a worktree whose berth you created, when its work is done or the user asks, and `berth remove --dry-run` first to see what it deletes.
- Never run `FLUSHDB` or `FLUSHALL` on a Redis server the berth shares with main through a key prefix (`REDIS_PREFIX` in the managed block). They clear main's keys and every other berth's, not just this berth's.
- Never run `berth prune --idle` unless the user asks. It removes the berths of every worktree on the machine unused for that long, other agents' included.

## Reading berth's output

- Pass `--json` to any command and read stdout. A failure prints `{"error": {"code", "message", "next"}}` there instead.
- Exit codes are stable: `0` success, `1` error, `2` no free ports, `3` worktree not found, `4` a clone failed, `5` config invalid.
- `berth doctor` in the worktree reports problems such as values still pointing at main, shadowed variables and ports another process holds.
- Once the app runs, `berth doctor --probe` also requests it and reports cookies shared with other worktrees and redirects to main. A finding in `berth doctor --probe --json` with a `fix` carries config to merge into the config in the main worktree; apply it as given, then run `berth add` again. Without a `fix`, don't guess: a message naming several keys leaves the choice to the user.
- When the user runs `berth proxy`, plain `berth doctor` also reports the wide cookies the proxy saw the app set, as `cookie_domain` findings, with no request made. Treat them as the probe's. `[proxy] cookies = "rewrite"` hides the problem only behind the proxy; fixing the app's cookie domain is the cure. Never run `berth proxy trust` or `untrust` yourself: they change the system's trust store and raise the system's prompt.
- `berth list` shows every berth on the machine; `berth <command> --help` prints a command's usage.
