# 0014. Cloning the source's .env files and [files] clone paths

Status: accepted; how files are copied on Linux is [0041](0041-linux-reflink-and-ci.md)

## Context
A new worktree has no `.env` and no gitignored files. The design clones the source's gitignored `.env*` files and the paths under `[files] clone`, with copy-on-write where the filesystem has it, and says ports, names and files are ready in seconds. It leaves open which `.env*` files count, what happens when the berth already has some of a listed path (git checks out tracked files inside it), and what a later `add` does.

## Decision
- **Source:** the main worktree.
- **`.env*` files:** every file whose name starts with `.env` that `git ls-files --others --ignored --exclude-standard --directory` lists in the source, at any depth. Files inside an ignored directory are skipped, since git reports only the directory. One is cloned only when the berth lacks it and its directory exists there.
- **`[files] clone`:** each path is merged in: whatever the berth lacks is cloned whole, a directory both have is descended into, and anything the berth already has, such as a tracked file, is left alone. Missing parent directories are created. A path the source doesn't have is skipped.
- **How:** `clonefile(2)` with `CLONE_NOFOLLOW` on macOS, which clones a whole directory tree in one call on APFS; a plain recursive copy keeping modes and symlinks wherever that fails. Linux is 0041.
- **Later `add`:** a `[files] clone` path is cloned once and recorded as `cloned`; later runs skip it, so a file deleted in the berth stays deleted and a large `node_modules` isn't walked again. A path added to the config later is cloned on the next `add`, and a listed path the source didn't have yet is tried again. A `.env*` file is cloned whenever the berth lacks it. Nothing is refreshed from the source.
- **Recorded:** every path berth created (the top of each cloned tree, the first parent directory it had to create, a `.env` it created) is kept in the berth's registry record as `files`, also when `add` fails partway, so removal deletes those and nothing else. `status` reports `.env` and these paths as `files`.

## Options considered
- Only `.env*` files at the root: misses a monorepo's per-app `.env` files, which git already tells us are ignored for nothing extra.
- Skip a listed path when the berth has it at all: Laravel's `storage/app` exists in every checkout because of its tracked `.gitignore`, so it would never be cloned.
- Overwrite what the berth has: would replace tracked files the branch changed.
- `cp -c`: the same clone but a process per path and no control over errors.
- Re-clone on every `add`: a second `add` would rewrite files and lose the berth's own changes.
- Merge again on every `add`: walks every file of a large dependency directory each time and brings back files the user deleted in the berth.

## Evidence
`e2e/files_test.go` covers: root and nested ignored `.env` files cloned and an unignored `.env*` file not; a listed directory, a file keeping mode 0600, untracked files inside a tracked directory and a path main lacks; and a second `add` changing no file. `internal/clone` tests cover merging, created paths and the copy fallback.

## Undoing it
Cloning lives in `internal/clone`, the choice of paths in `materialize` in `internal/cli/files.go`. Berths made before a change keep their recorded `files`.
