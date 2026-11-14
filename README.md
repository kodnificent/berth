# Berth

`git worktree` copies your code. Berth copies everything else.

Berth gives every git worktree its own environment: ports, hostname, `.env`, dependency folders and data. Commands like `npm run dev`, `rails server` and `docker compose up` then work in any worktree unchanged, side by side with main and with each other.

It is a single Go binary. It never starts your services.

## Status

Pre-release. Expect changes before v1.0.

## Install

On macOS and Linux:

```sh
curl -fsSL https://useberth.dev/install | sh
```

On Windows, in PowerShell:

```powershell
irm https://useberth.dev/install.ps1 | iex
```

With Go, on any of them:

```sh
go install github.com/kodnificent/berth/cmd/berth@latest
```

The scripts download the build for your machine from the [latest release](https://github.com/kodnificent/berth/releases/latest), check it against the release's checksums, and install it for your user without `sudo` or an administrator. The binaries are also on the releases page.

## Use

Once per repository, in the main worktree:

```sh
berth init
```

Then, whenever a worktree needs to run:

```sh
git worktree add .worktrees/fix-checkout
cd .worktrees/fix-checkout
berth add
npm run dev
```

`berth add` assigns free ports and a hostname, writes them into the worktree's `.env` between markers, and copies untracked files and data from main. `berth status` shows what the worktree got, and `berth remove` deletes it again, along with everything berth created for it and nothing else.

To give every new worktree a berth without running `berth add` yourself:

```sh
berth hook install
```

## What it promises

- **It never starts your services.** No `up`, no `start`. You run your app the way you always do.
- **It never changes main's data.** Each worktree gets a copy.
- **It deletes only what it recorded as its own.**
- **Nothing has to be committed.** Berth can run entirely from private config.

Every guarantee has a test against real git and Docker. The full list is in the [guarantees reference](site/src/content/docs/docs/reference/guarantees.md).

## Agents

Coding agents that work in worktrees can each run, migrate and test their branch for real. `berth status --json` tells an agent its own URL and ports and what to do next, and `berth docs agents` prints the guides built into the binary.

## Documentation

- User docs: [`site/src/content/docs/docs/`](site/src/content/docs/docs/), published at [useberth.dev](https://useberth.dev).
- What berth does and guarantees: [`docs/design.md`](docs/design.md).
- How each guarantee is met: [`docs/decisions/`](docs/decisions/).

## Development

```sh
go test ./...
```

runs the unit tests.

```sh
go test -tags e2e ./e2e/...
```

runs the end-to-end tests against real git and Docker. They need Docker running and clean up everything they create.

## License

[MIT](LICENSE)
