# 0016. Keeping berth's files out of git status

Status: accepted

## Context
Files berth writes must never show in a berth's `git status`, and the repository's `.gitignore` must stay unchanged; the design names `.git/info/exclude` as the place for ignore entries. A cloned `.env*` file is ignored already, but a `.env` berth creates and a `[files] clone` path may not be.

## Decision
After writing, berth asks `git ls-files --others --exclude-standard --directory` which of the paths it created, plus `.env`, would show as untracked. For each, it appends an entry anchored to the worktree root (`/web/build`, with gitignore's special characters escaped) to `info/exclude` in the repository's common git directory, unless that exact line is there already. Nothing is written when every path is ignored or tracked.

`info/exclude` is shared by every worktree of the repository, main included, so an entry also hides that path in main's `git status`. Berth only adds entries for paths it put into a berth, which in main are the source files it cloned from.

## Options considered
- `.gitignore`: committed, so it changes the repository; the design rules it out.
- A per-worktree excludes file through `core.excludesFile` with `extensions.worktreeConfig`: turns on a repository extension and edits git config, which older tools may not understand.
- `git status` to find shown paths: refreshes and rewrites the index on each run, so a repeated `add` would change a file.
- Unanchored entries (`build`): would hide same-named paths anywhere in the tree.

## Evidence
`e2e/files_test.go`: with untracked, unignored cloned paths and a `.env` berth created, in a repository whose `.gitignore` ignores nothing, `git status --porcelain --untracked-files=all` stays empty and `.gitignore` is byte for byte unchanged in main and the berth. A second `add` leaves `.git/info` untouched.

## Undoing it
The entries are written by `exclude` in `internal/cli/files.go`. Removing them on `berth remove` must keep entries another berth of the same repository still needs.
