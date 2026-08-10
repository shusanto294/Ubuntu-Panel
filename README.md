# Ubuntu Panel — Go

A server control panel: one static binary with the frontend inside it, nothing
to install on the machine it runs on, and a SQLite file for state.

This is the Go rewrite of the C panel. The frontend came across unchanged — the
same Vue pages, the same Inertia protocol, the same route names — so pages can
be ported one at a time instead of all at once.

## Why the rewrite

The C panel hand-rolled its own HTTP parser, JSON encoder, session store, cookie
parser and CSRF check. Each was a place to get a detail wrong, and one of them
was: a CSRF failure answered an Inertia request with a plain JSON body, which
the Inertia client refuses outright. The setup form dead-ended behind

> All Inertia requests must receive a valid Inertia response, however a plain
> JSON response was received.

with no way forward. Go's standard library replaces all of it, and the two
rules that failure came down to are enforced in one place each:

- **A session is stored before its cookies are handed out.** `internal/session`
  wraps the response writer so the database write and the `Set-Cookie` headers
  happen on the first byte written, whichever handler writes it. A session that
  cannot be stored fails the request loudly instead of producing a token that
  can never match.
- **An Inertia request is never answered with a JSON body that is not a page.**
  Anything that is not a page — expired session, sign-in required, a frontend
  rebuilt under an open tab — is a `409` with `X-Inertia-Location`, which is the
  protocol's own way to say "leave the app and load this URL". Both rules have
  tests in `internal/web/web_test.go`.

## Build

```sh
make            # frontend, then the binary with the frontend embedded
make test
make release    # dist/ubuntu-panel-linux-{amd64,arm64}
```

`CGO_ENABLED=0` throughout: the SQLite driver is pure Go, so cross-compiling for
a server from any machine needs no toolchain and the result has no shared
library dependencies.

## Run

```sh
PANEL_DATABASE=./panel.db PANEL_LOG=./panel.log PANEL_BIND_PORT=8088 ./ubuntu-panel --config /dev/null
```

Open `http://127.0.0.1:8088`. With no account yet it goes to the setup form.

For frontend work, serve the build from disk so a rebuild shows on reload:

```sh
PANEL_DEBUG=1 PANEL_PUBLIC=./internal/assets/public ./ubuntu-panel --config /dev/null
npm run build   # in another shell, as often as you like
```

## Configuration

Read from `/etc/ubuntu-panel/panel.conf` as `KEY=value`, with the environment
taking precedence. Same file and same keys the C panel used.

| Key | Default |
| --- | --- |
| `PANEL_ROOT` | `/opt/ubuntu-panel` |
| `PANEL_DATABASE` | `/var/lib/ubuntu-panel/panel.db` |
| `PANEL_LOG` | `/var/log/ubuntu-panel/panel.log` |
| `PANEL_BIND_HOST` | `127.0.0.1` |
| `PANEL_BIND_PORT` | `8088` |
| `PANEL_SITES_ROOT` | `/var/www` |
| `PANEL_PUBLIC` | `$PANEL_ROOT/public` (only with `PANEL_DEBUG`) |
| `PANEL_DEBUG` | unset |

## Layout

```
cmd/ubuntu-panel      the server: configuration, logging, signals
internal/config       panel.conf and the environment
internal/store        SQLite: schema, users
internal/session      sessions, CSRF, flash messages
internal/inertia      the Inertia server protocol and the HTML shell
internal/web          routes, middleware, handlers
internal/assets       the built frontend, embedded
resources/js          the Vue frontend, unchanged from the C panel
```

## Ported so far

Setup, sign-in, sign-out, the dashboard, and the profile and password forms.
The database schema matches the C panel's, so the same `panel.db` can be served
by either binary while the rest moves across.

Still on the C build: sites, databases, email, DNS, apps, logs, the terminal and
system settings. Their Vue pages are already here; each needs the Go handler
that supplies its data. The navigation links to them and they answer with a page
saying so, rather than a broken one.
