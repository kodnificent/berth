# 0015. Symlinks in cloned files

Status: accepted

## Context
Nothing in a berth may refer to the source worktree's path: a symlink into the source must point into the berth, and file contents are never rewritten (design, State cloning > Cloned files). Storage links, Composer `path` repositories and workspace packages are symlinks, absolute or relative.

## Decision
After cloning a path, berth walks the clone and checks every symlink, including a cloned path that is itself a link:
- **Absolute, into the source:** rewritten to the same relative place under the berth. A target counts as into the source when it is under the source's path as written, or once symlinks in its longest existing prefix are resolved, so `/var/...` and `/private/var/...` on macOS both match and a dangling target still does.
- **Absolute, elsewhere:** unchanged.
- **Relative, staying inside the source:** unchanged, since from the berth it already lands at the same relative place.
- **Relative, climbing out of the source:** rewritten to the absolute path it pointed at from the source. The berth sits at another depth, so the same relative text would land somewhere else.

Links are copied as links, never followed, and the source's links are never touched.

## Options considered
- Rewrite only links under the cloned path itself: misses `public/storage`-style links that cross into another part of the worktree.
- Compare targets only as written: misses links written through `/var` or `/tmp` on macOS, which resolve under `/private`.
- Leave every relative link alone: a link out of the source, such as a Composer path repository in a sibling directory, would break in a worktree at a different depth.

## Evidence
`e2e/files_test.go` covers absolute links to a file, a directory, the source root and a dangling target, one written through `/var`, a cloned top-level path that is a link, relative links inside and beside the cloned path, and an absolute link outside the source; the source's links are checked unchanged. `internal/clone` tests cover the rules, including a relative link climbing out.

## Undoing it
The rules are `clone.Retarget`, applied by `clone.Repoint`. Changing them only affects berths cloned afterwards.
