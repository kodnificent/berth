# 0058. Refusing a docker compose too old for the override file

Status: accepted

## Context
The override file is written with `!override` and `!reset` so that a berth differs from main only on the host side ([0018](0018-berth-override-file.md)). Those tags are read by whatever docker compose the user runs, not by berth. An older compose reads the file without an error but not as written, and the No collisions guarantee breaks (design, Guarantees):

- Before 2.24.4, a `ports: !override` list is added to main's instead of replacing it, so the berth publishes main's host ports as well as its own. `docker compose up` in the berth then fails on a port main holds, or takes main's port while main is down.
- An excluded service gets `profiles: !override [berth-excluded]`, the same tag, so an older compose adds that profile to the service's own instead of replacing them: an excluded service with profiles of its own would start when one of them is enabled. This follows from the same merge; it wasn't run.
- On 2.18, `container_name: !reset null` applies to the first service in the file only. Every later service is named `"null"`, so they collide with each other.

Nothing reported it. `doctor` reads the berth's project with compose-go, which berth builds in and which reads the tags correctly, so the names it checked were already reset. Docker Desktop 4.20, which ships Compose 2.18.1, is enough to hit it (issue #3).

## Decision
- **The minimum is 2.24.4**, `compose.MinVersion`. `docker.ComposeVersion` runs `docker compose version --short`, which needs the CLI and its compose plugin but not the daemon. `compose.ReadsOverride` compares the leading `major.minor.patch` it prints, so a suffix such as `-desktop.1` is ignored and a missing patch reads as 0.
- **`add` refuses before it changes anything.** When the berth has compose files, `addBerth` checks the version right after planning them, before databases, the registry, files or volumes. Below 2.24.4 it exits `1` with the new code `compose_too_old`, naming the version found and the one berth needs, and a `next` hint to update compose and run `berth add` again. Without compose files there is no override and no check.
- **Not knowing doesn't block `add`.** When docker isn't found, `docker compose version` fails, or it prints something that isn't a version, `add` goes on as before. If compose is missing, the user's own `docker compose up` says so.
- **`doctor` reports it** wherever there is an override to read: in main when the project a berth would use has compose files, and in a berth whose record has a `COMPOSE_FILE`. The check is `compose_version`: a `problem` below 2.24.4, `not_verified` when docker isn't found, compose didn't answer or answered with no version, nothing otherwise. `init` shows it with the rest of main's findings.
- **Known limit:** berth can only ask `docker compose`, the plugin. Someone who runs a standalone `docker-compose` of another version is checked against the wrong binary.

## Options considered
- Writing an override older compose reads correctly: a plain `ports:` list merges by host side, so main's port stays beside the berth's, and nothing else drops an inherited list or name (0018).
- A `doctor` finding alone: `add` would still write a file compose misreads, and the first sign would be a collision at `up`.
- Refusing when the version is unknown: berth would then need compose to `add` a berth whose app the user starts some other way, and compose's own error at `up` already names the problem.
- Probing the behaviour with `docker compose config` on a scratch project rather than reading the version: slower, and it would need a project to probe on every `add`.
- Checking the Docker Desktop version: compose is installed in other ways too (plugins from the release page, Linux packages), and it's compose that reads the file.

## Evidence
Each docker/compose release binary was run as `docker-compose config` in a berth whose `compose.yml` has `web` (`container_name`, `8080:80`) and then `cache` (`container_name`), with the override berth 0.1.0 wrote (`cache` first, then `web` with its port `!override`d to 20003):

| Compose | `web` publishes | Names not reset |
| --- | --- | --- |
| 2.18.1 | 20003, 8080 | `web: "null"` |
| 2.23.3 | 20003, 8080 | none |
| 2.24.0 | 8080, 20003 | none |
| 2.24.3 | 8080, 20003 | none |
| 2.24.4 | 20003 | none |
| 5.8.0 | 20003 | none |

2.24.4's release notes bump compose-go to v2.0.0-rc3 "to fix multiple compose files merge issues" (docker/compose#11416).

`e2e/compose_version_test.go` puts a `docker` first on `PATH` that answers `compose version` as each case needs and runs the real one for everything else, since CI installs one compose:

- Reporting 2.18.1: `doctor` in main reports `compose_version`; `add` exits `1` with `compose_too_old` and a hint naming 2.24.4, and writes no `.env` or `compose.berth.yml` and makes no berth; reporting 2.24.4, `add` makes the berth; in it, `doctor` reports the old version and nothing for the real one. Without the check, `add` succeeds there and `doctor` stays silent.
- Failing as docker does without its compose plugin, or reporting `dev`: `add` makes the berth, and `doctor` in main and in the berth reports one `not_verified` finding.
- Reporting 2.18.1 in a repository with no compose files: `add` makes the berth, and `doctor` reports no `compose_version` in main or the berth.

`internal/compose/version_test.go` covers parsing and the boundary, and `internal/cli/compose_version_test.go` the error, its hint and each finding, docker not found among them.

## Undoing it
The check is `checkComposeVersion` in `addBerth` (`internal/cli/berths.go`) and `composeVersionFindings` in `runDoctor` and `mainFindings` (`internal/cli/doctor.go`), with the messages in `internal/cli/compose.go`, `MinVersion` and `ReadsOverride` in `internal/compose/version.go`, and `ComposeVersion` in `internal/docker`. `compose_too_old` is part of the `--json` schema (0002), so removing it is a breaking change; lowering `MinVersion` is not.
