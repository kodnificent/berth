# 0031. A visible personal config, and config edit

Status: accepted

## Context
0007 put the personal layer at `<git common dir>/berth.toml`. Editors hide `.git/`, so editing it meant typing `code .git/berth.toml`, and an agent had to know where to look. The design now makes `berth.local.toml` the visible default, keeps `.git/berth.toml` as the hidden alternative, and adds `berth config path` and `berth config edit`. This supersedes 0007's location rule for the personal layer only; the repo and user layers and the merge rule are unchanged.

## Decision
- **Where:** the personal layer is `berth.local.toml` at the top of the main worktree (`worktree.Repository`), or `<git common dir>/berth.toml`. Berth reads it from main whichever worktree it acts on, as before. In a bare-repository layout there is no main working tree, so the default is the hidden file.
- **Both exist:** `config_invalid`, exit 5, on every command that loads config, with `<berth.local.toml>: <.git/berth.toml> is a personal config too; keep one and delete the other`. `doctor` reports it as a `config` problem, because doctor already turns every `config_invalid` line into one. Neither file is read.
- **Keeping it out of git:** when `config edit` creates `berth.local.toml`, it runs the 0016 `exclude` step on main for that one path: an anchored `/berth.local.toml` goes into `info/exclude` of the common directory when git would show the file as untracked. `.gitignore` is never touched. A file the user creates by hand is not excluded by berth, so it shows in `git status` until they ignore it; `config edit` is the documented way to create it.
- **`config edit`:** checks for an editor first (`$VISUAL`, then `$EDITOR`) and fails with exit 1 naming both variables and the path when neither is set, before creating anything. It then creates the file if missing, with a two-line commented header, and runs `sh -c '<editor> "$@"' <editor> <path>`, as git does, so `code --wait` and other commands with flags work and the path is never parsed by the shell. The editor gets berth's stdin, stdout and stderr (stderr for its stdout under `--json`, so stdout stays one JSON object). Berth itself reads nothing from stdin (0003). A non-zero editor exit is exit 1. When the editor exits cleanly, berth loads the whole layered config, so the edited file is checked together with the others, and reports problems in the 0008 `<file>: <key>: <problem>` form with exit 5.
- **doctor:** `berth.local.toml` is left out of the `ignored_file` warning (0030); a berth doesn't need its own copy. In a berth, a `berth.local.toml` at the berth's own top level is a `personal_config` warning, because berth reads only main's.

## Options considered
- Both files exist, one wins and doctor warns: every other command would then read a file the user may think is ignored, and doctor is not run on every change. An error names both files at the moment it matters, and the fix is one `rm`.
- Both files merged as two layers: a second precedence rule to learn, for a case with no use.
- `berth.local.toml` read from the worktree berth acts on: git doesn't copy ignored files into new worktrees, so a berth would silently lose the personal layer.
- `.gitignore` entry: committed, and the design forbids touching it (0016).
- Always adding the exclude entry, even for a hand-made file, on every `add`: `add` would write to `info/exclude` for a file it never created, and 0016 limits entries to paths berth wrote.
- Splitting the editor command on spaces instead of using the shell: breaks quoted paths and anything git users already put in `$EDITOR`.
- Refusing to run the editor without a TTY: would break `code --wait` and scripted editors run from agents, which need no terminal.

## Evidence
Unit tests in `internal/config` cover the location rule, the bare layout and the error when both files exist. The e2e tests cover `berth.local.toml` written in main and read from a berth, `.git/berth.toml` still working and `config edit` opening it without creating `berth.local.toml`, both files present giving exit 5 in `status` and `doctor`, and `config edit` with a scripted editor given as `<script> --wait`: the header, the anchored `info/exclude` entry, an unchanged `.gitignore`, an empty `git status` in main, exit 5 for an invalid edit and the no-editor error.

## Undoing it
The location rule is `config.Personal`; `config.Paths` uses it. Going back to the hidden file only would strand anyone whose config is `berth.local.toml`, so it would need a release note telling them to move it. The commands are in `internal/cli/configcmd.go`.
