---
title: Plain dev servers
description: Node, Rails, Django, Laravel, Go and anything else that reads .env.
sidebar:
  order: 1
---

With no Docker in the project, you start the app yourself: `npm run dev`, `php artisan serve`, `rails s`. Berth starts none of it. It gives each worktree its own ports, host and URL, writes them into the worktree's `.env`, and your dev server reads them the way it reads everything else there.

Berth can't see a dev server the way it sees a compose file, so you name each port the project needs in `berth.toml`.

## Which ports berth assigns and how your dev server picks them up

Each entry under `[ports]` is a name you choose and, usually, the env key your dev server reads its port from:

```toml
[ports]
web = { env = "PORT" }
vite = { env = "VITE_PORT" }
```

`berth add` gives each name a port from the pool, `20000-29999` unless `[pool] ports` says otherwise, and writes it to that key in the managed block of the worktree's `.env`. A port is only handed out when no other worktree holds it and nothing is listening on it. The worktree keeps its ports until you remove it, so bookmarks and config that hold a port stay valid.

Main is not touched. It has no managed block and keeps the ports it always had.

Your dev server has to read the key. Many do already: a server that starts on `$PORT` needs nothing. One that has its port written in a config file needs that file to read the key instead. For Vite:

```js
// vite.config.js
import { defineConfig, loadEnv } from 'vite'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  return { server: { port: Number(env.VITE_PORT) || 5173, strictPort: true } }
})
```

The fallback keeps main on its usual port. `strictPort` makes the server fail when the port is taken instead of moving to the next one, which would put it on a port berth knows nothing about.

Berth has no way to make a server use the port, so check after the first start: `berth status` lists each port, and `berth doctor` reports an assigned port that a process outside the worktree is listening on.

### A complete example

```toml
# berth.toml
[berth]
host = "{name}.shop.localhost"
url = "http://{host}:{port.web}"

[ports]
web = { env = "PORT" }
vite = { env = "VITE_PORT" }

[env]
url = ["APP_URL"]

[env.set]
VITE_API_URL = "{url}/api"

[files]
clone = ["node_modules"]
```

In a worktree on the branch `fix-checkout`, `berth add` copies main's gitignored `.env`, then writes this as the file's last lines:

```dotenv
# >>> berth (managed, do not edit)
APP_URL=http://fix-checkout.shop.localhost:20001
PORT=20001
VITE_API_URL=http://fix-checkout.shop.localhost:20001/api
VITE_PORT=20000
# <<< berth
```

Everything above the markers is main's `.env` as it was, and berth never changes it. If `.env` is tracked by git, `berth add` refuses, since it only writes gitignored files.

Two keys in that config are worth a second look:

- `[berth] url` has no port unless you give it one. The default is `http://{host}`, which is right behind a reverse proxy and wrong for a dev server on a port. Set it to the port people open in the browser.
- `[berth] host` is optional. The default is `{name}.localhost`, so the worktree above would be `fix-checkout.localhost`. Putting the project in the name keeps two repositories' worktrees apart at a glance. It must contain `{name}`.

## Mapping your own env keys with `[env.set]`

`[env] url` and `[env] host` are lists of keys that receive the worktree's URL and host as they are:

```toml
[env]
url = ["APP_URL"]
host = ["SESSION_HOST"]
```

`[env.set]` is for everything else. Each key gets a value built from templates, or a plain value that is the same in every worktree:

```toml
[env.set]
VITE_API_URL = "{url}/api"
HMR_HOST = "{host}"
ASSET_URL = "http://{host}:{port.vite}"
WS_URL = "ws://127.0.0.1:{port.web}/socket"
QUEUE_DRIVER = "sync"
```

| Template | Value |
| --- | --- |
| `{name}` | The worktree's name, made from its branch: `fix-checkout` |
| `{host}` | `[berth] host` with the name filled in |
| `{url}` | `[berth] url` with the host and ports filled in |
| `{port.<name>}` | The port assigned to that `[ports]` entry |

Write `{{` and `}}` for a literal brace. A template naming a port you didn't define stops berth with exit `5`, naming the file and the key, so a typo never becomes an empty value.

When the same key appears in more than one place, `[env.set]` wins over `[env] url`, `[env] host` and a `[ports]` entry's `env`. Berth writes `COMPOSE_PROJECT_NAME` and `COMPOSE_FILE` itself, so they can't be set here.

Keys a bundler inlines at build time, such as `VITE_` and `NEXT_PUBLIC_` ones, only take effect when the worktree builds. Build output copied from main still carries main's values, and `berth doctor` warns when a worktree sets such a key apart from main's.

## Dev servers that check the Host header

The worktree's URL uses its own hostname, `fix-checkout.shop.localhost`, so that each worktree has its own cookies. Browsers send every `*.localhost` name to your machine, so the name needs no DNS and no `/etc/hosts` entry.

Some dev servers refuse a request whose `Host` header they don't recognise. Berth can't change that from outside. If yours does, add the name to its allow list, from a key berth writes so the list is right in every worktree:

```toml
[env.set]
ALLOWED_HOSTS = "{host},127.0.0.1"
```

Then read that key where your framework keeps its allowed hosts. If the framework accepts a suffix, allowing `.localhost` once covers every worktree and needs no key.

For calls from one of your processes to another, use `127.0.0.1` and the port rather than the hostname. Outside browsers, `*.localhost` names don't resolve everywhere.

## Tools that don't read `.env`

Not everything loads `.env` on its own: a test runner, a plain `node` or `go run`, a script. Two commands cover those.

`berth exec` runs one command with the worktree's variables set:

```sh
berth exec -- go run ./cmd/server
berth exec -- npm test
```

The command runs in your current directory when that is inside the worktree, with its own stdin, stdout, stderr and exit code. The variables are the ones in the managed block, and they override any you have exported in the shell.

`berth env` prints the same variables as `export` lines, for a shell session:

```sh
eval "$(berth env)"
```

Both are escape hatches. A tool that reads `.env` needs neither.

An exported variable is the usual reason a server ignores its port: most tools let the shell win over `.env`. If your shell exports `PORT`, `berth status` says so:

```text
shadowed PORT  exported as 3000 in this shell, which overrides the berth's 20001
```

Unset it, or start the server through `berth exec`.

## Dependency folders

A new worktree has no `node_modules` or `vendor`, because git doesn't copy ignored files. List the folders a worktree should start with:

```toml
[files]
clone = ["node_modules", "vendor"]
```

`berth add` copies each one from main and says how:

```text
berth: cloned node_modules (cow) in 512ms
```

`cow` is a copy-on-write clone, which shares main's blocks until either side changes a file. It is what you get on APFS, btrfs, XFS and a Windows Dev Drive. Elsewhere the copy is a full one and berth says so.

Nothing is copied that the config doesn't list, apart from main's gitignored `.env*` files. Files the worktree already has are never overwritten, a path main lacks is skipped, and symlinks that point into main are repointed into the worktree. Berth runs no package manager. If the worktree's lockfile differs from main's, install in a `post-add` hook:

```toml
[hooks]
post-add = "npm ci"
```

Leave out virtualenvs and build output such as `.venv` and `target/`. They record absolute paths, so a copy would point back at main. Install or build them in `post-add` instead.

`berth init` in main writes the `[files] clone` entries it can detect from your lockfiles, so you rarely start this list by hand.

## Ports for processes outside compose with `[ports]`

A project can mix the two: databases in Docker Compose, the app and its asset server on the host. `[ports]` is for the second kind, next to whatever compose publishes:

```toml
[compose.ports]
"db:5432" = "assign"

[ports]
web = { env = "PORT" }
vite = { env = "VITE_PORT" }

[env.set]
DATABASE_URL = "postgres://app:secret@127.0.0.1:{port.db}/shop"
```

Compose ports and `[ports]` entries come from the same pool and are named the same way in templates. A compose port can be used in a template once it is listed under `[compose.ports]` as `assign`.

A `[ports]` name can't be the name of a compose service that has an assigned port, since `{port.<name>}` would then mean two things. Berth refuses that config with exit `5`. The `env` key is optional: an entry written as `web = {}` still gets a port, shown in `berth status` and usable as `{port.web}`.

See [Docker Compose](/docs/stacks/docker-compose/) for the compose side.
