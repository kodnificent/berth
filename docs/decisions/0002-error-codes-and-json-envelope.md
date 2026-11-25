# 0002. Error codes, the JSON error envelope and `next` hints

Status: accepted; the exit codes of `exec`, which are the command's, are superseded by [0035](0035-exec-replaces-the-process.md)

## Context
The design promises `--json` on every command with a documented schema, and stable exit codes (`0` success, `1` error, `2` no free ports, `3` worktree not found, `4` state clone failed, `5` config invalid). Agents need to tell failures apart without parsing English.

The design's agent guidance has `berth status --json` carry `next` hints, so an agent that runs status first learns what to do: wait while state clones, use the berth's URL rather than `localhost`, and in a worktree with no berth, run `berth add` before starting the app (design, Agent guidance and Lifecycle). The `no_berth` and `main_worktree` refusals are errors, so their hints travel in the envelope.

## Decision
Every failure is an `apperr.Error` with a stable string code. Each code maps to one documented exit code:

| `error.code` | Exit | When |
| --- | --- | --- |
| `error` | 1 | Anything without a specific code |
| `usage` | 1 | |
| `unknown_command` | 1 | |
| `unknown_flag` | 1 | |
| `main_worktree` | 1 | A command that acts on one berth (`add`, `status`, `remove` and the like) is pointed at the main worktree |
| `no_berth` | 1 | A command that needs a berth is pointed at a linked worktree that has none |
| `config_exists` | 1 | `berth init` found the config it would write already there |
| `problems_found` | 1 | `berth doctor` found something wrong (0028) |
| `compose_too_old` | 1 | `berth add` found a docker compose too old to read the berth's override file (0058) |
| `no_free_ports` | 2 | |
| `worktree_not_found` | 3 | The path does not exist, or is not inside a git worktree (including a path inside `.git`) |
| `state_clone_failed` | 4 | |
| `config_invalid` | 5 | |

The list in code is `internal/apperr/apperr.go`.

With `--json`, an error is printed to stdout as exactly one object, `{"error": {"code": "...", "message": "..."}}`, and nothing goes to stderr. Without `--json`, the message goes to stderr as `berth: <message>` and stdout stays empty. Codes are never renamed; new codes may be added. Adding fields to the envelope is not a breaking change.

The envelope may also carry `next`, a list of strings saying what to do about the error, one hint each: `{"error": {"code": "...", "message": "...", "next": ["..."]}}`. It is left out when there is no hint. Without `--json`, each hint follows the message on stderr as `next: <hint>`.

Where hints come from:

- **Errors:** `apperr.Error` carries `Next`, set where the error is made. `no_berth` and `main_worktree` carry their hints this way, with their codes and exits unchanged.
- **Status, worked out, not stored:** `status` works its hints out from what it already reports, in one function, `statusHints`. A failed clone or a failed or skipped `post-add` gives no hint, since status already shows the failure and its reason, and a hint to use the URL would be wrong. `status --json` always carries `next`, empty when there is nothing to say.
- **Inside a hook (`BERTH_PATH` set):** no hint suggests `--wait`. A running or pending `post-add` there is normally the hook asking, and berth holds its lock until the hook ends, so it counts as settled and the berth gets its URL hint. A volume still cloning is named, with no `--wait`. This holds whichever berth the hook asks about, as `--wait` itself does (0029).

## Options considered
- Exit code only, no string code: several distinct failures share exit 1, so agents could not tell them apart.
- JSON errors on stderr: the design's agents read stdout; splitting success and failure across streams makes every caller read both.
- Numeric codes in JSON: less readable in logs and easy to confuse with exit codes.
- `no_berth` as `worktree_not_found` (exit 3): the worktree exists, so an agent would be told the wrong thing.
- Hints stored by `add` in the registry: they would go stale as clones finish, and status reads live state anyway.
- Hints on failed clones and `post-add` (run `berth add` again): what retries each case differs, and the failure line already says it.
- Omitting `next` from `status --json` when empty: makes agents check for the key before reading it.

## Evidence
`internal/cli/report_test.go` checks every code's JSON shape and exit code, and that `next` reaches both outputs; `cmd/berth/main_test.go` checks the codes reachable from the command line.

`e2e/hints_test.go` and `e2e/main_hints_test.go` cover each hint, the hinted errors keeping their codes and exits, and status inside a hook. Unit tests in `internal/cli` cover failed clones and `post-add`, and a clone inside a hook.

## Undoing it
Changing a code or the envelope is a breaking change to the `--json` schema and needs a schema version bump announced in the release notes.

Hints are `statusHints` in `internal/cli/berths.go`, `mainWorktreeHint` and the `WithNext` calls in `resolveBerthWorktree` and `findBerth`, `Next` and `NextOf` in `internal/apperr`, and the `next` printing in `report`. `next` in `status --json` and in the envelope is part of the `--json` schema, so removing it is a breaking change.
