# 0055. A compose project's directory is its first file's folder

Status: accepted

## Context
The design joins main's project name and the berth's `{name}` to name a berth's compose project, and `clone` and `share` find main's volumes by main's project name. Berth read every compose project with the worktree root as the project directory (0012, 0017). Docker Compose does not: with no `--project-directory`, it takes the folder of the first compose file. So with `COMPOSE_FILE=docker/compose.yml` and no top-level `name:`, Docker called main's project `docker` and berth called it by the repository's folder. Berth then looked for main's volumes under a project that did not exist, and a `clone` volume came out `ready` and empty. Relative bind mounts and build contexts resolved against the wrong folder too.

## Decision
- `compose.Load` sets the project directory to the folder of the first file it reads, whether `COMPOSE_FILE`, `[compose] files` or the default names chose it. The project's name, when the files and `COMPOSE_PROJECT_NAME` give none, and every relative path follow from that directory, as they do in Docker Compose.
- The `.env` that feeds interpolation is still the one in the directory berth was asked about, the worktree root. Docker Compose 5.5.1 does the same: run in the root with `COMPOSE_FILE` naming a file in `docker/`, it interpolates from the root's `.env` and ignores `docker/.env`.
- A berth's first compose file can be one git ignores, read from main before it is cloned. `Options.ProjectDir` then names the folder it will sit in inside the berth, so the berth's own paths are not resolved in main.
- A berth's recorded project name is never recomputed, so berths made before this change keep theirs. In an affected repository, new berths are named after the subfolder, as main is.

## Options considered
- Telling users to add `name:`: it fixes the name but not relative paths, and the failure without it is silent.
- Running `docker compose config` and reading its answer: exact by definition, but needs the Docker CLI where berth reads compose files today without it, and costs a process per load.
- Moving the `.env` lookup to the first file's folder as well: that is what compose-go does by default when given no working directory, and it is not what the Docker CLI does.

## Evidence
Tests in `internal/compose` cover, through `COMPOSE_FILE` and through named files, the project's name, a relative bind mount, and a port interpolated from the root's `.env` with a decoy `.env` in the subfolder; a project's own `name:` kept; and a first file read from elsewhere resolved in its own place. `e2e/compose_subfolder_test.go`, against real Docker, checks Docker's own name for main's project, that the berth's cloned volume holds main's data, and that the berth's project is built from main's. Without the change it fails with an empty clone and a project named after the repository's folder.

## Undoing it
Set `opts.WorkingDir` back to `dir` in `compose.Load` and drop `Options.ProjectDir`. Berths made meanwhile keep their recorded project names.
