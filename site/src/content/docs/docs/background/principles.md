---
title: Principles
description: The rules berth never breaks, and why.
sidebar:
  order: 4
---

Berth does one job: it copies the environment the way `git worktree` copies the code. Each rule here protects that job, and each is something you can rely on when you script berth or hand it to an agent.

## Berth never starts your services

There is no `berth up`, `down` or `start`. You run `docker compose up` or `npm run dev` yourself, exactly as you do in main.

A tool that starts your app has to know how your app starts, and then every flag, profile and script you use has to pass through it. Berth stays out of that: once `berth add` returns, nothing of berth's is running between you and your app.

Berth does run short-lived helpers of its own to copy data, such as a container that copies a volume. They exit when the copy is done. Your `post-add` hook runs once, after the copies, and that is your command, not berth's.

## Native commands work unchanged

A command that works in main works in a worktree, and acts only on that worktree. You never pass a project name, a port or a file list to make it so.

This works because the environment is files. Compose and most dev tools already read `.env`, so berth changes a worktree's environment by writing gitignored files in it: a managed block at the end of `.env`, and `compose.berth.yml` for compose projects. Your commands read them and need no wrapper.

For the tools that don't read `.env`, `berth exec -- <command>` and `eval "$(berth env)"` set the same variables. They are escape hatches, not the normal path.

Inside the containers, nothing changes. Services see the same container ports, service names and configuration as in main. Only the host side differs: published ports, names, and the services you excluded.

## Copy, don't recreate

A new worktree gets a copy of main's files and data, not an empty environment you have to migrate and seed. The copy is what makes a worktree usable in the time it takes to create one.

Two promises come with it:

- **Main is never harmed.** Copying never changes or loses main's data. If a service has to pause for a consistent copy, berth says so before it happens, and the pause lasts only for the copy.
- **Nothing points back at main.** Symlinks that led into main lead into the worktree. Berth never rewrites file contents, so `berth doctor` reports a copied text file that still holds main's path instead.

Only what the config lists is copied, apart from main's gitignored env and compose files, so `berth.toml` shows exactly what a worktree starts with.

## Fast where possible, honest where not

Ports, names and env files are ready as soon as `berth add` returns. Data is as fast as your filesystem and database allow: a copy-on-write clone where there is one, a real copy where there isn't.

A slow copy runs in the background, and berth doesn't pretend otherwise:

- `berth add` names how each listed path was copied and how long it took.
- `berth status` shows each copy as `cloning`, `ready` or `failed`, with progress.
- `berth status --wait` returns when the copies and `post-add` have finished.
- A copy killed by a crash or a reboot reads as `failed`, never `ready`, and `berth add` again recovers it.

The same goes for `berth doctor`. When it can't check something, it reports "not verified" with the reason, never a pass.

## Destruction is explicit

Creating a berth can be automatic. Destroying its data never is.

When a worktree is deleted, berth frees its ports and hostname the next time any berth command runs. Its databases, volumes and copied files stay until you run `berth remove` or `berth prune`. A missing folder doesn't prove the work was thrown away: it may have moved, or be on a drive that isn't mounted.

Idle worktrees are the same. `berth prune --idle 7d` removes the berths of worktrees unused for a week, and only when you run it. It never touches the worktree's own files, and never a berth with a running container or a locked worktree.

`remove`, `prune` and `reset` each take `--dry-run`, or `-n`, to list what they would delete first.

## Berth deletes only what it recorded

Berth records what it creates for a worktree: each file, volume, database name and key prefix. `remove`, `prune` and `reset` work from that record, not from your config as it is today, so editing or breaking the config never redirects a deletion.

In practice:

- On a shared database server, berth drops the databases it recorded for the worktree, or those under the worktree's own prefix, and no others.
- In `.env`, it removes its own block and leaves every other byte as it was. It deletes the file only if it created it.
- Tracked files are never touched. Berth refuses to write into a `.env` that git tracks.
- A file the worktree already had is never overwritten by a copy.

## Framework-agnostic

Berth knows ecosystems: Docker Compose, lockfiles, package managers, database engines. It does not know Laravel, Rails or Next, and no behaviour depends on which one you use.

Framework knowledge is data. Your config says which env keys hold the URL, the host and the ports. Presets are files shipped with berth that suggest that config for a framework they detect. `berth init` applies them, `--preset <name>` picks one, and `--no-presets` skips them. A preset only writes config you could have written yourself.

This is why the rules in these docs are stated in terms of `.env` keys and compose files. If your stack reads its settings from the environment, berth can work with it.

## Non-interactive, with `--json` everywhere

Berth never prompts and never reads standard input, in a terminal or out of one. A command either does its work or fails with a message. That makes it safe in a git hook, in CI, or under an agent, where a question would hang forever. Anything that needs a decision is a flag. The one command that opens anything is `berth config edit`, which starts your editor because you asked it to.

Every command takes `--json`. Scripts and agents read that, not the lines written for people. Errors come in the same form:

```json
{
  "error": {
    "code": "no_berth",
    "message": "worktree /repo/.worktrees/feat-x has no berth; run berth add",
    "next": ["run berth add before starting the app or its tests here"]
  }
}
```

Exit codes are stable:

| Code | Meaning |
| --- | --- |
| `0` | Success |
| `1` | Error |
| `2` | No free ports in the pool |
| `3` | Worktree not found |
| `4` | A copy of data failed |
| `5` | Config invalid |

`berth exec` exits with its command's code.

Berth also refuses a config it doesn't understand. An unknown key, a wrong type or a template naming an undefined port stops the command with exit `5`, naming the file and the key. A typo never falls back to a default without telling you.

## Nothing committed unless you want it

Berth works with no config at all, and it can run entirely from a personal config that is never committed: `berth.local.toml` at the top of main, which berth keeps out of `git status` when it creates the file for you. A team that hasn't adopted berth sees nothing of yours.

The files berth generates in a worktree are ignored through `.git/info/exclude`. Your `.gitignore` is never edited.

## The same from every tool

Berth has one interface, the command line. Plain git, an editor, a script and a coding agent all get the same behaviour from the same two commands: `berth add` when a worktree needs to run, and `berth status` to learn where it runs. Integrations are extras that call those commands. None is required.
