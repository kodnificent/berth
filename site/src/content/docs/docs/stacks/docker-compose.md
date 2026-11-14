---
title: Docker Compose
description: Each worktree gets its own compose project, ports and volumes.
sidebar:
  order: 3
---

`docker compose up` works in a worktree exactly as in main, and acts only on that worktree. Berth gives each worktree its own compose project name and writes `compose.berth.yml`, applied last through `COMPOSE_FILE` in the worktree's `.env`. You never pass `-p` or `-f`.

## What `compose.berth.yml` changes and what it leaves alone

It changes only what must differ per worktree: published host ports, `container_name`, volume and network names, built image names, and excluded services. Containers see the same container ports, service names, environment and configuration as in main.

Berth never changes your own port variables. A healthcheck that reads `${SEARCH_PORT}` inside the container still sees main's value; only the host side is remapped.

If a service sets `container_name`, berth resets it in each worktree so names don't collide. A script that runs `docker exec mysql` still reaches main's container, so `berth doctor` reports it: use `docker compose exec mysql` instead.

## Port policies: assign, ephemeral, none

| Policy | Effect |
| --- | --- |
| `assign` | A stable host port from the pool, kept until the worktree is removed, and usable in templates as `{port.<name>}` |
| `ephemeral` | Docker picks a free port each time; `berth status` shows it |
| `none` | Not published in worktrees |

A port main publishes on a fixed host port is `assign` by default, since publishing main's port again would collide. A port main already leaves to Docker stays `ephemeral`. When two of a service's port entries become identical once the policy is applied, berth writes one.

## Volume roles: clone, share, fresh

| Role | Effect |
| --- | --- |
| `clone` | The worktree gets a copy of main's volume, made in the background |
| `share` | The worktree uses main's volume itself; for caches that are safe to share |
| `fresh` | The worktree gets a new, empty volume (the default) |

A volume copy pauses main's containers that use the volume, so the copy is consistent, and resumes them as soon as the copy ends. Berth says so before it happens. While a container with a health check is paused, Docker reports it as unhealthy until its next check after the pause, so a service that waits on `service_healthy` may see a short blip.

`berth status` shows each copy's progress, and `berth status --wait` returns once copies and `post-add` are done.

## Excluding services

```toml
[compose]
exclude = ["mailpit"]
```

An excluded service never runs in a worktree. Services that `depends_on` it still start, so the project stays valid.

## Images and builds

A worktree builds only when main would. For the same build inputs, `docker compose up` in a worktree builds nothing and runs main's image. When the worktree changes a Dockerfile or a build input, it builds exactly as main would after the same change, under its own image name, so main's image is never replaced.

Berth decides this in `berth add` within a few seconds, without reading most files: git settles tracked ones, and copies of main's untracked files keep their size and modification time. If telling the worktree's inputs from main's would still mean reading more than 100 MB, or looking at more than 100,000 files, the worktree builds on `up` instead. `berth add` and `berth status` name the service and say why.

Give every build a `.dockerignore`. A build whose context is the whole repository with no `.dockerignore` counts every file in it as an input, including the worktree's own `.env` and `compose.berth.yml`, so each worktree builds its own image, and Docker reads all of it on every build. `berth doctor` in main warns about it.

## Your own compose overrides

`compose.override.yml` and any files in main's `COMPOSE_FILE` still apply in every worktree. A gitignored override in main is copied in. Files listed in `[compose] files` apply in worktrees only, after the repo's own files.

Run `berth doctor` in your main checkout to have compose validate the files a worktree would read before you create one.
