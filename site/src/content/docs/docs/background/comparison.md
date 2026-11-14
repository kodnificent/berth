---
title: Comparison
description: How berth relates to other tools for worktree environments.
sidebar:
  order: 2
---

Almost everything berth does can be done another way, and most people arrive here already using one of them. This page says what each of those ways does well, where it stops, and where berth fits beside it. The yardstick is the [five things two worktrees fight over](/docs/background/why-berth/): ports, names and cookies, databases, untracked files, and the one database server on your machine.

Third-party tools are described from their own public documentation as it read in November 2026. They change, so check theirs before relying on a detail here.

## Doing it by hand

You copy `.env` into the new worktree, change the ports in it, run the installs, and create a second database if the branch needs one.

**What it does well.** There is nothing to install and nothing hidden. For one extra worktree now and then, it is the right amount of tooling.

**Where it stops.** It is a checklist you repeat for every worktree and get slightly wrong each time:

- You have to remember which ports are taken, across every repository on the machine.
- Different ports on `localhost` still share one cookie jar, so logging in to one worktree logs you out of the other.
- A second database starts empty unless you also dump and load main's.
- Cleanup depends on remembering what you created.

Berth is that checklist, run by one command, with a record of what it made so `berth remove` can take exactly that back.

## `COMPOSE_PROJECT_NAME` alone

Docker Compose already separates projects. Its documentation says a project name is how Compose isolates environments from each other, and gives running a copy per feature branch as a use. The name comes from the `-p` flag, then `COMPOSE_PROJECT_NAME`, then a top-level `name:` in the compose file, then the name of the project directory.

**What it does well.** Two stacks under different project names get their own containers, networks and named volumes. If your worktrees sit in differently named folders and the compose file sets no `name:`, you get this without doing anything.

**Where it stops.**

- **Ports.** A project name doesn't change published ports. If the file publishes `8000:80`, the second stack can't bind `8000`.
- **Fixed names.** A service with `container_name` has the same container name in every project.
- **A `name:` in the file.** It outranks the directory name, so every worktree gets the same project until something sets the variable.
- **Data.** A new project's volumes are new and empty.
- **Everything outside compose.** The missing `.env`, the dependency folders, the dev server you run on the host and the cookies are untouched.

Berth uses the same mechanism and fills in the rest. It writes `COMPOSE_PROJECT_NAME` and `COMPOSE_FILE` into the worktree's `.env` and adds one override file that remaps published ports and resets fixed container names. See [Docker Compose](/docs/stacks/docker-compose/).

## Compose wrappers

This is a category more than a product: a script or small CLI, often written inside a team, that runs `docker compose` for you with a project name and a port offset worked out from the branch.

**What they do well.** One command brings a branch's stack up, and the wrapper can do things berth refuses to, such as starting, stopping and supervising services.

**Where they stop.** Every compose command has to go through the wrapper. A plain `docker compose logs` typed from habit, run by an editor plugin or run by a coding agent acts on the wrong project or fails. Each flag, profile and extra file you use has to pass through it too.

Berth takes the opposite position: the environment is written into files compose already reads, so the plain command is the right command, and [berth never starts your services](/docs/background/principles/#berth-never-starts-your-services). If what you want is one command that also brings the stack up, a wrapper does more for you there than berth will.

## Dev containers and a VM per branch

A dev container moves the whole development environment into a container described by `devcontainer.json`. The VS Code documentation describes forwarding listed ports to your machine and working with several compose-managed containers from separate windows. Hosted versions go a step further: GitHub describes a codespace as a development environment in the cloud, a container on a virtual machine, which you can create from any branch or commit.

**What they do well.** This is the strongest separation on this page. Each environment has its own toolchain, system packages and filesystem as well as its own ports and data, it is the same for everyone on the team, and a hosted one takes the load off your laptop. If you need to contain code you don't trust, this is the category to look at. Berth is [not a sandbox](/docs/background/why-berth/#what-berth-does-not-do).

**Where they stop.**

- Each environment is built or pulled and then set up from scratch: dependencies installed, database migrated and seeded. It doesn't begin as a copy of the main checkout you already have working.
- Your editor, terminal and tools have to attach to the container. Commands don't run where they used to.
- A hosted environment is remote. Berth is local only.

Berth separates less and costs less. Worktrees share your machine's toolchain, and each one starts as a copy of main's files and data in about the time the copy takes. Choose by what you need kept apart: the toolchain and the code itself, or only the running environment.

## Agent app setup scripts

Tools that run coding agents in parallel usually create a git worktree per agent and give you a place to prepare it. Three examples, from their documentation:

- **Claude Code** creates worktrees with `--worktree` and copies the gitignored files matched by a `.worktreeinclude` file into each one. Its documentation describes worktrees as isolating file edits, and leaves setting up the rest of the environment to you or the agent.
- **Cursor** runs the commands listed in `.cursor/worktrees.json` in each new worktree. Its examples copy `.env` from the main checkout, install dependencies and run a migration.
- **Conductor** runs a setup script when it creates a workspace and an archive script before it archives one, has a setting for copying gitignored files, and gives each workspace ten ports starting at `CONDUCTOR_PORT`.

**What they do well.** They run at exactly the right moment, without being asked, and they can run anything. Copying env files and installing dependencies is handled well by all three.

**Where they stop.**

- The script is yours to write. Ports that don't collide, a database per worktree, cookie separation and cleanup are problems the script has to solve, and a handed-out port range still has to be wired into your app's config.
- Each tool has its own format. A worktree made by plain `git worktree add`, or by a different tool, gets none of it.
- Cleanup is a second script, where the tool offers one, and it has to know what the first one created.

These scripts and berth fit together. The setup script is a natural place for a single line, `berth add`, and the same line then works under every tool. The opt-in [git hook](/docs/guides/hooks/) covers tools with no setup script at all. [Parallel agents](/docs/guides/agents/) has the details.

## Env rewriters and proxies

Two smaller kinds of tool each solve one of the five problems.

**Per-directory environment loaders.** direnv, for example, loads and unloads environment variables as you change directory, from an `.envrc` file. That gives each worktree its own environment in your shell with no wrapper. It loads what you wrote, though: it doesn't choose free ports or know what other worktrees hold, and it reaches only programs started from that shell. It pairs well with berth, since `eval "$(berth env)"` prints a worktree's variables for tools that don't read `.env`.

**Local reverse proxies.** A proxy that maps a hostname to each dev server gives every worktree its own name, which fixes the shared cookie jar and saves remembering port numbers. It routes requests and nothing more: the servers behind it still need ports of their own, and files and data are untouched.

Berth gives each worktree a hostname without a proxy, because browsers send every `*.localhost` name to your machine. Its own proxy is optional and adds port-free URLs and HTTPS. See [Hostnames and cookies](/docs/guides/hostnames-and-cookies/).

## Framework tools

Some ecosystems have a local environment tool of their own. Laravel Valet is a clear example: once you park a folder, every directory in it is served at `http://<directory-name>.test`, with no port to choose and optional TLS.

**What they do well.** For the framework they are built for, the web side takes no configuration. A worktree placed in a parked folder gets its own hostname, and so its own cookies, by existing.

**Where they stop.**

- They serve the framework family they know.
- The hostname is the only thing separated. Valet's documentation points you to one database server on `127.0.0.1` for every site, so two worktrees with the same `.env` use the same database, queue and cache.
- Ports for anything beside the web server, such as an asset dev server, are still yours to assign.

Berth is [framework-agnostic](/docs/background/principles/#framework-agnostic), so it knows less about any one framework than these tools do. What it adds beside them is the part they leave shared: each worktree's own databases on the one server, with the worktree's `.env` pointed at them. See [Local databases](/docs/stacks/local-databases/).

## What berth borrows

Little in berth is new. Most of it is an existing convention, applied for you.

- **From git:** the verbs. `berth add`, `remove`, `list` and `prune` mirror `git worktree`, `-n` means dry run as it does in git, config is layered the way git's is, and generated files are hidden through `.git/info/exclude`.
- **From Compose:** the isolation itself. A berth's stack is an ordinary compose project, selected with `COMPOSE_PROJECT_NAME` and adjusted with an override file listed in `COMPOSE_FILE`.
- **From the `.env` convention:** the interface. Compose and most dev tools already read `.env`, so writing to it is how berth reaches them without wrapping them.
- **From browsers:** hostnames. Every name under `.localhost` already reaches your machine, with no DNS or `/etc/hosts` change.
- **From filesystems and database engines:** the copies. File clones are the filesystem's own copy-on-write, a Postgres copy is the server's own template copy, a MySQL copy reads one consistent snapshot the way `mysqldump --single-transaction` does, and a SQLite copy holds SQLite's own file locks.
- **From setup scripts:** the hook. Your `post-add` command runs after the copies, for whatever berth doesn't do.

What berth adds is the bookkeeping between them: one pool of ports for the whole machine, one name per worktree, and a record of everything it created so removal deletes exactly that.
