---
title: Using berth privately
description: Run berth with nothing committed to the repo.
sidebar:
  order: 5
---

Berth doesn't need the repository's permission. With no config at all it gives each worktree its own ports, names and `.env`, and everything it writes in a worktree is kept out of `git status` through `.git/info/exclude`, never `.gitignore`. Its own records live outside the repository, in `~/.local/state/berth` (`%LOCALAPPDATA%\berth` on Windows). When you want config, keep it in a file only you have.

## `berth.local.toml`

Your own config for a repository lives in `berth.local.toml` at the top of your main checkout. It is never committed: berth lists it in `.git/info/exclude` when it creates it, without touching `.gitignore`. Every worktree of the repository reads it from main.

```sh
berth config edit
```

opens it in `$VISUAL` or `$EDITOR`, creating it first if needed, and checks the config when you close the editor.

To start from what berth can detect instead of an empty file, run this in your main checkout:

```sh
berth init --private
```

It writes the same starting config `berth init` would, into `berth.local.toml` rather than `berth.toml`. Add `--dry-run` to print the file without writing it. It never overwrites a config that is already there.

`berth config edit` works from any worktree, since there is one personal config per repository, not one per worktree. A `berth.local.toml` inside a linked worktree is never read, and `berth doctor` warns about one:

```text
personal_config (warning)  /Users/you/shop/.worktrees/fix-checkout/berth.local.toml  berth reads the personal config from main only, so this file has no effect; move its keys to /Users/you/shop/berth.local.toml
```

With neither `$VISUAL` nor `$EDITOR` set, `berth config edit` exits `1` and names the file, so you can open it yourself. When the edited file is invalid it exits `5`, naming the key. The file is saved as you left it, and `berth add` refuses it until you fix it.

## Keeping it hidden in `.git/berth.toml`

`git clean -fdX` deletes ignored files, `berth.local.toml` included. If you run it, or want the file out of sight, put the same config in `.git/berth.toml` instead. Berth reads either one, but not both: with both present it stops with exit code `5` until you delete one. `berth config path` tells you which is in use.

```text
$ berth config path
/Users/you/shop/berth.local.toml
```

When neither file exists yet, it prints where `berth config edit` would create one. `berth config path --repo` prints the committed `berth.toml`'s path and `--user` the user config's, so a script never has to guess.

## What belongs in your personal config

Any key berth has can go in any layer, except `[proxy] http` and `https`, which berth reads from the user config only. Which file a key belongs in depends on who it is for.

- **The whole config, when your team hasn't adopted berth.** Ports, volumes, cloned files, env keys, hooks, databases: all of it works from `berth.local.toml` exactly as it would from `berth.toml`.
- **Your own differences, when the team has a `berth.toml`.** A hook you want off (`post-add = ""`), an extra path to clone, a service you never run, a database you would rather start empty.

What doesn't belong there:

- **Logins.** Berth has no key for one. It reads a database or Redis login from main's `.env` when it needs it, and never stores it.
- **Anything a teammate needs to get the same result.** That goes in the committed `berth.toml`.
- **Settings for one worktree.** The personal config applies to every worktree of the repository. A value that should differ per worktree is a template, such as `{name}`, `{host}` or `{port.web}`.

## Personal defaults in `~/.config/berth/config.toml`

The user config holds what is true of your machine, whichever repository you are in:

```toml
[pool]
ports = "21000-21999"         # the range every berth's assigned ports come from

[clone]
min_free = "25GB"             # warn before a copy leaves less free disk than this

[berth]
host = "{name}.dev.localhost" # the hostname pattern for repositories that set none
```

It lives at `~/.config/berth/config.toml` on every platform, or at `berth/config.toml` under `$XDG_CONFIG_HOME` when that is set to an absolute path. Berth never creates it; make the file yourself. `berth config path --user` prints where it goes.

The port pool is the key most worth setting here. Ports are handed out from one pool shared by every berth on the machine, `20000-29999` by default, so the range belongs to the machine and not to a repository.

Keep repository-specific tables out of it. A `[compose.ports]`, `[state.mysql]` or `[hooks]` entry in the user config applies to every repository on the machine.

## How config layers combine

Berth reads up to three files and its built-in defaults. The first that sets a key wins:

| Layer | File | Read from |
| --- | --- | --- |
| Personal | `berth.local.toml` or `.git/berth.toml` | Your main checkout |
| Repo | `berth.toml` | The worktree berth is acting on, so each branch reads its own version |
| User | `~/.config/berth/config.toml` | Your home directory |
| Built-in | | The binary |

A missing file is skipped. Tables merge key by key. Any other value replaces the lower layer's whole, arrays included. Given a team config:

```toml
# berth.toml
[files]
clone = ["vendor", "node_modules"]

[compose.volumes]
mysql = "clone"
redis = "fresh"
```

and your own:

```toml
# berth.local.toml
[files]
clone = ["vendor", "node_modules", "storage/app"]

[compose.volumes]
mysql = "fresh"
```

the worktree clones all three paths, since your list replaces the team's rather than adding to it, and gets an empty `mysql` volume while `redis` stays as the team set it. To add one entry to an array, repeat the entries you want to keep. An entry under `[state.mysql.databases]` or `[state.postgres.databases]` is replaced whole as well.

Berth refuses a config it doesn't understand, and the message names the file whose value won and the key:

```text
berth: /Users/you/shop/berth.local.toml: env.set.API_URL: {port.api} names no defined port: define api under [compose.ports] with "assign" or under [ports]
```

A change to any layer takes effect the next time you run `berth add` in a worktree, which rewrites its managed block and compose file. Run `berth doctor` in your main checkout after an edit to check the config before any worktree uses it.
