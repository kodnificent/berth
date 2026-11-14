# 0020. Copying clone volumes

Status: accepted

## Context
A `clone` volume gives the berth a copy of the source's volume (design, Configuration). The copy must be a consistent point-in-time copy with no torn writes, must never change or lose main's data, and if a source service has to pause, berth says so first and the pause lasts only for the copy (design, Guarantees). On Docker Desktop volumes live on ext4 inside the VM: there is no copy-on-write and the host can't read them. A database volume of a gigabyte or more is ordinary, and a brief, announced pause of main's database is an acceptable price for a copy that isn't torn.

## Decision
- **Which volumes:** each `clone` key that main's compose project declares and doesn't mark external. The source is main's volume as main's project names it; the target is `<berth project>_<key>`, the name the berth's project gives it (0018). A key main lacks, or an external one, is skipped with a warning. A volume with a driver other than `local`, or with a `device` option, is refused, since its copy would be the same storage as main's.
- **Target:** berth creates the target with main's driver, options and labels plus compose's project and volume labels for the berth's project, so the berth's compose adopts it, and records it in the berth's registry record as created by berth, saving the record before creating the volume, so a kill at any later step never leaves a volume berth made but doesn't know (0036). A target that already exists and isn't recorded is never overwritten: the clone fails and says how to remove it.
- **Copy:** in a helper container (`busybox:1.36.1`, no network) with the source mounted read-only and the target read-write: `tar | tar --numeric-owner` into a staging directory inside the target, then a move into place and the root's owner and mode set from the source's. Owners, modes, symlinks, hard links and modification times are kept.
- **Consistency:** the running containers that mount the source, berth's own helpers aside, are paused (`docker pause`, the cgroup freezer) only while `tar` reads the source, and resumed straight after, also when the copy fails. The helper is started and the target emptied before the pause, so the pause covers only the read. A frozen database's files are what a power cut would leave, which the engine's own crash recovery makes consistent; nothing writes during the read, so the copy is one point in time. Volumes no running container mounts are copied without pausing anything.
- **Announcing:** `add` names the services that will pause, on stderr, before it starts the clone process. The clone process checks again when it copies and pauses whatever mounts the source then, so a container started in between is paused unannounced rather than copied torn.
- **Health checks:** berth keeps pausing and says what it does to health. When a container it will pause has a health check (its `docker ps` status carries a health state), the announcement adds that Docker reports it as unhealthy while paused and until its next health check after the pause, so anything gated on `service_healthy` sees a blip. The design states it under State cloning.
- **Missing source:** when main has no such volume yet (compose creates volumes on `up`), the berth's volume is created empty and reported ready with `source_missing`. `add` doesn't copy it later, because by then the berth may hold data of its own; `berth reset` does (0036). When the berth's volume already exists, as on a retry or a reset, the clone process empties it instead of copying, never mounting main's.
- **Status:** each clone volume is reported in `status` under `state` with its volume, strategy (`copy`), status (`cloning`, `ready` or `failed`), size, duration, the services paused and the pause in seconds.

## Options considered
- `mysqldump` into a running berth server: engine specific, needs the berth's server started, and the spike's 595 MB took 30.6 s, slower than a file copy.
- Copying without pausing: InnoDB files copied while being written are torn; the integrity test would fail.
- Stopping the source service instead of pausing it: drops its connections and runs its shutdown and startup, which is far longer than the copy.
- Timing the pause between health checks: Docker sets a paused container's health to `unhealthy` the moment it pauses, with no failed probe and a failing streak of 0, whatever the interval or retries. Timing can't help.
- Running a health check right after unpausing: Docker has no command or API call that runs one on demand; the status returns at the next scheduled check, up to one full interval later.
- `docker run` per copy while paused: the container start (about two seconds when measured) would be inside the pause.
- `cp -a`: busybox's doesn't keep hard links reliably; `tar` does.
- Copying straight into the target's root: a service started early would find half of its data and might use it; in the staging directory it finds a directory that isn't its data.

## Evidence
`e2e/volumes_test.go` covers: a copy compared entry by entry with its source (owners, modes, a symlink, a hard link, a timestamp and 1 MiB of random data), with compose naming the cloned volume; a clone under write load, where one session commits pairs of rows into two tables throughout the copy, the berth's MySQL starts, every InnoDB table passes `CHECK TABLE` and both tables hold the same number of rows; checksums of main's MySQL and cache volumes before and after cloning and changing the copy; and the announcement reaching add's stderr before Docker's pause event, naming the health effect, with the service running unpaused after and `status` reporting `paused` and `pause_seconds`. A spike showed busybox `tar --numeric-owner` keeping uid 1000, mode 0600, hard links and timestamps. In another, `mysqldump` of 595 MB took 30.6 s.

Health on Docker 29.8.1: a busybox container with `--health-interval 2s --health-retries 3` read `unhealthy` 1 s into a 4 s pause with `FailingStreak` 0 and no failed probe in its log, and `healthy` again about 1.5 s after unpausing. With a 10 s interval and a 2 s pause, it read `healthy` again 10 s after unpausing. A database with a health check read `unhealthy` after a 3.5 s pause.

## Undoing it
The copy is `copyVolume` in `internal/volume/clone.go`, its scripts beside it; which volumes and targets in `startClones` in `internal/cli/volumes.go`, which also builds the health note from `Container.Healthcheck` in `internal/docker/volumes.go`. Targets are listed in each record's `volumes`, so another mechanism can find what this one created.
