---
title: Hostnames and cookies
description: A hostname for every worktree, so each one keeps its own logins.
sidebar:
  order: 3
---

## Why ports alone share cookies

Browsers keep cookies per host and ignore the port. `localhost:8000` and `localhost:20001` are the same site to a cookie jar, so two copies of your app on two ports share one session cookie and one CSRF token.

In practice: you log in to the worktree and main logs you out. A form posted in one fails its CSRF check because the other replaced the token. A test that clears cookies in one clears them in both. Giving each worktree its own port fixes the port collision and none of this.

A different host gets a different cookie jar. So berth gives every worktree its own hostname.

## A hostname for every worktree

| Worktree | Host |
| --- | --- |
| main | `localhost` |
| `fix-checkout` | `fix-checkout.localhost` |
| `feat-x` | `feat-x.localhost` |

The host is built from the worktree's name. The name comes from its branch when the berth is made, cut down to a valid DNS label, and is unique among every berth on your machine, across repositories. It never changes afterwards, and it is freed when the berth is removed. `berth status` shows it:

```text
name  fix-checkout
host  fix-checkout.localhost
url   http://fix-checkout.localhost
```

Browsers send every name under `localhost` to your own machine, so this needs no DNS setup and no `/etc/hosts` entry. Main keeps plain `localhost`, which no worktree is ever given.

The hostname is only a name. Berth runs nothing behind it: your app still listens on its own port, so the URL you open needs that port, as in `http://fix-checkout.localhost:20000`. The next section puts it into `url`.

If your dev server checks the `Host` header, it has to accept the worktree's name. See [plain dev servers](/docs/stacks/dev-servers/).

## Setting the host and URL

Two keys under `[berth]` decide what a worktree is called:

```toml
[berth]
host = "{name}.shop.localhost"
url = "http://{host}:{port.web}"
```

| Key | Default | Templates it can use |
| --- | --- | --- |
| `host` | `{name}.localhost` | `{name}` |
| `url` | `http://{host}` | `{name}`, `{host}`, `{port.<name>}` |

**Set `url` yourself.** The default has no port in it, so it only works for an app listening on port 80. Name the port your app answers on. A port name is a compose service with one `assign` port listed under `[compose.ports]`, a `"service:port"` key there, or an entry under `[ports]`:

```toml
[compose.ports]
"web:80" = "assign"       # {port.web}

[ports]
vite = { env = "VITE_PORT" }   # {port.vite}, for a process outside compose
```

**`host` must contain `{name}`.** Without it every worktree would get the same host, and berth refuses the config with exit code `5`:

```text
berth: /Users/you/shop/berth.toml: berth.host: must contain {name}, or every berth would share one host and its cookies
```

Putting the project in the host, as in `{name}.shop.localhost`, keeps worktrees of different repositories apart at a glance. No two worktrees ever get the same host: if a changed pattern would give a worktree a host another one already has, `berth add` stops and names the worktree that has it.

**Hand the values to your app.** A hostname only separates cookies if the app believes it is served from there. Map the keys that hold your app's address, and berth writes them into the managed block of the worktree's `.env`:

```toml
[env]
url = ["APP_URL"]                    # receives the URL
host = ["APP_HOST"]                  # receives the host

[env.set]
ALLOWED_ORIGINS = "{url},http://app.{host}:{port.web}"
```

```dotenv
# >>> berth (managed, do not edit)
ALLOWED_ORIGINS=http://fix-checkout.shop.localhost:20000,http://app.fix-checkout.shop.localhost:20000
APP_HOST=fix-checkout.shop.localhost
APP_URL=http://fix-checkout.shop.localhost:20000
# <<< berth
```

A name under the worktree's host, such as `app.fix-checkout.shop.localhost`, belongs to that worktree too, so an app that serves several subdomains works as it does in main.

After changing `host` or `url`, run `berth add` again in each worktree to rewrite its `.env`.

## Cookie domains that break isolation

A cookie set without a `Domain=` attribute is host-only: the browser sends it back to the exact host that set it and nowhere else. That is what you want, and it is what most frameworks do by default.

A cookie set with a `Domain=` is sent to that domain and everything under it. Whether that is a problem depends on how wide the domain is:

| `Set-Cookie` from `fix-checkout.shop.localhost` | Reaches | Isolated |
| --- | --- | --- |
| No `Domain=` | `fix-checkout.shop.localhost` only | Yes |
| `Domain=fix-checkout.shop.localhost` | The worktree's host and names under it, such as `app.fix-checkout.shop.localhost` | Yes |
| `Domain=.shop.localhost` | Every worktree of the repository | No |
| `Domain=localhost` | Every worktree on the machine, and main | No |

The wide ones usually come from a key copied from main's `.env`, such as `SESSION_DOMAIN=localhost` or `SESSION_DOMAIN=.shop.localhost`, which was harmless while there was one copy of the app.

**What `berth doctor` flags.** Berth can't tell a cookie domain from any other value by reading `.env`, so it checks what the app actually sends. With the app running in the worktree:

```sh
berth doctor --probe
```

requests the worktree's URL and reports every cookie whose domain reaches further than the worktree's host:

```text
cookie_domain  http://fix-checkout.shop.localhost:20000  cookie shop_session sets Domain=.shop.localhost, so a browser sends it to every berth under shop.localhost, not only this one; scope it to fix-checkout.shop.localhost or leave Domain out; the berth's .env sets SESSION_DOMAIN to it
```

A host-only cookie, or one scoped to the worktree's host or a name under it, is not reported. Without `--probe`, `berth doctor` makes no requests: it reports only the wide cookies [`berth proxy`](#the-proxy-port-free-urls-and-https) has seen your app set.

The fix is to give the key the worktree's host, or to leave the domain unset so the cookie is host-only:

```toml
[env.set]
SESSION_DOMAIN = "{host}"
```

Then run `berth add` again and restart the app. When exactly one key in the worktree's `.env` holds the wide domain, `berth doctor --probe --json` carries that config as the finding's `fix`. When several keys hold it, the finding names them and leaves the choice to you.

Three things to know about the probe:

- It connects only to `127.0.0.1`, using the worktree's hostname, and never starts your app. With nothing listening it reports the URL as not verified, never as a pass.
- It requests only ports berth assigned to the worktree, so it can never reach main's app or anyone else's. With the default `url`, which has no port, it reports `not requested: port 80 is not one of this berth's ports`. Set `url` as above.
- Cookies are often set on a page other than the first, such as a login page. List those under `[doctor] probe`:

```toml
[doctor]
probe = ["http://{host}:{port.web}/login"]
```

Old cookies outlive the fix. After narrowing a domain, clear your browser's cookies for the wide domain once, or the cookie set earlier keeps being sent.

## Server-to-server calls use 127.0.0.1

`*.localhost` names are a browser convenience. Outside a browser they don't always resolve: `curl` handles them itself, but a program that asks the operating system to look the name up may get no answer, on macOS in particular.

So anything on your machine that calls the worktree's app without a browser should use the loopback address and the worktree's port: a queue worker calling the API, a test runner, a webhook forwarder, a script.

```toml
[env.set]
INTERNAL_API_URL = "http://127.0.0.1:{port.web}"
```

The ports are the ones `berth status` lists. If the app picks a site by the `Host` header, send the worktree's name along:

```sh
curl -H 'Host: fix-checkout.shop.localhost' http://127.0.0.1:20000/health
```

Use `127.0.0.1` and the worktree's port, never `localhost:8000`: that is main.

Calls between containers of one compose project are untouched. Services reach each other by service name and container port exactly as they do in main, since berth changes only the ports published on your machine.

## The proxy: port-free URLs and HTTPS

Every worktree's host already works with its port, `http://feat-x.myapp.localhost:20001`. [`berth proxy`](/docs/reference/commands/#proxy) drops the port: run it in a terminal of its own and every worktree on the machine answers at `https://feat-x.myapp.localhost` and `http://feat-x.myapp.localhost`, and so does every name under it, such as `app.feat-x.myapp.localhost` for an app that serves several hosts. Tell it which port your app serves on:

```toml
[proxy]
port = "web"

[proxy.routes]
"vite.{host}" = "vite"   # a dev server on another of the worktree's ports
```

The app sees the `Host` the browser sent, as it does in main, with `X-Forwarded-Proto`, `X-Forwarded-Host` and `X-Forwarded-For` set by the proxy. Worktrees you add or remove while it runs are picked up within a second. Berth never starts the proxy, and the proxy never starts your app: a worktree whose app isn't running gets a page naming it and its port.

The proxy listens on this machine's loopback addresses only. Ports 80 and 443 are kept for administrators on Linux and macOS, so on first run it may exit with the command that allows them; or set `[proxy] http` and `https` in your user config to ports above 1023.

### HTTPS that browsers trust

The proxy signs a certificate for each name it serves from a certificate authority berth makes in its state directory. That authority can vouch only for names under `.localhost`, so trusting it can't be used to impersonate any other site. Run this once:

```sh
berth proxy trust
```

It adds the authority to your system's trust store through the system's own prompt, and prints how to add it to Firefox, which keeps its own store. `berth proxy untrust` takes it out again. Until you trust it, HTTPS still works behind a browser warning. With the proxy running, set `[berth] url = "https://{host}"` so the URL berth writes into your env is the one you open.

### Cookies the proxy catches

The proxy sees every `Set-Cookie` your app sends. One whose `Domain=` covers more than the worktree's host, such as `.myapp.localhost` or `localhost`, reaches every worktree under that name, so the proxy reports it on its output and to `berth doctor`, naming the cookie and its domain, never its value. Scoping the domain to the worktree's host fixes it for good. Until then, `[proxy] cookies = "rewrite"` has the proxy replace the wider domain with the worktree's host: the cookie still reaches the worktree's own subdomains, `app.` and `console.` alike, and no other worktree. A cookie scoped to the worktree's host or a name under it passes untouched. The proxy never renames a cookie or drops its domain, so a script that reads `XSRF-TOKEN` by name keeps working.
