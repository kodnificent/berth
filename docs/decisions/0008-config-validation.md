# 0008. Config validation: strict schema, every problem, file and key named

Status: accepted

## Context
An unknown key, a wrong type or a template naming an undefined port must make the command exit 5, naming the file and the key. A config that is silently half-ignored is worse than one that fails, because berth would provision something the user didn't ask for.

## Decision
- Each file is parsed on its own first. A syntax error, a value of the wrong type, or a key outside the schema fails with `<file>: <problem>`, and the problem names the key (`unknown key berth.hots`, `line 2 (last key "berth.host"): incompatible types`). An unknown table is reported once, not once per key inside it.
- The merged config is then checked as a whole, because a template in one file may name a port defined in another. Each problem is reported as `<file>: <key>: <problem>`, where the file is the one whose value won for that key. Every problem is reported, one per line, so a user fixes them in one pass.
- The schema holds only what this version acts on. A table or key the design describes but berth does not act on yet is rejected as unknown, so a config never appears to work while doing nothing.
- Value checks: port keys are `"<service>:<container port>"` with a policy of `assign`, `ephemeral` or `none`; volume roles are `clone`, `share` or `fresh`; environment names match `[A-Za-z_][A-Za-z0-9_]*`; `[files] clone` paths stay inside the worktree; template rules are in 0009. Tables added since carry their own checks in the records that add them.
- All of these are `config_invalid`, exit 5 (0002). An unreadable file (permissions) is a plain error, exit 1, because the config itself may be fine.

## Options considered
- Warn on unknown keys: a typo such as `hots` would quietly fall back to the default host.
- Accept every table the design describes before berth acts on it: berth would accept settings it never applies.
- Stop at the first problem: cheaper, but a user with three typos runs berth three times.
- Validate only the merged tree: a type error could not name its file once layers are mixed.

## Evidence
`e2e/config_test.go` writes each kind of problem into each of the three layers and checks exit 5, `config_invalid`, and that the message names the file and key, for `add` on a new worktree, `add` on an existing berth and `status`. `internal/config` tests cover the value checks and cross-layer attribution.

## Undoing it
The schema is the `Config` struct and `Config.validate`. Adding a key is not a breaking change. Relaxing unknown keys to warnings would need a way to show warnings on every command.
