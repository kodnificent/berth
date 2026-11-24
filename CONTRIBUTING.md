# Contributing to berth

Thanks for helping. This page says when to open a pull request, when to open an issue, and what a pull request needs before it can merge.

## Pull request or issue

**Open a pull request** when you want to make the change yourself: a bug fix, a feature, docs, tests. You do not need an issue first.

**Open an issue** when you don't: to report a bug, ask for a feature, or ask a question, and leave the work to someone else.

[`docs/design.md`](docs/design.md) is the spec. Before building a change in behaviour, read the principles and non-goals at the top of it: some requests, such as a command that starts your services, are out of scope on purpose.

## Reporting a bug

Include:

- `berth --version`, your OS and filesystem, and your Docker and Compose versions if containers are involved
- the output of `berth doctor`
- the command you ran, what you expected and what happened
- the smallest `berth.toml` and compose file that reproduce it, if you can

Report security problems privately through the repository's Security tab, not in a public issue.

## Setting up

You need Go (the version in `go.mod`) and git. The end-to-end tests also need Docker with Compose.

```sh
go test ./...
```

runs the unit tests.

```sh
go test -tags e2e -run TestName ./e2e/...
```

runs one end-to-end test against real git and Docker. The full e2e suite takes over an hour, so run the tests for the area you touched and let CI run the rest on your pull request.

The website and user docs live in `site/`:

```sh
cd site && npm install && npm run dev
```

## What a pull request needs

- **A test.** Every guarantee in the design has a test. A bug fix adds a test that fails without the fix. A change that touches a guarantee keeps or extends its test, and never weakens one to get green.
- **The design, if behaviour changes.** Update `docs/design.md` in the same pull request and say why. The design states behaviour, not mechanisms.
- **A decision record, if you chose a mechanism.** When you pick how a guarantee is met, or change how one is met, add a record under [`docs/decisions/`](docs/decisions/). Its README has the template.
- **The user docs, if a user would notice.** Update the matching page under `site/src/content/docs/docs/`.
- **One change.** Keep unrelated fixes in separate pull requests.

## Rules the code keeps

- **Berth never starts the user's services.** No `up`, `down` or `start`. Short-lived helpers that clone state are fine and must exit when done.
- **The core is framework-agnostic.** Nothing outside the preset loader names Laravel, Rails, Next or any other framework. Knowledge of ecosystems (compose, lockfiles, database engines) is fine.
- **Destructive operations delete only what berth recorded as its own.**

## Style

- `gofmt` and `go vet` clean.
- Comments say why, never what, and only when the code cannot. Most files need none.
- Commit messages are one line and say what changed.

## Review

CI runs the unit tests on push and the e2e suites (Linux, Windows and btrfs) on the pull request. A first-time contributor's run waits for a maintainer to approve it. Pull requests are squashed on merge.

By contributing you agree that your work is released under the [MIT license](LICENSE).
