# 0028. Doctor's checks and shadowing reports

Status: accepted

## Context
`berth doctor` reports invalid config, an invalid berth compose project, a hardcoded `container_name`, an assigned port taken by another process, and cloned text files containing the source worktree's path; `status` and `doctor` both report exported variables that shadow managed keys (design, Doctor). The design says what is reported. This record says how each check decides, and what counts as not verified rather than a pass. Checks added later are in 0030, 0033, 0041 and 0052.

## Decision
- **Shadowing:** a managed key (every key `berth env` prints) is shadowed when the environment berth runs in exports it with a different value. The same value is not reported, so `eval "$(berth env)"` is never flagged. `status --json` lists `shadowed: [{key, exported, value}]`; the human `status` prints a line for each.
- **Findings:** `{check, level, location, message}`, where `level` is `problem` or `not_verified`. Any problem makes `doctor` exit 1; with `--json` the one object it prints then also carries `error: {code: "problems_found", message}`, so the envelope rule of 0002 still holds. Not verified alone exits 0.
- **Checks:** `config`, one finding per problem the loader reports, located at its file (the other checks then run with built-in defaults); `compose`, `docker compose config --quiet` in the worktree, so compose itself judges the project, located at the worktree with its `COMPOSE_FILE`; `container_name`, each service the repository's own compose files give a fixed name, located at its `file:line`, even though berth's override resets it, because `docker exec <name>` still reaches main's container; `ports`, each assigned port that isn't free on every interface (0010's test) and isn't published by the berth's own project, naming the listener from `lsof`, while a `[ports]` port held by a process whose working directory is in the worktree is the berth's own and passes, and one whose listener can't be found is not verified; `shadowing`, as above; `source_path`, below; `berth`, when the worktree has no berth.
- **Source path:** every regular file under the recorded `files` except `compose.berth.yml`, skipping a file with a NUL byte in its first 8000 bytes as binary. The forms are the source's resolved path, the `/…` alias that resolves to it when it lives under `/private` on macOS, and each with `\`, `\\` and `\/` for `/`. A match counts only where the path ends (the next byte isn't a letter, digit, `.`, `-` or `_`) and isn't the start of the berth's own path, since a worktree may sit inside main. The first match in a file is its finding, at `file:line`. Files are read in parallel, one worker per CPU. Contents are never rewritten (design, Cloned files).

## Options considered
- Treating every exported managed key as shadowing: flags the `eval "$(berth env)"` the design recommends.
- Validating with compose-go alone: berth loads with consistency checks off (0012), so it would pass projects compose rejects.
- Reporting `container_name` only when the berth's effective project keeps it: hides the `docker exec` trap, which is the collision the design's example names.
- Bind-testing a port berth's own containers publish: always busy while the berth runs, so every running berth would fail.
- Searching binaries too: a match in compiled data can't be told from chance, and the guarantee asks only that doctor report what it can find.

## Evidence
`e2e/doctor_test.go` covers: shadowed keys in `status` and `doctor`; a clean berth exiting 0, each problem found and then fixed back to clean, and a running berth's own published port passing; and the source path found in its resolved, JSON-escaped and `/var` forms at their lines, with a clean file not reported. Unit tests for the forms and the match rules are in `internal/cli/doctor_test.go`.

## Undoing it
All checks are in `internal/cli/doctor.go`; shadowing is `shadowed` in `internal/cli/berths.go`.
