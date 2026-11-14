# 0034. A bounded image check, outside the registry lock

Status: accepted

## Context
A berth builds only when main would, deciding is quick, and other commands never wait for an `add` copying files or comparing build inputs (design, Images and builds, and Guarantees). When a service builds with `context: .`, no `.dockerignore`, and a Dockerfile that puts a build arg in a `COPY` source, the whole repository counts as build inputs (0023). `add`, run by the post-checkout hook, then read every byte of both worktrees, large untracked files among them, for minutes, holding the machine-wide lock (0005) the whole time. `git worktree add` couldn't return, and `berth list` and every other command hung behind it.

## Decision
- **Locks:** `add` takes the registry lock only to find or create the record and hand out its names and ports, and again for a moment to save it. Everything else, from cloning env files and `[files] clone` paths to comparing build inputs and starting clones, runs under the worktree's own lock, `berth-lock` beside the marker in its git administrative directory (0004), so it moves with the worktree and exists before the berth has an id. `remove` takes the same lock before the registry lock. The order is always worktree lock, then registry lock, never the other way, so they can't deadlock. Two `add`s of one worktree still run one after the other.
- **Comparing, not digesting:** `Compare` in `internal/image` replaces the digest of 0023. It checks the build section, the Dockerfile and the ignore file first, then walks the berth's selected context files in lexical order and stops at the first difference. A path only one side has, or a different type, permissions, size or symlink target settles it without reading content. A file settles as unchanged without being read when git vouches for both copies (the same blob in both indexes, at stage 0 with no assume-unchanged or skip-worktree flag, and not named by `git diff-files`, which compares stat data and reads racily clean files) or when both copies have the same size and modification time, which a copy-on-write clone keeps. Plain copies now keep the modification time too. Only what still looks different is read and hashed. A named context that is the same directory for both builds is never read.
- **The bound:** deciding may read at most 100 MB of file content, the berth's and main's copies together, and visit at most 100,000 entries of each worktree's context, counting those it only walks past: an exclusion in the ignore file or a glob at the start of a source sends the walk through directories nothing is read from. Past either, berth doesn't share main's image for that service: the berth builds on `up`. `add` says so on stderr, naming the service and the reason, and suggests a `.dockerignore`. When the build reads its whole context, the context is the repository and no ignore file applies, the reason says so, since then the berth's own `.env` and `compose.berth.yml` are build inputs and no berth can ever match main.
- **Reporting:** what `add` found for each service goes in the record as `builds` (service, shared, reason, bytes read) and `status` shows it, as `build web  main's image` or `build web  builds on up: <reason>`. `doctor` in main warns about a whole-repository context with no `.dockerignore`.
- **In `add`, not in the background:** the check runs in `add` before it returns. The bound keeps it to about a second, so the hook stays fast, and a `docker compose up` run straight after `add` finds the tag in place.

## Options considered
- Running the check in a background process, as volume clones are: `up` right after `add` would race the tag and build, breaking the guarantee for exactly the users who never wait, and it adds another process and state to recover.
- Keeping the digest and only moving it out of the lock: other commands stop hanging, but `git worktree add` still waits minutes.
- Comparing content only through git for tracked files: untracked and ignored files, such as `vendor` and `node_modules`, are most of a large context and git says nothing about them.
- Recognising clones by shared extents: there is no portable way to ask, and a clone's mtime already says the same.
- A bound in time rather than bytes and files: it would decide differently on a busy or idle machine.
- A lock per berth id: a new berth has no id until the registry lock assigns one.

## Evidence
On a fixture with a 310 MB context (three 100 MB files and 10,000 small ones under `[files] clone`), `berth add` took 4.6 s with the digest and 1.8 s with `Compare`, reading nothing. Comparing 100,000 cloned files a side takes about 1.2 s on an M-series Mac. A 100 MB file copied without its mtime is skipped in 0.9 s, reported in `add` and `status`, and `up` builds. With `BERTH_TEST_IMAGE_CHECK_DELAY=8s`, `list` and `status` return in well under 2 s while that `add` runs. The e2e tests also cover the reason and the `doctor` warning for a whole-repository context with no `.dockerignore`. Unit tests in `internal/image` cover which changes make builds differ, what is settled without reading, and both bounds.

Not covered: a file whose content changed while its size and modification time stayed the same, which only a tool that sets times on purpose produces. Git, rsync and make trust the same signal.

## Undoing it
The lock split is `addBerth`, `register` and `saveRecord` in `internal/cli/berths.go` and `LockWorktree` in `internal/registry`. The comparison and bound are `Compare` and `DefaultLimits` in `internal/image`, used by `shareImages` in `internal/cli/images.go`. Raising the bound only changes when berth gives up. Records keep `builds`; an older berth ignores it.
