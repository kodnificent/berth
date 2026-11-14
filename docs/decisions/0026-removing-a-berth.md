# 0026. What berth remove deletes, and how it knows each thing is its own

Status: accepted

## Context
`berth remove` deletes the berth's compose project, its `clone` and `fresh` volumes and its generated files, prints what it removed, and never deletes a `share` volume, anything in main, or anything berth didn't record as created by that berth, even when names match (design, Guarantees and Lifecycle). Removal leaves tracked files untouched and `.env` exactly as it was before `add`. `pre-remove` runs before anything is deleted. Compose makes `fresh` volumes on `up`, so left to compose berth has no record of them.

## Decision
- **Fresh volumes are berth's:** `add` creates every volume the berth's project declares that isn't external, `share` or `clone`, as `<berth project>_<key>` with main's driver, options and labels plus compose's project and volume labels, and records it in `volumes` beside the clone targets (0020). Compose adopts them on `up`. One that already exists and isn't recorded is used but never recorded, and `add` says so. With Docker unreachable, `add` warns that compose will create them and `remove` won't know them.
- **New projects only:** when `add` picks a new berth's name, it skips any name whose compose project already has a container, volume or network in Docker, so everything later labelled with the berth's project came from the berth.
- **Ownership:** a volume is deleted only when the record lists it and Docker still labels it with the berth's project. Containers (with their anonymous volumes), networks and images are the compose project's when Docker labels them with the berth's recorded project, which is how compose itself decides what `down` takes; images must also be named `<berth project>-…`. Image tags berth gave the berth (0023) are untagged; `docker image rm` without force, so main's image keeps its own name. Share volumes are external in the berth and labelled with main's project, so neither rule reaches them.
- **Files:** each path in the record's `files` that git doesn't track is deleted; inside a recorded directory, any tracked file and the directories holding it stay. A `.env` berth didn't create loses only its managed block, with the separator newline (0013), written back with its mode. Entries in `.git/info/exclude` stay: every worktree of the repository shares them, and they only hide paths.
- **Order:** plan, then refuse while `post-add` is running, then run `pre-remove` in the worktree with no lock held, so it may run berth itself, and stop on a non-zero exit with nothing deleted. Then, under the registry lock until the end, so no `add` runs meanwhile: stop a clone in progress (kill its process group once it is confirmed as the berth's clone, as [0048](0048-windows.md) says, wait for its lock, and resume anything of main's it paused with the recovery of 0021), plan again (pre-remove may have changed containers), and delete containers, networks, volumes, images, files and the block in that order. Last go the clone and post-add state, the worktree's marker and the record.
- **Failures:** with Docker unreachable, a berth that has compose or Docker resources is refused before anything is deleted. When a deletion fails, the rest continue, the record keeps what is left, and the command exits 1 naming each failure, so running it again finishes. With `--json` the failure goes into the object `remove` already prints, as `error` beside `removed`, so a script reading one object never misses it; this is the shape `status --wait --json` uses (0029). Without `--json` the list goes to stdout and the error to stderr.
- **Output:** each deleted item as `kind name`, with `--json` `{id, worktree, dry_run, removed: [{kind, name}]}`; kinds are `container`, `network`, `volume`, `image`, `file` and `env_block`. `-n`/`--dry-run` prints the plan and changes nothing, `pre-remove` included.

## Options considered
- Deleting every volume labelled with the berth's project: a volume labelled by hand or by another tool would go too, which is exactly the name-match case the guarantee rules out.
- `docker compose down --volumes --rmi local`: needs the compose files, which are gone for an orphan, and deletes by label, not by record.
- Recording container ids: compose creates containers on `up`, long after berth last ran, so there is nothing to record them in.
- Refusing `remove` while a clone runs: a large clone can take minutes, and the agent hook that calls `remove` would fail for no reason; stopping it is the `kill -9` path already proven safe for main.
- Going on when `pre-remove` fails: the hook exists to save or hand off state first, and deleting after it failed could lose that.
- Removing `info/exclude` entries: other berths of the repository rely on them, and a stale one hides nothing berth didn't put there.
- A second JSON object holding the error, or an `errors` array per item in `removed`: the first breaks one object per command, and the second is a new shape for agents to learn where `error` already exists and its message names each thing that failed.

## Evidence
`e2e/remove_test.go` covers: containers, the network, clone and fresh volumes, the berth's image tag and files go, while main's image id, its cache checksum and a share volume stay, and `--dry-run` deletes nothing; `git status --ignored` and every tracked file are byte for byte as before `add`, and an own `.env` without a final newline comes back exact with mode 0600; a volume labelled with the berth's project but not recorded, an unlabelled volume, a container and an image named after the project, a berth whose project name extends this one's, and main's volume all survive `remove` and `prune`; and `pre-remove` sees `.env`, `compose.berth.yml` and the clone volume and can run `berth status`, while a failing one leaves the berth intact. Compose 5.5.1 labels built images with `com.docker.compose.project`, checked with `docker image inspect`.

`e2e/teardown_gaps_test.go`: a `remove` that fails part way (a cloned directory made read-only) exits `1` with one object holding `removed` and `error`, and a second `remove` finishes.

## Undoing it
`planTeardown`, `execute`, `quiesce` and `printRemoved` are in `internal/cli/remove.go`; `createFresh` in `internal/cli/compose.go`; the in-use check in `provision`. Dropping `createFresh` means `remove` leaves fresh volumes behind, since nothing records them.
