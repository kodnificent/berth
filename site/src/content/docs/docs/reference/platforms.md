---
title: Platforms
description: Where berth runs, and what differs on Windows.
sidebar:
  order: 5
---

Berth runs natively on macOS, Linux and Windows 10 and later, and every [guarantee](/docs/reference/guarantees/) holds on each. Inside WSL it is the Linux build, for repositories in WSL's own filesystem.

## Where berth keeps its state

| Platform | Registry and clone state | User config |
| --- | --- | --- |
| macOS, Linux | `~/.local/state/berth/`, or `$XDG_STATE_HOME/berth` | `~/.config/berth/config.toml`, or `$XDG_CONFIG_HOME/berth/config.toml` |
| Windows | `%LOCALAPPDATA%\berth`, or `%XDG_STATE_HOME%\berth` | `%USERPROFILE%\.config\berth\config.toml`, or `%XDG_CONFIG_HOME%\berth\config.toml` |

`XDG_STATE_HOME` or `XDG_CONFIG_HOME` set to an absolute path wins over the default on every platform, Windows included. `berth config path --user` prints the config in use.

## The proxy on ports 80 and 443

[`berth proxy`](/docs/reference/commands/#proxy) listens on the loopback addresses only, and berth never raises its own privileges, so where the system keeps low ports for administrators it exits `1` with the command to run:

| Platform | Low ports | What to run |
| --- | --- | --- |
| Linux | Below `net.ipv4.ip_unprivileged_port_start`, 1024 by default, are root's | `sudo sysctl -w net.ipv4.ip_unprivileged_port_start=80`, until the next boot (a file in `/etc/sysctl.d` keeps it), or `sudo setcap cap_net_bind_service=+ep "$(command -v berth)"`, until berth is next installed |
| macOS | Below 1024 on the loopback addresses are administrators', though any user may take them on `0.0.0.0` | `sudo env HOME=... /path/to/berth proxy`, as printed: it carries `HOME` and any `XDG_STATE_HOME`, `XDG_CONFIG_HOME` or `BERTH_STATE_DIR` you set, which `sudo` would otherwise drop, so the proxy finds your worktrees. Once its ports are open it becomes the user who ran `sudo`, for good, before it reads your worktrees or writes anything, so every file it writes is yours. Run as root without `sudo` from your account, or with a state directory that account doesn't own, it exits `1` |
| Windows | No ports are kept for administrators, but a port in a range Windows reserves, often for Hyper-V or WSL, is refused | `netsh interface ipv4 show excludedportrange protocol=tcp` lists the ranges |

On every platform, `[proxy] http` and `https` in the user config move the proxy to ports above 1023 instead, and the URLs then carry them.

## Trusting the proxy's HTTPS

`berth proxy trust` adds the proxy's certificate authority to the store the system's own tools use, through their prompt:

| Platform | Store | Tool and prompt |
| --- | --- | --- |
| macOS | Your login keychain, trusted as a root for your user, for SSL only | `security add-trusted-cert -p ssl`; macOS asks you to allow the trust settings change |
| Linux | The CA certificate directory your distribution uses, such as `/usr/local/share/ca-certificates` or `/etc/pki/ca-trust/source/anchors` | `sudo install`, then `update-ca-certificates`, `update-ca-trust` or `trust extract-compat`; `sudo` asks for your password |
| Windows | Your user's Trusted Root Certification Authorities store | `certutil -user -addstore Root`; Windows asks you to confirm |

Only macOS lets the trust itself be narrowed to TLS servers. On Linux and Windows the store trusts the authority for whatever its certificate allows, and it allows only TLS servers with names under `.localhost`. Firefox keeps its own store on every platform, and Chrome and Chromium keep their own on Linux; `trust` prints how to add the authority to each. `berth proxy untrust` removes only the certificate `trust` added, found by its fingerprint. Run both as yourself, never with `sudo`: they ask for what needs an administrator themselves.

On Windows the proxy needs no administrator, since no ports are kept for one. Run from an elevated prompt it stays elevated, as Windows has nothing to give up to, and what it writes in your profile inherits your profile's permissions.

## Windows

### Git for Windows runs your scripts

`post-add`, `pre-remove`, `command` scripts and the git hook run with the `sh` that [Git for Windows](https://gitforwindows.org) installs, so a script written on a Mac runs unchanged on Windows. Berth finds that `sh` through the `git` on your `PATH`, whichever folder Git for Windows is installed in. With no Git for Windows, or a git that has no `sh.exe` such as MinGit, each script fails with a message naming `sh.exe`: `post-add` reads failed in `berth status`, `berth remove` stops before removing anything, and the database the script fills reads failed. `berth hook install` refuses, naming `sh.exe`, and writes no hook.

Inside those scripts, paths are spelled the way that `sh` spells them: `pwd` prints `/c/Users/you/app`. `BERTH_PATH` and `BERTH_SOURCE` are Windows paths, which the same `sh` reads as they are. `cygpath -w "$PWD"` gives the Windows spelling when a Windows program needs it.

A `command` script, and every process it starts, ends when the background process running it does, however that process ends, `taskkill /F` included.

### Cloned files

On a [Dev Drive](https://learn.microsoft.com/windows/dev-drive/), which is ReFS, `[files] clone` paths and SQLite copies are block clones: instant, and sharing clusters with main until either side writes. On NTFS they are full copies. `berth add` prints which path ran, and on NTFS both it and `berth doctor` say so and name Dev Drive.

A symlink in a cloned path needs Developer Mode, or an administrator: with neither, that path fails naming Developer Mode, the other paths still finish, and `add` exits with the error. Turn Developer Mode on and run `berth add` again to fill in the path. A symlink or junction that leads into main points into the berth afterwards, and a junction stays a junction. Removing a berth deletes a junction itself, never what it leads to.

### Editing the config

`berth config edit` opens `%VISUAL%`, or else `%EDITOR%`, through the same `sh`, so a value such as `code --wait` works as it does elsewhere.

### Compose files

The managed block joins `COMPOSE_FILE` with `;`, as `docker compose` expects on Windows.

### Docker

Berth works with any Docker engine that runs Linux containers: Docker Desktop, or Docker Engine inside WSL2 reached from Windows through `DOCKER_HOST`. A plain engine in WSL doesn't translate Windows paths, so a compose service that bind-mounts a Windows folder needs Docker Desktop; named volumes, which berth copies, work with either.

### Ports

A port another program listens on, on any address, is never assigned, as on macOS and Linux. `berth doctor` names the program listening on a port the berth was assigned, and `wslrelay` there means a container in WSL publishes it.
