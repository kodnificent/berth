# 0019. Share volumes

Status: accepted

## Context
A volume with role `share` in `[compose.volumes]` is the source's volume itself: a file written to it in the berth is visible in main (design, Configuration). `remove` must never delete a share volume, and neither should a user's `docker compose down --volumes` in a berth, since that would destroy main's data. On a fresh clone, main's volume may not exist yet, because compose creates volumes only on `up`.

## Decision
- The berth's override declares a share volume as `external: true` with main's volume name, as main's compose project resolves it. Compose then never creates or deletes it from the berth.
- `add` creates main's volume when Docker is reachable and it doesn't exist, with main's driver, options and labels, plus the `com.docker.compose.project` and `com.docker.compose.volume` labels main's compose would set, so main adopts it as its own. It is main's, not the berth's, and is not recorded as created by the berth. When Docker can't be reached `add` still succeeds and says so on stderr; compose then names the missing volume on `up`.
- A share volume main doesn't declare, or that is external already, is left as it is.

## Options considered
- Setting only `name:` to main's volume: the berth's compose would treat main's volume as one it manages, so `down --volumes` in the berth could delete main's data.
- Requiring main to have run first: `up` in a berth would fail where the same command in main succeeds.
- Creating the volume without compose's labels: main's compose expects its own project label on a volume it didn't mark external.

## Evidence
`e2e/compose_test.go` writes a file in the berth before main has ever run, reads it in main, then runs `down --volumes` in the berth and reads it again. A spike with Compose 5.5.1 showed main's `up` and `down --volumes` treating a volume created with those two labels as its own without warnings.

## Undoing it
The override entry is in `Override` in `internal/compose/override.go`, the creation in `ensureShared` in `internal/cli/compose.go`.
