---
title: Monorepos
description: Several apps and services in one repository.
sidebar:
  order: 4
---

Berth works at the level of the worktree, not the package. A worktree of a monorepo gets one berth: one name, one host, one set of ports, one managed block. Inside that, you can name as many ports and copy as many folders as the repository has apps. What berth doesn't have is a config, an `.env` block or a compose project per app. This page says where that line runs.

## One config, at the top

Berth reads `berth.toml` from the top of the worktree, and your personal config from the top of main. A `berth.toml` in `apps/web/` is not read.

Every berth command acts on the worktree that contains the current directory, so you can run `berth status` or `berth add` from any folder in it.

## Several dev servers per worktree

Give each server its own entry under `[ports]`, with the env key it reads:

```toml
[berth]
host = "{name}.myapp.localhost"
url = "http://{host}:{port.web}"

[ports]
web = { env = "WEB_PORT" }
api = { env = "API_PORT" }
admin = { env = "ADMIN_PORT" }

[env.set]
API_URL = "http://127.0.0.1:{port.api}"
PUBLIC_API_URL = "http://api.{host}:{port.api}"
ADMIN_URL = "http://admin.{host}:{port.admin}"
```

In a worktree on the branch `feat-x`, the managed block holds all of it:

```dotenv
# >>> berth (managed, do not edit)
ADMIN_PORT=20000
ADMIN_URL=http://admin.feat-x.myapp.localhost:20000
API_PORT=20001
API_URL=http://127.0.0.1:20001
PUBLIC_API_URL=http://api.feat-x.myapp.localhost:20001
WEB_PORT=20002
# <<< berth
```

A worktree has one host and one URL. For the other apps, build a name under the host with a template, as `api.{host}` does above. Names under a `*.localhost` host reach your machine in a browser like the host itself. For calls between your own processes, use `127.0.0.1` and the port.

Two apps that both read a key called `PORT` can't both get it from one block. Give each a key of its own and have the app read that, or pass it on the command line:

```sh
cd apps/api
berth exec -- sh -c 'PORT=$API_PORT npm run dev'
```

## Several `.env` files

Berth copies every gitignored `.env*` file main has, in any folder, into the same place in the worktree: `apps/web/.env`, `apps/api/.env.local`. A file is copied only when its folder exists in the worktree, and a file the worktree already has is never overwritten. Files inside a folder that is ignored as a whole are not found.

The managed block is written to one file only: `.env` at the top of the worktree. An `apps/web/.env` is copied from main as it is, with main's ports and URLs in it, and berth does not write into it.

So an app that reads the `.env` in its own folder needs one of these:

- **Run it through `berth exec`.** The command runs in the folder you are in, with the worktree's variables set, and they override what the app's own `.env` would otherwise supply in most tools, since the process environment usually wins.

  ```sh
  cd apps/web
  berth exec -- npm run dev
  ```

- **Point the tool at the top-level file.** Many tools take the folder to load env files from as an option, such as Vite's `envDir`.
- **Export the variables for a session** with `eval "$(berth env)"`, then start the apps as usual.

`berth doctor` reads the top-level `.env` only, so a stale port in `apps/web/.env` is yours to spot.

The same holds for local databases: `[state.mysql]`, `[state.postgres]` and `[state.redis]` read the server from main's top-level `.env`, and the worktree's database names go into the top-level block.

## Several compose files

Berth reads one compose project per repository, from the top of the worktree, the way `docker compose` run there would:

- With `COMPOSE_FILE` set in main's top-level `.env` or in your shell, the files it lists, in any folders.
- Otherwise `compose.yml` or its other default names at the top, plus `compose.override.yml`.

So a repository whose services are split across files works when main names them:

```dotenv
# main's .env
COMPOSE_FILE=apps/api/compose.yml:apps/web/compose.yml
```

The worktree's managed block then lists the same files with berth's own last:

```dotenv
# >>> berth (managed, do not edit)
COMPOSE_FILE=apps/api/compose.yml:apps/web/compose.yml:compose.berth.yml
COMPOSE_PROJECT_NAME=api-feat-x
# <<< berth
```

Every published port in every listed file gets its own host port, and `docker compose up` at the top of the worktree runs the whole set under the worktree's project name. Name the ports you want in templates under `[compose.ports]`:

```toml
[compose.ports]
"api:80" = "assign"   # {port.api}
"web:80" = "assign"   # {port.web}
```

When the first file in `COMPOSE_FILE` is in a subfolder, Compose names an unnamed project after that folder and resolves relative paths there. Berth does the same, so main's project and volumes are found under the name Docker gave them, and a berth's project is that name joined with the berth's. A top-level `name:` in the file still wins.

Files listed under `[compose] files` are added in worktrees only, after the repository's own. See [Docker Compose](/docs/stacks/docker-compose/).

## Packages with their own lockfiles

`[files] clone` takes any path inside the worktree, so list each package's dependency folder:

```toml
[files]
clone = [
  "node_modules",
  "apps/web/node_modules",
  "apps/api/vendor",
]
```

Each path is copied from main where main has it and skipped where it doesn't. Symlinks that point into main, as workspace links can, are repointed into the worktree.

`berth init` only looks for lockfiles at the top of main, so it writes the top-level folder and you add the nested ones yourself. A package manager that keeps one store for the whole workspace and links packages from it is often quicker to run again than to copy. Do that in the hook, which runs at the top of the worktree:

```toml
[hooks]
post-add = "pnpm install --frozen-lockfile --prefer-offline"
```

## What is not supported

- **A config per app.** One `berth.toml` at the top covers the worktree.
- **A managed block per `.env`.** Only the top-level `.env` gets one.
- **Several compose projects.** If each app has its own compose file and you run `docker compose up` inside `apps/api`, berth doesn't see that project, and it collides with main's as it would without berth. Combine the files with `COMPOSE_FILE` at the top instead.
- **A berth for part of a worktree.** `berth add` sets up every port and copies every listed path, whichever app you mean to run. Nothing is started, so the cost is ports from the pool and the copies.
- **More than one host database server.** A config names `[state.mysql]` or `[state.postgres]`, not both.
