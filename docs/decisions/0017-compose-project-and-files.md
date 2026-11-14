# 0017. The berth's compose project and files

Status: accepted; a compose project's directory is its first file's folder, not the worktree root, in [0055](0055-compose-project-directory.md)

## Context
`docker compose` run in a berth must act on the berth's own project, even when the compose file pins `name:`, and must read the repository's own compose files, including `compose.override.yml`, then the files under `[compose] files`, then berth's own override (design, Guarantees and Configuration). Compose takes `COMPOSE_PROJECT_NAME` from `.env` over a pinned `name:`, but setting `COMPOSE_FILE` stops it loading `compose.override.yml` on its own, so berth must list every file itself. A `compose.override.yml` is often gitignored, so `git worktree add` doesn't bring it.

## Decision
- **Keys:** when the berth has a compose project, the managed block (0013) sets `COMPOSE_PROJECT_NAME` to the berth's compose project (0011) and `COMPOSE_FILE` to its file list. Both are stored in the berth's record, so `status` and `env` print what `add` wrote. `[env.set]`, `[env] url`, `[env] host` and `[ports] env` may not name either key (exit `5`), since either would undo the berth's isolation.
- **Files:** when main's environment (process or main's `.env`) sets `COMPOSE_FILE`, its entries in order, relative ones resolved against main's directory as `docker compose` would resolve them there and written relative to the worktree. Otherwise compose's own choice made in the worktree: the first default file name present, then the first override name present. Then `[compose] files`, then `compose.berth.yml` (0018). Entries are joined with `COMPOSE_PATH_SEPARATOR` when main sets it, `:` otherwise.
- **Ignored compose files:** a file in that list that the worktree lacks, main has, and git ignores in main is cloned from main like an env file (0014) and recorded as created by berth.
- **No compose project:** neither key is written and no `compose.berth.yml` exists.
- Ports are read (0012) from the same files without `compose.berth.yml`, so the berth's ports never feed back into themselves.

This supersedes 0012's rule that a berth's project is read from the default names in the worktree plus `[compose] files`; the rest of 0012 stands.

## Options considered
- `COMPOSE_PROJECT_NAME` alone, with an override file compose finds by name: compose only finds `compose.override.yml`, which the repository may own.
- Leaving `compose.override.yml` out of `COMPOSE_FILE`: the berth would silently lose the repository's override, which breaks "works exactly as in main".
- Referencing main's ignored override by its absolute path: the berth would read a file in main, against "nothing points back at the source", and an edit in main would change every berth without `add`.
- Main's file list as found in main, also without `COMPOSE_FILE`: a branch that renames `docker-compose.yml` to `compose.yml` would point the berth at a file it doesn't have.
- `cli.WithConfigFileEnv` from compose-go: it resolves relative `COMPOSE_FILE` entries against berth's own working directory, not main's.
- Letting `[env.set]` override the keys: a typo would quietly put a berth on main's project.

## Evidence
`e2e/compose_test.go` covers a fixture that pins `name:`, and a tracked and a gitignored `compose.override.yml` followed by `.worktree/compose.yml`, checked by which file's value wins and that main is unaffected. A spike with Docker Compose 5.5.1 confirmed that `COMPOSE_PROJECT_NAME` in `.env` beats a pinned `name:` and that `COMPOSE_FILE` disables the automatic override. `internal/compose` tests cover `COMPOSE_FILE` with a custom separator resolved against the project directory.

## Undoing it
File selection and cloning are in `planCompose` in `internal/cli/compose.go`, the keys in `managedEnv` in `internal/cli/files.go`. Records keep `compose_file`, which the next `add` rewrites.
