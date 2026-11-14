# 0012. Reading compose projects with compose-go

Status: accepted; which files a project is read from is [0017](0017-compose-project-and-files.md)

## Context
Assigning ports needs the ports a berth's compose project publishes and the ports main's project publishes, resolved exactly as compose resolves them: interpolation from `.env`, override files, profiles and `extends`.

## Decision
Berth reads compose projects with `github.com/compose-spec/compose-go/v2`, the library compose is built on, without calling Docker.
- Berth names the files itself and never uses the library's own discovery. Which files a project is read from is 0017.
- Every profile is active, so a service under a profile still has its ports reserved and assigned.
- The berth's project is interpolated with its own `.env`, or main's until it has one, so container ports read from variables match main's.
- Environment files and consistency checks are skipped: only ports are read, and a fresh worktree may lack the files services name.
- A broken compose file in this repository's main or berth fails `add`; one in another repository is skipped.

## Options considered
- `docker compose config --format json`: exact, but needs the Docker CLI on every `add`, takes hundreds of milliseconds, and fails while Docker is restarting.
- Parsing YAML directly: would miss interpolation, overrides, `extends` and `include`.
- compose-go's own discovery: it searches parent directories, so a worktree nested inside main's directory with no compose file would read main's.

## Evidence
`internal/compose` tests cover profiles, interpolation from a chosen env file, published ranges, an unpublished port, a missing `env_file`, and a directory without a compose file inside one with it. `e2e/ports_test.go` runs `add` with Docker unreachable.

## Undoing it
All compose reading is in `internal/compose`. Replacing the library means reproducing compose's interpolation and merge rules; the rest of berth sees only service, container port and published host ports.
