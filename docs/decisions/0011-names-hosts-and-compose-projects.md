# 0011. Names, hosts and compose projects

Status: accepted

## Context
The design guarantees that generated project, database and host names fit their target's rules and length limits, and that no two berths share a hostname or container. A plain slug of the worktree's directory name could exceed a DNS label, and two repositories with a same-named worktree would get the same default host, `{name}.localhost`, and so share cookies.

## Decision
- **Base:** the branch checked out when the berth is created, or the worktree directory's name on a detached HEAD.
- **Slug:** lowercase, accents dropped after Unicode NFD decomposition, every run of other characters turned into one hyphen, cut to 40 characters or less if `[berth] host` puts other text in `{name}`'s label. A slug with nothing left is `berth`.
- **Unique:** the first of the slug, then `-2`, `-3` and so on, whose name, host and compose project no other berth on the machine has. The host `localhost` and this repository's main compose project are never used. The choice runs under the registry lock.
- **Compose project:** main's compose project name (or the repository directory's name with no compose file), a hyphen and the berth's name, cut so the whole is one DNS label of at most 63 characters.
- **Stable:** name and compose project are chosen once, when the berth is created, and stored in its record, so moving a worktree or switching its branch keeps its host, cookies and containers. The host is recomputed from `[berth] host` on every `add` and refused if another berth already has it.
- **Host template:** `[berth] host` must contain `{name}` and must form a valid hostname, or config is invalid (exit `5`).
- `status --json` reports `name`, `host` and `compose_project`.

## Options considered
- Name from the directory: tools name directories after branches inconsistently (Claude Code adds a random suffix), and a branch is what the user recognises.
- Recompute the name on every command: a moved worktree or a new branch would change its host and project, losing cookies and orphaning its containers.
- Repository in the default host (`{name}.<repo>.localhost`): still collides for two repositories with the same directory name, and lengthens every host.
- A hash suffix on every name: unique without a registry, but every host would carry noise; a numeric suffix only appears on an actual clash.
- Transliterating all scripts: needs a large table; dropping what is left after removing accents is enough, since uniqueness doesn't depend on it.
- Bare berth name as the compose project: could equal another repository's main project, and hides which repository a container belongs to in `docker ps`.

## Evidence
`e2e/ports_test.go` adds berths for branches with slashes, uppercase, accents, CJK, an emoji, 200 characters, two 200-character branches differing only in the last character, `feat/x`, `feat-x` and `feat_x`, `localhost`, and the same branch in a second repository. It checks every compose project against compose's and DNS's rules, has `docker compose --project-name` accept it, checks every host label, and checks that names, hosts and projects are all distinct. A fuzz test in `internal/names` runs 3000 random branch names.

## Undoing it
Slugs and candidates live in `internal/names`, the choice in `internal/cli/provision.go`. Names are stored in each record, so a new rule only affects new berths unless existing names are cleared.
