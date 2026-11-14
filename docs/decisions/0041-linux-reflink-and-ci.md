# 0041. Reflink on Linux, and saying which copy ran

Status: accepted

## Context
The design promises copy-on-write clones of files and dependency directories by reflink on btrfs and XFS, full copies elsewhere, a `berth add` that says which path ran for every item, and a `doctor` check of copy-on-write support. 0014 cloned with `clonefile(2)` on macOS and a plain copy everywhere else, so on Linux every clone was a full copy, `add` said nothing about files, and `doctor` had no such check. This record supersedes the How line of [0014](0014-cloning-env-and-listed-files.md); the rest of 0014 stands.

## Decision
On Linux each cloned file is reflinked with `ioctl(FICLONE)` and copied byte for byte only when the filesystem refuses, `add` says per `[files] clone` path whether it ran `cow` or `copy`, and `doctor` tries a clone from main to warn when dependency directories will be full copies.

### Details
- **Reflink:** `copyFile` in `internal/clone` first asks the kernel to share the source's extents with `ioctl(FICLONE)`, and copies the bytes only when that is refused. btrfs and XFS (made with reflink, the default since xfsprogs 5.1) accept it; ext4, tmpfs and a pair of paths on different filesystems refuse it without writing anything. It is tried for every file, since a refusal costs one system call. macOS keeps `clonefile` of the whole tree in one call, falling back to the per-file copy.
- **Reporting:** `clone.Merge` counts clones that share data and files copied byte for byte. `add` prints, for each `[files] clone` path it clones, `berth: cloned <path> (<strategy>) in <time>` on stderr, the strategy being `cow`, `copy`, or `cow, with N files copied in full` when the filesystem shared some files and refused others. `.env*` files are not reported: they are small and instant on every filesystem.
- **Doctor:** when `[files] clone` lists a dependency directory (`vendor`, `node_modules` or `.venv`, the directories of the lockfile table `init` already uses), `doctor` clones a 64 KiB scratch file from main's root into the worktree's root the way any clone runs, removes both, and warns, as `copy_on_write`, when no data was shared: `ext4: dependency clones (vendor) will be full copies, …`, or naming both filesystems when main and the berth sit on different ones. In main it tries main's own directory. A probe that can't run is `not_verified`. Docker data paths get no such check, since a volume clone is a full copy on every platform.
- **Docker volumes:** unchanged. Native Linux clones volumes through the same helper container and tar stream as Docker Desktop, a full copy, as the design's table says.

## Options considered
- `copy_file_range(2)`: reflinks on btrfs and XFS by itself, but on ext4 it copies in the kernel and returns the same success, so berth could not say which path ran.
- `cp --reflink=auto`: a process per path, GNU only, and the same silence about what it did.
- Asking the filesystem's name with `statfs` instead of trying a clone: XFS made without reflink and main and the berth on different filesystems look the same by name but can't share extents.
- A doctor warning for every `[files] clone` path, not only dependency directories: a small directory of config files cloned on ext4 is instant either way, and a warning on every doctor run there would be noise.
- Reporting `.env*` clones too: one line per env file on every `add`, saying nothing a user could act on.

## Evidence
`e2e/copy_on_write_test.go`: on btrfs the berth's `vendor/` and `node_modules/` files share physical extents with main's by `filefrag -v`, writing one in the berth leaves main's intact, and `add` reports `cow`; on ext4 they share none, `add` reports `copy` and `doctor` warns of full copies; on APFS `add` reports `cow`.

In `.github/workflows/test.yml` the `btrfs` job formats a 2 GB file as btrfs, mounts it on a loop device, sets `BERTH_TEST_BTRFS_DIR` and runs the btrfs and ext4 tests. The `e2e` job runs the whole suite on Ubuntu, where the temp directory is on ext4, so the full-copy path runs there. It installs Docker Compose v5.5.1, the version the tests are written against: the runner's v2.38 prints a build as `web  Built` rather than `Image … Built`.

## Undoing it
Reflink is `reflink` in `internal/clone/cow_linux.go`; making it return `errors.ErrUnsupported` brings back the plain copy, and `add` then reports `copy`. The doctor check is `copyOnWriteFindings` in `internal/cli/doctor.go`, and the report line is in `materialize`.
