# 0007. Config files, layering and merging

Status: accepted; the personal layer's location is superseded by [0031](0031-visible-personal-config.md)

## Context
The design layers config like git: a personal layer, then `berth.toml`, then `~/.config/berth/config.toml`, then built-in defaults, with the personal layer visible from every worktree. It leaves open where exactly each file is found, what "the same key" means when tables and arrays meet, and which TOML parser to use.

## Decision
- **Personal:** `berth.local.toml` at the top of the main worktree, or `<git common dir>/berth.toml`; which one, and why, is 0031. In a normal clone the second is `.git/berth.toml`; every linked worktree shares the common directory, and a bare-repo layout still has one.
- **Repo:** `berth.toml` at the top of the worktree berth acts on, so each branch reads its own committed version.
- **User:** `$XDG_CONFIG_HOME/berth/config.toml` when `XDG_CONFIG_HOME` is an absolute path, else `~/.config/berth/config.toml` on every platform, as documented, rather than Go's `os.UserConfigDir` (`~/Library/Application Support` on macOS).
- **Merging:** the files are merged as TOML trees, lowest precedence first. Tables merge key by key, so `[env.set]` or `[compose.ports]` entries from different layers combine and a higher layer replaces one entry. Every other value, arrays included, replaces the lower value whole: `exclude = ["a"]` in a higher layer means exactly `["a"]`. Berth records which file set every leaf so errors can name it (0008).
- `add` and `status` load and validate config before anything else, so an invalid config never creates a berth.
- TOML is parsed with `github.com/BurntSushi/toml`: its `MetaData.Undecoded` gives every unknown key in one pass, and its type errors already carry the key path.

## Options considered
- `.git/berth.toml` found by `git rev-parse --git-dir`: in a linked worktree that is `.git/worktrees/<id>`, so the main worktree's file would be invisible.
- Repo config read from the main worktree only: a branch that changes `berth.toml` would be provisioned from main's version, which is surprising for a committed file.
- `os.UserConfigDir`: contradicts the documented path on macOS.
- Arrays appended across layers: no way to remove an entry a lower layer added, and git's own multi-valued keys are a known source of confusion.
## Evidence
`e2e/config_test.go` writes each layer in turn and removes them again, checking `[berth] host` and per-entry `[env.set]` merging, and reads `.git/berth.toml` written in main from two linked worktrees. `internal/config` tests cover the merge rule and the user path. The e2e harness sets `XDG_CONFIG_HOME` per test, leaving `HOME` alone because Docker reads its contexts from it.

## Undoing it
Paths live in `config.Paths` and `config.UserPath`; the merge in `config.merge`. Appending arrays instead would change what existing configs mean, so it would need a release note.
