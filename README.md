# Ubuntu Panel — Go

A server control panel: one static binary with the frontend inside it, nothing
to install on the machine it runs on, and a SQLite file for state.

Sites, databases, email, DNS, apps and logs, on one machine, from a browser.
This is the Go rewrite of the C panel: the frontend came across unchanged — the
same Vue pages, the same Inertia protocol, the same route names — and the
database schema is the same, so the same `panel.db` can be served by either
binary.

## Install

One command on a fresh Ubuntu server:

```sh
curl -fsSL https://raw.githubusercontent.com/shusanto294/Ubuntu-Panel-Go/main/get.sh | sudo bash
```

It downloads the release built for this machine's architecture, checks it
against the published SHA-256, and runs the installer inside it. Nothing is
compiled on the server: no Go, no Node, no compiler, and no build memory on a
machine whose job is to serve sites. The install is a download, a file copy and
a systemd unit.

Then open the server's address **with the panel's port on the end** —
`http://your-server:8443` — and with no account yet it asks you to create the
first one.

The port is the point. Ports 80 and 443 belong to the sites this machine is
for, so the bare address answers "No site is configured for this hostname"
rather than a control panel's login form: nobody scanning the address is told
there is a panel here, let alone shown the way in. `PANEL_PUBLIC_PORT=9443`
picks a different one, Settings → Panel port moves it later, and either way the
installer opens it in ufw — a firewall at your host, a security group or a
network rule, has to allow it there as well.

The installer puts the software a server needs on the machine before it starts
the panel — nginx, PHP-FPM, MariaDB, Certbot, WP-CLI, Node and the rest of the
Apps page — so the first sign-in lands on a server that is finished. That is
the long part of the install: five to fifteen minutes on a small VPS, printed
step by step as it goes. Set `PANEL_INSTALL_APPS=core` for only what a site
cannot run without, or `none` to choose them yourself. It happens once, on a
machine the panel has never installed an app on, so removing one afterwards is
a decision the panel keeps.

Options are environment variables, or flags after `bash -s --`:

```sh
curl -fsSL https://raw.githubusercontent.com/shusanto294/Ubuntu-Panel-Go/main/get.sh | sudo PANEL_PORT=9000 bash
curl -fsSL https://raw.githubusercontent.com/shusanto294/Ubuntu-Panel-Go/main/get.sh | sudo bash -s -- --from-source
curl -fsSL https://raw.githubusercontent.com/shusanto294/Ubuntu-Panel-Go/main/get.sh | sudo bash -s -- --version v1.2.0
```

`--version` pins a release. `--from-source` is the escape hatch: it clones and
compiles on the server, which means installing Go and Node there, and it is
never chosen automatically — a one-minute install silently becoming a
five-minute one that leaves a toolchain behind is not a fallback anybody asked
for. `PANEL_ROOT`, `PANEL_USER` and `PANEL_SKIP_APT` work the same way.

What it leaves behind: the binary at `/opt/ubuntu-panel/bin/ubuntu-panel`, the
configuration at `/etc/ubuntu-panel/panel.conf` (including `PANEL_APP_KEY` —
back it up with the database), the database at
`/var/lib/ubuntu-panel/panel.db`, a systemd unit, and an nginx vhost proxying
port 8443 to the panel on 127.0.0.1:8088.

Set a hostname from Settings and it gets a Let's Encrypt certificate and serves
the panel over HTTPS on the same port. The hostname's ports 80 and 443 keep the
ACME challenge and a redirect to it, so an old bookmark still lands somewhere
useful — an address nobody has been told still answers nothing.

## Update

The same command:

```sh
curl -fsSL https://raw.githubusercontent.com/shusanto294/Ubuntu-Panel-Go/main/get.sh | sudo bash
```

It backs the database up first, keeps the database, the application key, the
port and the panel's own nginx vhost, and swaps the binary atomically — if the
new one does not answer on `/up` within thirty seconds it puts the old one back
and restarts it, so a bad update is a no-op rather than an outage.

From a checkout on the server, `sudo ./install.sh` does the same thing without
going to GitHub.

Settings → Panel update runs the same script in a transient systemd unit, which
is what lets the panel replace and restart itself without killing the process
doing it.

## What it does

Everything the C panel does, from the same pages:

- **Dashboard** — CPU, memory, disk and load from `/proc`, live and as history
- **Apps** — install and remove nginx, PHP-FPM, MariaDB, MongoDB, Redis, Node,
  Composer, WP-CLI, PM2, phpMyAdmin, Certbot, and a Postfix/Dovecot/OpenDKIM
  mail server with Roundcube. A bulk install puts every app's packages into one
  apt transaction, because dpkg takes an exclusive lock
- **Sites** — WordPress, Laravel, PHP, Node.js, Next.js and static, with git
  deployment, an nginx vhost, a systemd unit for the proxied types, DNS and a
  Let's Encrypt certificate
- **Databases** — MariaDB and MongoDB, with generated credentials
- **Email** — mail domains and mailboxes in Postfix/Dovecot's SQL tables, DKIM
  keys, the SPF/DKIM/DMARC/MX records to go with them, and webmail per domain
- **DNS** — Cloudflare, DigitalOcean, Linode, Vultr, Hetzner and Porkbun, with
  a full zone editor
- **Logs** — every deployment and install streams its console to the page
- **Terminal** — a real shell in the browser, on a PTY over a websocket

And one thing the C panel does not:

- **AI access** — the panel is an MCP server. Point Claude Code, Codex or any
  other Model Context Protocol client at `https://your-panel/mcp` with a key
  issued on the AI access page, and the assistant can create a site from a
  GitHub repository, deploy a WordPress or Next.js site, read a failing
  deployment's console, edit a site's files, create databases, install what the
  machine is missing, and run commands on the server — twenty tools, each one a
  call onto the same service the panel's own pages use.

  What it may do is a checklist on that page, from "look around" to the shell,
  with an Everything option and a switch for the whole server. A second switch
  makes anything destructive — deleting a site, replacing a file, a command
  that is not plainly read-only — refuse the first attempt and tell the
  assistant to put it to you in words first. Every command it runs is in the
  panel's own Logs, with its output

The database schema matches the C panel's, so the same `panel.db` can be served
by either binary.

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

## Build and release

```sh
make            # frontend, then the binary with the frontend embedded
make test
make release    # dist/*.tar.gz for amd64 and arm64, plus their SHA-256 files
```

`CGO_ENABLED=0` throughout: the SQLite driver is pure Go, so cross-compiling for
a server from any machine needs no toolchain, and the result is a static binary
with nothing to resolve on the machine it lands on. That is what lets the
install be a download.

Releases are built by GitHub rather than by hand. Push a tag and
`.github/workflows/release.yml` runs the tests, builds both architectures,
checks that each binary really is statically linked, and attaches the tarballs
to the release:

```sh
git tag v1.0.0 && git push origin v1.0.0
```

`get.sh` then finds that release and installs it. There is also a
`workflow_dispatch` trigger, for building a release from a branch without
tagging it.

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
| `PANEL_APP_KEY` | — (written by the installer; encrypts every stored credential) |
| `PANEL_WORKERS` | `2` (`1` on a machine under 1.5 GB) |
| `PANEL_INSTALL_APPS` | `all` — what the installer puts on the machine before starting the panel: `all`, `core` or `none` |
| `PANEL_REPOSITORY` | `https://github.com/shusanto294/Ubuntu-Panel-Go` |
| `PANEL_UPDATE_BRANCH` | `main` |
| `PANEL_DEBUG` | unset |

The panel keeps its own state in SQLite while the sites and mailboxes it
manages use MariaDB, and that is deliberate rather than a stage on the way to
something else. The largest table the panel owns is `metric_samples`: one row a
minute, pruned at 35 days, so about 50,000 rows and a few megabytes however
long the server runs. Everything else is one row per site, database, mailbox or
task. What SQLite buys against that is the thing that matters on a control
panel — it always starts. A panel whose own state lived in MariaDB could not
come up while MariaDB was down, which is exactly when you would want to open
the panel and look at MariaDB.

`PANEL_APP_KEY` is 32 random bytes, base64, and every stored credential — DNS
API tokens, database and mailbox passwords — is encrypted with it, in the same
AES-256-GCM form the C panel used. Losing it, or changing it, makes all of them
unreadable; the panel starts anyway and says so on the pages that need one.

## Layout

```
cmd/ubuntu-panel      the server: configuration, logging, signals
internal/config       panel.conf, the environment, and the site/engine tables
internal/store        SQLite: schema and every model
internal/session      sessions, CSRF, flash messages
internal/inertia      the Inertia server protocol and the HTML shell
internal/web          routes, middleware, handlers, the job registry
internal/mcp          the Model Context Protocol server an assistant connects to
internal/crypto       the application key: AES-256-GCM over stored credentials
internal/proc         running commands, quoting, writing files
internal/tasks        the step runner behind every live console
internal/queue        the jobs table and its workers
internal/catalog      what the panel can install, and how
internal/sites        nginx vhosts, systemd units, the per-type deploy recipes
internal/databases    creating and dropping databases on the installed engines
internal/dns          six DNS providers behind one interface
internal/mail         Postfix, Dovecot, OpenDKIM and Roundcube
internal/system       /proc, the public IP, the panel's own hostname
internal/terminal     the browser shell, and the live stream sharing its socket
internal/assets       the built frontend, embedded
resources/js          the Vue frontend, unchanged from the C panel
```

## How the work runs

Anything that takes longer than a request is a row in `jobs`, picked up by a
worker. A worker runs a **task**: a named list of steps whose console the page
polls while it happens. A step is either a list of shell commands or a
function, and steps can be grouped — when a grouped step fails, the rest of
that group is abandoned and everything else carries on, so installing nine apps
does not stop at the first one that will not configure.

A job left running when the panel stopped goes back on the queue at startup;
every handler is written to be safe to run twice.

One websocket carries the rest. The dashboard subscribes to `metrics` and a
running task subscribes to `task:<id>`, so a reading a second costs a read of
/proc rather than a request; the pages fall back to polling when the socket
cannot be reached. The same socket carries the terminal, which is a PTY rather
than a subscription. It authenticates with a one-shot ticket minted by an
authenticated request, because a cross-scheme upgrade does not reliably carry
the session cookie.

Two rules hold everywhere a command is built: the command string is always a
literal in this codebase and user input reaches it only through `proc.Quote`,
and files are written by staging and copying rather than by echoing contents
through a command line — so nothing with a quote, a newline or a `$` in it can
be mangled, and no password ever appears in `ps`.
