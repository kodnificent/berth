---
title: Parallel agents
description: Run several coding agents at once, each with a working app.
sidebar:
  order: 1
---

## How agents get a berth without extra setup

Give each agent its own git worktree, as most harnesses already do. Berth then gives each worktree its own ports, hostname, `.env` and data, so three agents can run the app and its tests at once without one's migration breaking another's run.

Nothing has to be installed into the agent for this. Berth steers from its own output:

1. The agent runs `berth status` in its worktree. With no berth yet, berth says so and gives the command.
2. The agent runs `berth add`. It returns in seconds with the worktree's URL and ports.
3. The agent starts the app with the repository's usual commands, which read the files berth wrote.

```text
$ berth status
berth: worktree /Users/you/shop/.worktrees/fix-checkout has no berth; run berth add
next: run berth add before starting the app or its tests here
```

A berth is made when a worktree needs to run, not when it is created. Many agent worktrees only ever read and edit code, and each berth holds copies of your data and a share of the port pool. If you would rather every worktree had one from the start, [install the git hook](/docs/guides/hooks/#the-git-hook-berth-hook-install): it works for any tool that creates worktrees with `git worktree add`.

Berth suits unattended runs:

- It never prompts. No command waits on a question, in a terminal or out of one.
- `berth add` is safe to repeat. Run twice, or by the git hook and the agent both, it gives one berth.
- Every command takes `--json`, and exit codes are stable: `0` success, `1` error, `2` no free ports, `3` not a worktree, `4` a copy failed, `5` config invalid.

Agents abandon worktrees more often than they remove them, and an abandoned worktree keeps its ports and data. Clear them out now and then with [`berth prune --idle`](/docs/guides/removing/#idle-berths-and---idle).

## What an agent should run first: `berth status --json`

Before starting the app, running its tests or opening any URL, an agent should run:

```sh
berth status --json
```

```json
{
  "id": "9f2c4e1ab07d3355",
  "worktree": "/Users/you/shop/.worktrees/fix-checkout",
  "repository": "/Users/you/shop",
  "source": "/Users/you/shop",
  "created_at": "2026-11-10T15:46:15Z",
  "name": "fix-checkout",
  "host": "fix-checkout.localhost",
  "url": "http://fix-checkout.localhost:20000",
  "compose_project": "shop-fix-checkout",
  "ports": [
    { "name": "web", "host_port": 20000, "policy": "assign" }
  ],
  "env": {
    "APP_PORT": "20000",
    "APP_URL": "http://fix-checkout.localhost:20000"
  },
  "files": [".env", "vendor"],
  "state": [],
  "post_add": {
    "command": "composer install",
    "status": "ok",
    "exit_code": 0,
    "output": "Generating optimized autoload files",
    "log": "/Users/you/.local/state/berth/hooks/9f2c4e1ab07d3355.post-add.log",
    "started_at": "2026-11-10T15:46:16.192Z",
    "finished_at": "2026-11-10T15:46:41.530Z"
  },
  "shadowed": [],
  "builds": [],
  "next": [
    "use http://fix-checkout.localhost:20000 to reach this berth's app, not localhost or main's URL"
  ]
}
```

What to read from it:

| Field | Use |
| --- | --- |
| `url`, `host` | Where this worktree's app is reached. Never `localhost:8000` or main's ports, which main or another agent may be using |
| `ports` | Each port berth manages, with the host port it assigned |
| `env` | The keys berth wrote into the worktree's `.env` |
| `state` | Each volume, database and SQLite file, with `status` (`cloning`, `ready` or `failed`, or `creating` for a database not yet made) and progress while it copies |
| `post_add` | How your `post-add` hook went, when one is set |
| `shadowed` | Managed keys that a variable exported in the agent's shell overrides. Unset them before running the app |
| `next` | What to do now |

While a copy or `post-add` is still running, the agent waits with:

```sh
berth status --wait --json
```

It returns once everything has finished, with the same output. It exits `4` if a copy failed and `1` if `post-add` did, and the output then carries an `error` beside the status. Until then the worktree's database may hold only part of main's data, so an agent that starts tests early gets failures that mean nothing.

`berth add --json` prints only the berth's `id`, paths and whether it was `created`. The URL and ports come from `berth status --json`, or from the summary `berth add` prints without `--json`.

## Hints in the JSON output

`berth status --json` ends with `next`, a list of strings, each one thing to do now. While a copy or `post-add` is still running, the hint names it and says to run `berth status --wait` before starting the app. Once everything is ready, it gives the worktree's own URL and says to use it rather than `localhost` or main's. The list is empty when there is nothing to say, so an agent can always read it.

Errors carry hints too, as `error.next`. In a worktree with no berth, `status` exits `1` with error code `no_berth` and a hint to run `berth add` before starting the app or its tests. In your main checkout it exits `1` with `main_worktree` and a hint that berths live in linked worktrees. Without `--json`, the same hints print as `next` lines.

## Claude Code, Cursor, Codex and Conductor notes

Every harness gets the same behaviour from the same two commands: `berth status` to learn where the worktree runs, `berth add` when it needs to run. The Claude Code plugin is a convenience on top, never a requirement.

### Cursor, Codex, Conductor and other harnesses

There is no plugin for these, and none is needed. Put one line in the instructions file your agent reads, such as `AGENTS.md`:

```md
Before starting the app or running its tests in a git worktree, run `berth docs agents` and follow it.
```

The agent then gets the guide that matches the installed berth, and `berth status` steers it from there. A harness that creates its worktrees with `git worktree add` also triggers the [git hook](/docs/guides/hooks/#the-git-hook-berth-hook-install), if you installed it.

### The Claude Code plugin

The plugin starts every Claude Code session knowing its worktree's berth, so the agent never has to remember to ask. It lives in the berth repository under `plugins/claude/`. Load it with Claude Code's `--plugin-dir` flag, pointing at a clone of the repository:

```sh
git clone https://github.com/kodnificent/berth.git ~/src/berth
claude --plugin-dir ~/src/berth/plugins/claude
```

It is not in a plugin marketplace yet. `berth` itself has to be on the `PATH` Claude Code runs hooks with.

When a session starts, resumes, or is cleared or compacted, its `SessionStart` hook runs `berth status --json` in the session's directory and adds a short summary to the session's context:

- **In a worktree with a berth:** the URL, host, compose project, every assigned port, the state of each clone, `post-add`, and every `next` hint, such as waiting for a clone before starting the app.
- **In a worktree with no berth:** that there is none yet, and to run `berth add` before starting the app or its tests.
- **In your main checkout or outside a git repository:** nothing.
- **When berth is busy,** for example while another berth command holds its lock: one line saying so, after at most a second. The session never waits on berth.

The hook only reads. It never runs `berth add`, `remove`, `prune` or `reset`, never waits for clones, and never fails a session start: if berth is missing or errors, it adds at most one line.

The plugin also carries the `working-in-a-berth` skill, the same text `berth docs agents` prints first, so Claude Code loads it when the agent is about to run the app, its tests or `docker compose` in a worktree.

## The built-in agent guide: `berth docs agents`

```sh
berth docs agents
```

prints the agent guides built into the binary, as markdown. They can't drift from the version you have installed, since they ship inside it. There are two, printed one after the other:

| Guide | For | Covers |
| --- | --- | --- |
| `working-in-a-berth` | Every session in a worktree | Run `berth status --json` first, make the berth only when the app or tests are about to run, wait for copies, use the berth's URL and ports, never pass `-p` to `docker compose`, never edit the managed block, what not to remove |
| `setting-up-berth` | Once per repository | `berth init`, reading its suggestions, the config files, databases, hooks, and checking the result with `berth doctor` |

`berth docs agents --json` returns them as `guides`, a list of `name` and `markdown`, for a tool that wants one guide without the other.

The same files live in the berth repository under `docs/agents/`, and the Claude Code plugin's skill is a copy of the first.
