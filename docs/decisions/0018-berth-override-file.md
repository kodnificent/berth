# 0018. The berth override file

Status: accepted

## Context
Only the host side may differ between main and a berth: published host ports, container names, shared volumes and excluded services (design, Guarantees). Port policies must be honoured (`assign`, `ephemeral`, `none`), and excluding a service must never invalidate the project, even when another service depends on it. Compose merges override files in ways that defeat a plain override: port lists merge by their host side, so main's published port would stay beside the berth's; profile lists merge; and a dependency on a service that is not active makes `up` fail with "depends on undefined service".

## Decision
Berth writes `compose.berth.yml` in the worktree root, applied after every other compose file (0017), generated from the berth's project as compose resolves it, and rewritten only when its bytes change. It holds:
- **Ports:** for each service that publishes, `ports: !override` with every port in the resolved model, keeping target, protocol, host IP, mode and names, with only `published` set from the policy: the assigned port for `assign`, nothing for `ephemeral` so Docker picks, and the entry dropped for `none`.
- **Identical port entries:** after the policy is applied, an entry identical in every field to one already written for the service is dropped. When the repository's files publish one container port twice, such as main's `8026:8025` and a berth-only file's `"8025"`, an `ephemeral` policy turns both into the same entry, and compose would publish the pair twice. Entries that still differ, such as two host IPs, are kept.
- **Container names:** `container_name: !reset null` on every service that sets one, so compose names it after the berth's project.
- **Excluded services:** `profiles: !override [berth-excluded]`, a profile nothing enables. Each service depending on one gets that dependency rewritten with its condition and `restart` kept and `required: false`.
- **Fixed resource names:** a volume or network the compose file names explicitly, and that isn't external, is renamed `<berth project>_<key>`; one named after the project already follows the project name. External ones stay shared, as the design says.
- **Shared volumes:** see 0019.
- Values are written as JSON strings, which YAML reads as double-quoted scalars, with `$` doubled so compose doesn't interpolate them. A file with nothing to change still holds `services: {}`.

The `!override` and `!reset` tags need a recent Docker Compose (they arrived in the 2.24 line); only 5.5.1 was tested.

## Options considered
- Remapping the project's own port variables in `.env`: a healthcheck or config reading the variable inside the container would change too, which the design forbids.
- Plain `ports:` in the override: merged with main's, publishing main's port as well (tested).
- Removing an excluded service in the override: compose has no way to delete a service from a later file.
- Rewriting every dependent's whole `depends_on` with `!override`: changes entries the berth has no reason to touch.
- Generating YAML with a library: `!override` and `!reset` tags need node-level control, and a hand-written emitter of this small shape is easier to keep deterministic, since a repeated `add` must change no file.
- Deduplicating port entries by container port alone: would drop a deliberate second binding on another host IP or protocol.
- Leaving identical port entries to compose: Docker published one of the pair when tried, but that is Docker's behaviour, not something berth's file should rely on.

## Evidence
`e2e/compose_test.go` compares the two `docker compose config` outputs for a project with a pinned `container_name`, an excluded dependency and a share volume: they are equal once those, published ports, the project name and the worktree path are set aside. It also covers a healthcheck with a reassigned port, each port policy, an excluded dependency that still starts, and main running beside three berths. `internal/compose` override tests cover host IPs, ranges, unpublished ports, entries the policy makes identical, restart on a dependency, renamed and external volumes and networks, an empty override, and quoting. Spikes with Compose 5.5.1 showed plain port lists merging and `required: false` with a never-enabled profile letting `up` succeed.

## Undoing it
Generation is `Override` in `internal/compose/override.go`. The file is listed in each record's `files` and in `COMPOSE_FILE`, so dropping it means removing it from both. Identical entries are dropped by the `seen` set in `Override`.
