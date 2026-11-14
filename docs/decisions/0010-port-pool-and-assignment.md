# 0010. Port pool and assignment

Status: accepted

## Context
The design guarantees that no two berths, and no berth and main, share a published host port, and that an assigned port is free on every interface when assigned and stays reserved while its berth exists. It leaves open where the pool comes from, which ports a berth needs when config says nothing, how "free" is checked, and what keeps a port across repeated `add` runs and a Docker restart.

## Decision
- **Pool:** `[pool] ports = "<first>-<last>"`, default `20000-29999`, read through the normal layers but meant for the user config. One pool serves every berth on the machine; berths with different pools still never share a port, because reservations are machine-wide.
- **Which ports:** every port the berth's compose project publishes gets a policy. `[compose.ports]` sets it; otherwise a port main publishes on a fixed host port is `assign`, and one Docker picks for main stays `ephemeral`. Services under `[compose] exclude` get none. Each `[ports]` entry is `assign`. Ports are named `<service>:<container port>` or by their `[ports]` key, matching `{port.<name>}` (0009).
- **Reserved:** host ports held by any other berth on the machine, plus every fixed host port published by the main worktree's compose project of this repository and of every other repository that has a berth, with every profile active. Main's project need not be running.
- **Free:** a port is free when binding it succeeds on `0.0.0.0`, `127.0.0.1`, `::` and `::1`, each tried separately. Only "address in use" and "permission denied" count as taken, so a host without IPv6 still works.
- **Choice:** the lowest port in the pool that is neither reserved nor in use.
- **Keeping:** assigned ports are stored in the berth's registry record (0005) under the registry lock. On a later `add` a port the berth still wants keeps its host port even if something now listens on it, since after a Docker restart that is the berth's own container; a port it no longer wants is released. Ports are never read from Docker, so `add` works while Docker is stopped or restarting.
- **Full pool:** exit `2`, `no_free_ports`, naming the port and the pool range. Nothing is recorded.

Berth only knows repositories that have a berth, so a berth added before another repository's first berth can hold a port that repository's main publishes. `doctor` reports an assigned port that another process has taken (0028).

## Options considered
- Ports from a hash of the berth's id: stable without storage, but collisions still need a registry to resolve, and a hash spread across the pool fragments it.
- A random free port: no better at avoiding collisions than lowest-first, and makes test output and `status` harder to read.
- Checking free ports with `docker ps`: misses non-Docker processes and stopped containers, and needs Docker running.
- Binding only `0.0.0.0`: on macOS a wildcard bind can succeed while another socket holds `127.0.0.1` on the same port.- A default pool above 32768: overlaps the range Linux and Docker use for ephemeral ports, so a port Docker picks for an `ephemeral` mapping could take an assigned one.

## Evidence
`e2e/ports_test.go` covers: five berths in two repositories, with main's ports inside the pool and not running; listeners on `0.0.0.0`, `127.0.0.1`, `::` and a port Docker Desktop publishes for a container, where only the remaining port is assigned; repeated `add` with the berth's own ports in use and Docker unreachable through `DOCKER_HOST`; a port staying reserved while its worktree is locked and missing; and a full pool. The test that restarts Docker Desktop for real only runs with `BERTH_E2E_RESTART_DOCKER=1`, since it stops every container on the machine. Unit tests in `internal/ports` cover policies, keeping, releasing and the probe.

## Undoing it
Wanted ports and allocation live in `internal/ports`, the reserved set in `internal/cli/provision.go`. Ports are in each record's `ports`; changing the allocation rule only affects new assignments, since held ports are kept.
