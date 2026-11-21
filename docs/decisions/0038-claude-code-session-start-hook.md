# 0038. Claude Code SessionStart hook

Status: accepted

## Context
The design's Claude Code plugin injects `berth status` into each session's context so the agent knows its URL and ports, or that it has no berth and how to make one. It only reads, and has no `WorktreeCreate` or `WorktreeRemove` hooks (design, Agent integrations). Claude Code waits for `SessionStart` hooks before the session can use their context, and berth can sit on its registry lock while another command runs, so the hook needed its own bound. Claude Code skills need frontmatter the guide in `docs/agents/` does not carry, so the skill and the guide needed a rule to stay the same text.

## Decision
- **Wiring:** `plugins/claude/hooks/hooks.json` wires `SessionStart` with no matcher, so the hook also runs after resume, clear and compact. No hook `timeout` is set: the hook bounds itself.
- **The hook:** a POSIX sh script. It takes `cwd` from the session JSON on stdin, falling back to its own directory, and runs `berth status --json` there in the background, with stdout to a temp file and stderr discarded. A watchdog kills berth after 0.7 s, which leaves the rest of the budget for the script itself: 1 s, or 2 s on Windows, where starting each process is slow ([0048](0048-windows.md)). It never runs `add`, `remove`, `prune`, `reset` or `--wait`, and always exits 0.
- **Reading the JSON:** an awk program in the script flattens berth's JSON to `path` and value pairs, leaving string values JSON-escaped, so they go into `additionalContext` without being decoded and encoded again. No `jq`, no new berth subcommand.
- **Output:** one `{"hookSpecificOutput": {"hookEventName": "SessionStart", "additionalContext": ...}}` object on stdout, or nothing. With a berth: the URL, host, compose project, every port with its host port, every state entry with its status and progress, `post-add` status, every `next` hint, and a line to use these rather than `localhost`. With `no_berth`: there is no berth yet, and run `berth add` before starting the app or its tests. With `main_worktree` or `worktree_not_found`: nothing. Any other error code: one line with the code and message. Berth not on `PATH`: one line saying so. Killed at the deadline: one line saying berth is busy.
- **Skill sync:** the frontmatter (`name`, and a `description` saying when to use it) lives only in `SKILL.md`; `scripts/sync-claude-skill.sh`, run by `go generate ./docs/agents`, keeps it and replaces the body with `docs/agents/working-in-a-berth.md`. A unit test in `docs/agents` fails when the body differs from the embedded guide.

## Options considered
- A hook in Go, or a `berth` subcommand printing hook output: a Go hook needs its own build and install, and a subcommand ties Claude Code's output shape to berth's CLI; both are more than the shell needs.
- `jq`: not installed everywhere, and degrading to one line without it would lose the URL and ports on those machines.
- Human `berth status` output as the context: the design has agents and scripts read `--json`, not the human output, and the error codes that pick the case are only in the JSON.
- Polling with `kill -0` instead of a watchdog: an exited child stays a zombie until reaped, so `kill -0` keeps succeeding and every run would hit the deadline.
- Claude Code's hook `timeout`: it cancels the hook, so the session gets no busy line, and its unit is seconds, too coarse for a 1 s budget.
- A one-line message in main and outside a repository: main runs with its own ports and needs no berth, and a session outside a repository has nothing to do with berth.
- Generating the whole `SKILL.md`, frontmatter included, from the script: the frontmatter would then live in a shell script rather than the file Claude Code reads.
- A symlink from the skill to the guide: the skill needs frontmatter the guide does not have.

## Evidence
`e2e/claude_plugin_test.go` covers each output case, the hook returning in about 0.1 s with a clone running, the busy line past the deadline (a held registry lock and a berth that sleeps), the hook changing no berth, and the wiring. `docs/agents/agents_test.go` covers the skill's body and frontmatter. The hook runs the same under `dash`, `bash` and macOS `/bin/sh`.

## Undoing it
Delete `plugins/claude/`, `.claude-plugin/`, `scripts/sync-claude-skill.sh`, `docs/agents/agents_test.go` and the `go:generate` line in `docs/agents/agents.go`; nothing in the binary depends on them.
