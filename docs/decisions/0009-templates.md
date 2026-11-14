# 0009. Templates: syntax, where each value may appear, and unassigned ports

Status: accepted; what `{name}` holds is [0011](0011-names-hosts-and-compose-projects.md)

## Context
Config values can use `{name}`, `{host}`, `{url}` and `{port.<name>}`, and `berth status --json` must show each templated key's effective value. The design does not say how the URL is built, which ports a template can name, or what status shows for a value whose port has no number.

## Decision
- **Syntax:** `{token}` is replaced; `{{` and `}}` are literal braces. An unknown token, an unclosed `{` or a stray `}` is invalid.
- **Where:** `[berth] host` may use `{name}`. `[berth] url` may use `{name}`, `{host}` and ports. `[env.set]` values may use all four. Nothing else is templated. This order (name, then host, then url) rules out cycles.
- **Defaults:** `host = "{name}.localhost"` and `url = "http://{host}"`. A project whose app has an assigned port sets `url = "http://{host}:{port.web}"`.
- **Port names:** a `[compose.ports]` entry with `assign` defines `{port.<service>:<container port>}`, and `{port.<service>}` when it is that service's only assigned port. A `[ports]` entry defines `{port.<name>}`. `ephemeral` and `none` ports have no stable value, so naming one is invalid, and a `[ports]` name may not equal a service with an assigned port.
- **`[env] url` and `[env] host`:** each listed key receives the URL or host. A key also set under `[env.set]` takes the `[env.set]` value.
- **Status:** `berth status --json` carries `name`, `host`, `url` and `env` (every key berth will set, expanded). A value that needs a port with no number assigned is left out of `env` (or `url`) and listed in `unresolved`, keyed `url` or `env.<KEY>`, with its template, instead of showing a guess.

## Options considered
- Leave unresolved templates in `env` as text: an agent would read `{port.web}` as a value.
- Shell-style `${...}`: collides with the compose and dotenv interpolation the same values often contain.
- No escape for braces: a JSON value in `.env` could not be written.

## Evidence
`e2e/config_test.go` checks `name`, `host`, `url` and every `[env.set]`, `[env] url` and `[env] host` key against each other with no template left. `internal/config` tests cover port values, escapes, `[env.set]` winning, and the unresolved report when no ports are given.

## Undoing it
Templates live in `internal/config/template.go` and `Config.Resolve`. Changing the `unresolved` shape is a `--json` schema change.
