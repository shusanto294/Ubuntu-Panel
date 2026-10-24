# Ubuntu Panel

A server control panel: one static binary with the frontend inside it, nothing
to install on the machine it runs on, and a SQLite file for state.

Sites, databases, email, DNS, apps and logs, on one machine, from a browser.
It is written in Go, and it is the panel that replaced an earlier C one: the
frontend came across unchanged — the same Vue pages, the same Inertia protocol,
the same route names — and the database schema is the same, so the same
`panel.db` can be served by either binary.

## Install

One command on a fresh Ubuntu server:

```sh
curl -fsSL https://raw.githubusercontent.com/shusanto294/Ubuntu-Panel/main/get.sh | sudo bash
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
curl -fsSL https://raw.githubusercontent.com/shusanto294/Ubuntu-Panel/main/get.sh | sudo PANEL_PORT=9000 bash
curl -fsSL https://raw.githubusercontent.com/shusanto294/Ubuntu-Panel/main/get.sh | sudo bash -s -- --from-source
curl -fsSL https://raw.githubusercontent.com/shusanto294/Ubuntu-Panel/main/get.sh | sudo bash -s -- --version v1.2.0
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
curl -fsSL https://raw.githubusercontent.com/shusanto294/Ubuntu-Panel/main/get.sh | sudo bash
```

It backs the database up first, keeps the database, the application key, the
port and the panel's own nginx vhost, and swaps the binary atomically — if the
new one does not answer on `/up` within thirty seconds it puts the old one back
and restarts it, so a bad update is a no-op rather than an outage.

It also updates the machine, not only the panel: apt is asked for the newest of
everything the panel installed — PHP, MariaDB, nginx, Node, Redis, the mail
server — and the newest PHP the panel knows about is installed if it is not
there yet, becoming the default for new sites. Only the panel's own packages,
because `apt-get upgrade` on somebody's server is a larger promise than this
makes; and only the default moves, because a site that works on PHP 8.3 is not
something to put on 8.5 while nobody is watching. Each site's PHP tab moves
that one. `PANEL_UPDATE_APPS=0` leaves the machine exactly as it was.

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
- **Sites** — WordPress, Laravel, PHP, Node.js, Next.js, Nuxt and static, with
  git deployment from public or private repositories — a generated deploy key or
  a read-only token — and a pull on a button, on a push, or on an interval, an nginx vhost, a systemd unit for the proxied types, DNS and a
  Let's Encrypt certificate. Each PHP site picks its own PHP version on its PHP
  tab — a version the machine already runs is a vhost rewrite and a reload, one
  it does not is installed first, on the queue, with the console on the page
- **Staging** — clone a site to a hostname of its own, break it, and push it
  back. The copy gets its own files, database and certificate, and every
  absolute URL in it is rewritten — for WordPress that means WP-CLI's own
  search and replace over every table, in plain, protocol-relative and
  JSON-escaped form and correctly through serialised PHP, so the copy does not
  load and then redirect whoever is editing it back to production. A push backs
  the live site up first and does not start if that backup fails
- **Databases** — MariaDB and MongoDB, with generated credentials
- **Email** — mail domains and mailboxes in Postfix/Dovecot's SQL tables, DKIM
  keys, the SPF/DKIM/DMARC/MX records to go with them, and webmail per domain
- **DNS** — two ways round. Either the panel writes records at somebody else's
  nameserver — Cloudflare, DigitalOcean, Linode, Vultr, Hetzner or Porkbun,
  with a full zone editor — or this machine *is* the nameserver: BIND serving
  zones held in the panel's own tables, the way Namecheap and GoDaddy serve
  theirs. Either one is a DNS provider to the rest of the panel, so a site gets
  its records published and a mail domain gets its SPF, DKIM and DMARC written
  whichever you picked. The nameserver page is mostly instructions, because the
  half the panel cannot do — registering two nameserver names as glue records
  at a registrar — is the half that decides whether any of it resolves
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

## Deploying from git

A site can be deployed from any git host over https or SSH, public or private,
and the Git tab on the site's page is where all of it lives: the URL and branch,
how the panel authenticates, a connection test, a pull button, and what should
happen when somebody pushes.

### Private repositories

Two ways, and the panel prefers the first:

- **A deploy key.** The panel generates an ed25519 pair per site, keeps the
  private half outside every site's directory under `/var/lib/ubuntu-panel`, and
  shows you the public half to add to the repository — on GitHub that is the
  repository's *Settings → Deploy keys*. Read-only, scoped to one repository,
  and revoked by deleting it there. ssh is given that key and only that key
  (`IdentitiesOnly=yes`), never prompts (`BatchMode=yes`), and pins the host key
  after the first connection (`accept-new`).
- **An access token.** Stored encrypted with the application key and never sent
  back to the browser. Use it where a deploy key is not an option. The token is
  passed per command and the stored remote is scrubbed to the clean URL, so it
  is never written into `.git/config` — which would otherwise put it in the
  working tree, in every backup of the site, and in reach of every other site on
  the machine. It is redacted out of the console the panel renders.

Either way the repository is checked before the site is created, with
`git ls-remote` — a few hundred bytes and a second. That turns a deployment that
fails on its fourth step, with a vhost and a database already built, into a
sentence under the form field. The check also reports which branches the
repository *does* have, because `main` against a repository whose default is
still `master` is the second commonest way this goes wrong.

Git's own words are translated rather than passed through, because git is
answering a different question: "could not read from remote repository" is what a
private repository says to a client with no key, and "repository not found" is
what GitHub says to a perfectly valid token that has no access to it. Neither
reads as the thing to go and fix.

### Pulling

**On a button** — Pull now, on the Git tab. Fetches the branch and resets the
working tree to it, then runs what the type needs: Composer and migrations for
Laravel, npm and a rebuild and a service restart for the Node types. Tracked
files changed on the server are lost, which is what makes a deployment
repeatable; untracked ones — uploads, `.env`, `node_modules` — are left alone.

**On a push** — a webhook. The panel generates a secret and shows the URL and
the secret to paste into the repository's settings. A delivery is acted on only
when it carries a signature made with that secret: GitHub's `X-Hub-Signature-256`
(HMAC-SHA256 over the body), GitLab's `X-Gitlab-Token`, or a plain token for
anything else, all compared in constant time. Only a push to the site's own
branch deploys, so a feature branch pushed for review cannot land on production.

**On an interval** — every 5 or 15 minutes, hourly or daily. The panel asks the
remote for the branch's commit and pulls only when it has changed, so an
interval on a repository nobody touched costs one small request. This is the one
that needs nothing configured anywhere else, and it is the backstop for the
webhook delivery that arrived while the panel was restarting — the two are not
alternatives and most sites want both.

## Staging sites

A staging site is an ordinary site row with `staging_of` pointing at the one it
was copied from. That is most of the design: it gets a vhost, a database, a
certificate, a PHP version picker, backups, the file manager and a terminal
without a line of extra code, because it *is* a site. What the Staging tab adds
is the two operations that keep the pair in step.

**Creating one** copies the files with `cp -a` into a directory beside the
destination and swaps it in, dumps the source's database and loads it into a new
one of the copy's own, then re-points the application. The copy deliberately
does not inherit:

- **the aliases** — production's hostnames stay on production. Two vhosts
  claiming one `server_name` and nginx hands it to whichever it loaded first, so
  production would quietly start being served out of the staging directory
- **the cache** — every site on the machine shares one Redis and one nginx cache
  zone, so a copy carrying production's cache configuration would be serving
  production's cached pages
- **indexing** — on WordPress the copy sets `blog_public` to 0, because a
  staging site is a complete second copy of somebody's content on a hostname
  reachable from the internet

**Pushing back** takes a backup of the live site first and does not start if that
backup fails. Files and database are separate choices: files alone is the
ordinary push, and the database is off by default because turning it on destroys
everything written to production since the copy was taken. On WordPress a files
push keeps production's `wp-content/uploads` by default — anything uploaded there
since the clone exists in no backup of staging.

### Why URLs are the hard part

An application that stores absolute URLs comes up on the new hostname still
convinced it lives at the old one. That does not look like a broken site: it
loads, and then redirects whoever is editing it back to production — so the
person thinks they are working on staging and is in fact editing the live site.

For WordPress that means WP-CLI's `search-replace` over **every** table,
`--precise` so the length prefixes inside serialised PHP are rewritten rather
than corrupted, and six passes rather than one, because the same URL is stored
as `https://example.com` in an option, as `https:\/\/example.com` inside a block
editor's JSON, and as `//example.com` in a theme's asset — and a replacement
looking for the first matches neither of the others. The scheme-ful forms run
first so the destination's own scheme wins; `guid` is skipped, because it looks
like a URL and is a permanent post identifier that feed readers dedupe on.

Then `home` and `siteurl`, the `WP_HOME` and `WP_SITEURL` constants where the
install defines them (they override the options entirely), `DOMAIN_CURRENT_SITE`
on a multisite network, and a flush of the permalinks, the transients and the
object cache.

Two things happen at opposite ends of the copy, and it matters which:

- **the database credentials** are re-pointed the moment the files are in place,
  before the vhost exists. Until then the copy's `wp-config.php` names the
  *source's* database, and a live hostname in front of that is production under
  a second name
- **the URLs** are rewritten after the certificate step, because what goes into
  the database is a scheme as well as a hostname. A certificate that fails to
  issue leaves the site on HTTP, and `https://` URLs written before anybody knew
  that are assets nothing answers

Laravel is the short version of the same job: `APP_URL` and the `DB_*` keys in
`.env`, rewritten line by line so the API keys and comments around them survive,
then `config:clear` and `config:cache` because `config:cache` had compiled the
old values into a PHP file. `APP_KEY` is left alone, so anything the source
encrypted still decrypts.

Nextcloud and n8n are refused rather than half-supported: Nextcloud records the
absolute path of its data directory inside itself, and n8n's workflows would fire
every schedule and webhook a second time.

## Hosting DNS on this machine

The panel can be the nameserver itself rather than a client of one. Install the
**DNS server** app and the machine runs BIND, authoritative for the zones in the
panel's own tables and recursive for nobody — an open resolver on a public
address is found within days and used to reflect traffic at somebody else, so
`recursion no` is not negotiable and is written before the service is started.

Four of the five steps are in the panel, on **DNS → This server**. The one that
is not is the one everything depends on.

1. **Install the DNS server** from the Apps page. Port 53 is opened on TCP *and*
   UDP: a query goes over UDP, but an answer too large for one datagram goes
   over TCP, so a firewall that opens only UDP gives you a nameserver that works
   until an answer grows. If your host has its own firewall in front of the
   machine, open 53 there too.

2. **Name the nameservers.** Two hostnames on a domain you own and intend to
   keep — `ns1.example.com` and `ns2.example.com` by convention. Every domain
   hosted here is pointed at those two names, so losing that domain takes all of
   them down. It does not have to be a domain hosted in the panel.

3. **Register them as glue records at that domain's registrar.** This is the
   step the panel cannot do, and the reason it exists is worth knowing. A
   resolver looking up `yoursite.com` asks the `.com` registry who serves it and
   is told `ns1.example.com` — a *name*, so it now needs that name's address.
   If the only machine that knows the address is the one it is trying to reach,
   it can never get started. A glue record is the registry storing the address
   alongside the delegation, so the answer arrives in the same reply.

   Every registrar calls the screen something different: glue record, host
   record, child nameserver, private nameserver, personal DNS server, "register
   a nameserver". Namecheap has it under *Advanced DNS → Personal DNS Server*,
   GoDaddy under *DNS → Hostnames*, Porkbun under *Details → Glue Records*.
   Cloudflare Registrar does not offer it at all, so name your nameservers on a
   domain held somewhere that does.

   Two nameservers on one machine means two names pointing at one address. Most
   registries accept that; some require the two to differ and a few require
   different networks. If yours refuses the second, give the server a second
   address or point `ns2` at another machine — a delegation to a nameserver that
   does not answer is worse than one that lists a single name, because resolvers
   pick at random and the domain then works intermittently.

4. **Host the nameservers' own domain here too.** The glue gets a resolver here
   the first time; after that it asks *this* machine for the addresses of `ns1`
   and `ns2` and believes the answer, so the machine has to have one. Add
   `example.com` as a zone and the panel writes those A records itself. A zone
   that delegates to names it holds no addresses for is the lame delegation
   every DNS checker reports and some registries reject.

5. **Point each domain at the nameservers.** Add the zone in the panel *first*,
   then change the nameservers at that domain's registrar. The other order
   points a domain at a machine that answers "no such domain", and that answer
   gets cached. Nameserver changes propagate over hours, and the old
   nameservers keep answering for whoever has them cached — so leave the
   records at the old host in place until traffic has moved.

The nameserver page tracks which of these is done, and asks a public resolver
what the nameserver names and each zone's delegation actually resolve to, which
is the only check that means anything: the panel accepting the settings says
nothing about what a registry is handing out.

After that it is a DNS provider like any other. "This server" appears wherever
one does, so creating a site publishes its records and issues its certificate,
a mail domain gets its MX, SPF, DKIM and DMARC written straight into the zone,
and deleting either takes the records back out. The zones are rows in
`dns_zones` and `dns_zone_records`; the files under `/etc/bind` are generated
from them and validated with `named-checkzone` before anything is reloaded, so
a record that would produce an unloadable zone is an error on the form rather
than a change that appeared to work and did nothing.

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
| `PANEL_REPOSITORY` | `https://github.com/shusanto294/Ubuntu-Panel` |
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
internal/sites        nginx vhosts, systemd units, the per-type deploy recipes,
                      backups, and the staging clone/push with its URL rewriting
internal/databases    creating and dropping databases on the installed engines
internal/dns          seven DNS providers behind one interface, one of them this machine
internal/nameserver   BIND: zone files generated from the panel's own rows
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

## Licence

MIT — see [LICENSE](LICENSE). Use it, change it, run it for clients, sell
hosting on it, fork it and rename it; keep the copyright notice with the copy.
There is no licence key, no activation, and nothing in the panel phones home to
ask whether it may run.

A release is one binary with the frontend embedded, so it carries its
dependencies with it. They are MIT, BSD and MPL-2.0, and they are listed with
their notices in [THIRD-PARTY.md](THIRD-PARTY.md). The software the panel
installs on your server — nginx, PHP, MariaDB, MongoDB, Postfix, WordPress and
the rest — is not part of this project and stays under its own licence.
