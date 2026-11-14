---
title: Why berth
description: The five things two worktrees fight over.
sidebar:
  order: 1
---

`git worktree add` gives you a second copy of your code. It copies tracked files and nothing else, so the two copies still share everything around the code: the ports, the container names, the database, the login cookie. Editing in parallel works. Running in parallel doesn't.

The usual way out is to copy the changes back into the main checkout to try them, which makes the work serial again. Berth takes the other way: it gives each worktree its own copy of the environment, so the commands you already use run in any of them.

There are five things two worktrees fight over.

## Ports

Both worktrees want the same ports: the web server on `8000`, the asset server on `5173`, the database published on `3306`. The second one to start fails, or quietly moves to another port that nothing else knows about.

Berth assigns each worktree its own ports from one pool shared by every repository on your machine, and writes them where your tools read them. A worktree keeps its ports until you remove it.

## Names and cookies

Two compose stacks started from two folders of the same repository can end up with the same project name, and a service with a fixed `container_name` collides outright. Berth gives each worktree its own compose project name and resets fixed container names.

Cookies are the quieter problem. Browsers keep cookies per host, not per port, so `localhost:8001` and `localhost:8002` share one cookie jar: logging in to one worktree logs you out of the other, and CSRF tokens cross over. Berth gives each worktree its own hostname, such as `fix-checkout.localhost`, which gives it its own cookies. Every `*.localhost` name reaches your machine in a browser with no DNS or `/etc/hosts` change.

## Databases

A migration on one branch changes the schema under every other branch that uses the same database. Tests in one worktree truncate tables another worktree is reading.

Berth gives each worktree its own data, and starts it as a copy of main's instead of an empty database you have to migrate and seed. In a compose project that means the worktree's own volumes. The copy is consistent, main's data is never changed, and a slow copy runs in the background while you get on with the code.

## Untracked files

A new worktree has no `.env`, no `node_modules`, no `vendor`, because git ignores them. Before anything runs, you copy the config by hand and wait for an install.

Berth copies main's gitignored `.env` files, and the folders you list, into the new worktree. Where the filesystem supports copy-on-write, a dependency folder with tens of thousands of files is cloned without duplicating its data.

## The one local database

A MySQL, Postgres or Redis server installed on your machine is one server for every worktree, and every worktree's `.env` points at the same database on it. No amount of separate ports fixes that.

Berth keeps the one server and gives each worktree its own databases on it, next to main's, or its own key prefix on Redis, and points the worktree's `.env` at them. See [Local databases](/docs/stacks/local-databases/).

## Who it is for

Anyone who runs more than one worktree of a project at a time:

- You, with a long-running branch in one terminal and a fix in another.
- A reviewer who wants to run a branch without stopping their own work.
- Coding agents, each in its own worktree, that need to start the app and run its tests without stepping on each other or on you.

Agents are treated as first-class users. Every command has `--json` output, nothing ever prompts, and `berth status` tells an agent the URL and ports of the worktree it is in, so it doesn't guess `localhost:8000`.

A worktree gets a berth when it needs to run, not when it is created. A worktree that only edits code costs nothing, unless you opt in to the git hook that runs `berth add` on every `git worktree add`.

## What berth does

`berth add` in a worktree does three things: it assigns ports and names, writes them into gitignored files such as `.env`, and copies files and data from main. Then it is out of the way. `docker compose up`, `npm run dev` or `php artisan serve` works in the worktree exactly as it does in main, and acts only on that worktree.

`berth remove` takes it all back: the ports, the copies, and its own lines in `.env`.

## What berth does not do

- **It never starts your services.** There is no `berth up`, `down` or `start`. You start, stop and supervise your app with the commands you already have.
- **It runs no package manager.** It copies `vendor/` and `node_modules/`. An install, if you want one, goes in your own `post-add` hook.
- **It is not a sandbox.** It keeps environments apart. It does not contain untrusted code.
- **It is local.** There are no remote or cloud environments.
- **It runs no background service.** Berth acts when you run a command and exits. The only things that outlive a command are the copies it starts and your `post-add` hook, and they end when their work does.
- **It doesn't wire worktrees together.** A service is either the worktree's own or left out of it. Sharing a database with main happens only when you ask for it by name.
- **It doesn't know your framework.** It knows Docker Compose, lockfiles and database engines. Everything about a particular framework lives in your config, which `berth init` can start for you from a preset.
