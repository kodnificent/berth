---
title: How berth works
description: What happens on berth add, what berth writes, and what it never touches.
---

`git worktree add` copies your tracked files and nothing else. Berth copies the rest: the ports, names, files and data your app needs to run. Then it gets out of the way.

## Three steps

A worktree gets its berth when it needs to run: you, or an agent, run `berth add` in it before starting the app or its tests, or the git hook `berth hook install` adds runs it on every `git worktree add`, if you opted in. Either way, berth does three things in order.

1. **Assign.** It picks free ports from one pool shared by every repo on your machine, and gives the worktree its own name and hostname, such as `fix-checkout.localhost`.
2. **Write.** It writes those values into gitignored files your tools already read, mainly `.env`.
3. **Copy.** It copies untracked files and data from your main worktree: `.env`, dependency folders like `node_modules/` and `vendor/`, and your databases. Slow copies continue in the background.

After that, berth starts nothing. You run your app the way you always do, and it reads what berth wrote.

## What berth writes

Berth writes its values between markers in `.env` and never touches anything outside them:

```dotenv
APP_NAME=Shop
# >>> berth (managed, do not edit)
APP_URL=http://fix-checkout.localhost:20001
PORT=20001
DATABASE_URL=postgres://localhost:5432/shop__fix_checkout
# <<< berth
```

For Docker Compose projects it also writes `compose.berth.yml`, which changes only what must differ between worktrees: published host ports, volumes, and the names of containers, networks and built images. The project name itself goes in `.env`. Inside the containers, everything looks exactly as it does in main.

Files berth generates are ignored through `.git/info/exclude`, so your `.gitignore` stays yours.

## Why each worktree gets a hostname

Browsers keep cookies per host, not per port. Two worktrees on `localhost:8001` and `localhost:8002` would share a login. A hostname per worktree gives each one its own cookies. Every `*.localhost` name points at your machine, so this needs no DNS or `/etc/hosts` changes.

## Where the copies come from

The source is your main worktree. Berth copies its files and data at the moment it makes the berth. Main is never changed. If a database has to pause for a consistent copy, berth says so before it happens, and the pause lasts only for the copy.

## When a worktree goes away

| | Automatic? |
| --- | --- |
| Free its ports | Yes |
| Delete its databases, volumes and copied files | No. They stay until you run `berth prune` |

A missing folder doesn't prove the work was thrown away, so berth never deletes data on its own. `berth list` shows leftovers and the disk space they use.
