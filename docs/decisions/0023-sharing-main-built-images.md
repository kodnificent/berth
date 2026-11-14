# 0023. Sharing main's built images

Status: accepted; how build inputs are compared is superseded by [0034](0034-bounded-image-check-outside-the-lock.md)

## Context
A berth builds only when main would, and building in a berth never changes main's images (design, Images and builds). Compose names a built image after its project, `<project>-<service>`, and a berth has its own project (0011, 0017), so a fresh berth has no image under its name and `up` would build one even when its build inputs equal main's. Compose builds on `up` only when the image is missing; it never compares build inputs itself. A service that names its image with `image:` next to `build:` has the same image name in every berth, so a build in a berth would replace main's.

## Decision
- **Tagging:** at the end of `add`, for each service the berth builds that main also builds, berth tags main's image with the berth's image name when main has that image, the berth doesn't have its own yet, and the two builds' inputs are equal. The tag is recorded in the berth's record (`images`), so only tags berth made are ever berth's to remove. A name the berth already has is never touched: that image is one the berth built or was given, and replacing it would be build work main wouldn't do.
- **Build inputs:** the service's compose `build` section (with the worktree's path removed, so the same section in two worktrees is equal), the Dockerfile, the ignore file that applies (`<Dockerfile>.dockerignore`, else the context's `.dockerignore`), and every context file the build target reads that the ignore file keeps: its path, type, permissions, content and symlink target, not its times. What the target reads comes from the Dockerfile, parsed with BuildKit's own parser: the `COPY` and `ADD` sources and `RUN --mount` bind sources of the target stage and every stage it reaches through `FROM` and `--from`, plus named build contexts it uses. When the Dockerfile leaves that open (a build arg in a source or a stage reference, a bind mount without a source, a frontend other than `docker/dockerfile`, an unknown target), the whole context counts. How the two sides are compared, and the bound on that work, is in 0034.
- **Named images:** for a service that sets `image:` and `build:`, the override (0018) sets `image:` to `<berth project>-<service>`, and every service that runs that image gets the same name, so the berth builds and runs its own tag and main's stays as it is.
- **Docker unreachable or inputs unreadable:** `add` still succeeds and says on stderr that `up` will build.

Inputs are compared with main's current files, not with the files main's image was built from, which berth can't know. If main's image is stale, main's `up` would run it as it is, and so does the berth's.

## Options considered
- Pointing the berth at main's image name with `image:`: a build in the berth would replace main's image.
- Removing `build:` in the berth when inputs match: `docker compose build` in the berth would then do nothing after the user changes the Dockerfile, where main would build.
- Building the image during `add` and relying on BuildKit's cache: build work main wouldn't do, and a fully cached build still gives a new image ID (seen in the spike below), so containers would differ from main's for no reason.
- Counting the whole build context: a branch that only changes code a dev target bind-mounts would rebuild, and main's untracked and ignored files that a fresh worktree lacks would make every berth rebuild.
- A hand-written Dockerfile reader: line continuations, JSON forms, heredocs and escape directives are BuildKit's to define; its parser package is used instead.
- Copying image layers or exporting and loading: tags share layers for free.

## Evidence
`e2e/images_test.go` covers: a berth with main's inputs doesn't build on `up` and its `web` runs main's image ID; a Dockerfile changed before `add` builds on `up`, from the berth's Dockerfile; and main's image ID is unchanged after the berth builds, with compose's default name and with `image:` named. Unit tests in `internal/dockerfile` cover stage reachability and fallbacks, in `internal/image` which file changes count as a difference, in `internal/compose` comparable build sections and renamed images. A spike with Compose 5.5.1: `docker compose build --dry-run` reports "Building" whether or not the image exists, in main as in a berth; `up --dry-run` reports a build only when the image is missing; the same Dockerfile built in two projects gave two image IDs.

Not covered: an `ONBUILD` instruction in a base image can read the context, and a build secret's file content isn't compared, only its name and path.

## Undoing it
Tagging is `shareImages` in `internal/cli/images.go`, the comparison `Compare` in `internal/image` (0034), the stage analysis `Analyze` in `internal/dockerfile`, and the image rename in `Override` in `internal/compose/override.go`. Records keep `images`; a tag berth made is an ordinary tag, and removing it leaves main's image as it is.
