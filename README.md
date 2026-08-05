# Ubuntu Panel

A self-hosted control panel that runs **on** the Ubuntu server it manages.

Install it on a fresh VPS with one command. It sets up nginx, PHP, Node and everything else
it needs, then gives you a web UI to host WordPress, Laravel, plain PHP, Node.js, Next.js
and static sites, manage databases, run real mailboxes, and get a root shell in the browser
— with DNS records created and deleted alongside your sites.

Built with Laravel 13, Inertia and Vue 3.

## Install

On a fresh Ubuntu 22.04 or 24.04 server, as root:

```bash
curl -fsSL https://raw.githubusercontent.com/shusanto294/Ubuntu-Panel/main/install.sh | sudo bash
```

That is the whole installation. It takes a few minutes and:

1. Installs nginx, PHP 8.3 (plus the extensions the panel needs), Composer, Node.js and git.
2. Creates an `ubuntupanel` system user with passwordless sudo, so the panel can manage the
   machine without the web process living in root's home.
3. Clones the panel into `/opt/ubuntu-panel`, installs dependencies and builds the assets.
4. Installs MariaDB, creates the panel's own database and user, writes `.env` and runs
   the migrations.
5. Installs three systemd services — queue worker, terminal server, scheduler timer.
6. Publishes the panel on **port 8443 over HTTPS** with a self-signed certificate, and opens
   the port in ufw if it is active.
7. Asks for an administrator email and password, then takes inventory of what is already
   installed on the machine.
8. Installs everything in the software catalogue — PostgreSQL, MongoDB, Redis, phpMyAdmin,
   PM2, WP-CLI, Certbot, the Postfix/Dovecot/OpenDKIM mail stack and Roundcube webmail
   included — so there is nothing left to click when it finishes.

That last step is the long one. It runs in the foreground rather than through the queue,
because an installer that hands back before the work is done cannot tell you whether it
worked. Every service is attempted even if one fails; the failures are named at the end and
the installer exits non-zero, but the panel itself is up either way and the failed rows can
be retried from its Software page.

Install less if you want to:

```bash
sudo bash install.sh --services default      # everything but PostgreSQL and MongoDB
sudo bash install.sh --services nginx,php,mysql
sudo bash install.sh --services none         # panel only
```

The only thing it asks for is the administrator email and password. When it finishes it
tells you exactly where to log in:

```
  Log in here:  https://YOUR.SERVER.IP:8443
  Username:     you@example.com
  Password:     the one you just chose

  Installed at: /opt/ubuntu-panel
  Database:     MariaDB, ubuntu_panel
  Services:     ubuntu-panel-queue, ubuntu-panel-terminal,
                ubuntu-panel-scheduler.timer
```

## Using your own domain

A fresh install answers on the server's IP with a self-signed certificate, so browsers warn
every time. Point a hostname at the machine (an `A` record to its IP), then either use
**Settings** in the panel, or run:

```bash
cd /opt/ubuntu-panel && sudo -u ubuntupanel php artisan panel:domain panel.example.com
```

It rewrites the vhost, installs certbot, issues a Let's Encrypt certificate, redirects HTTP
to HTTPS and updates `APP_URL`. Add `--email you@example.com` to get expiry warnings if
renewal ever breaks. The hostname must already resolve to the server — that is the usual
reason issuing fails, and the command warns before trying.

Sites you host get their own domains when you create them; this setting is only for the
panel itself.

### Options

Pass flags after `sudo bash`, or use `-s --` when piping:

```bash
curl -fsSL https://raw.githubusercontent.com/shusanto294/Ubuntu-Panel/main/install.sh \
  | sudo bash -s -- --port 9443 --email me@example.com --password 'a-good-password'
```

| Flag | Default | What it does |
| --- | --- | --- |
| `--port <n>` | `8443` | Port the panel is served on |
| `--dir <path>` | `/opt/ubuntu-panel` | Where the panel is installed |
| `--user <name>` | `ubuntupanel` | System user that runs it |
| `--php <version>` | `8.3` | PHP version to install |
| `--email <email>` | prompted | Administrator email |
| `--password <pw>` | prompted | Administrator password |
| `--branch <name>` | `main` | Branch to install from |

Running the installer again is safe: it updates the code, keeps your `.env` and database,
and leaves the administrator account alone.

### From a checkout

If you would rather read the script before running it:

```bash
git clone https://github.com/shusanto294/Ubuntu-Panel.git
cd Ubuntu-Panel
less install.sh
sudo bash install.sh
```

## Updating

One command, from anywhere on the server:

```bash
cd /opt/ubuntu-panel && sudo -u ubuntupanel php artisan panel:update
```

It checks what is published, and if you are behind it fetches the code, installs
dependencies, rebuilds the assets, runs migrations, refreshes the caches, and restarts the
services. If you are already current it says so and does nothing.

```bash
php artisan panel:update --force        # reinstall the current version anyway
php artisan panel:update --no-restart   # apply everything, restart later yourself
```

The restart is scheduled a few seconds out through `systemd-run`, not run as a child of the
update — otherwise the update would kill itself halfway through restarting the worker that
is running it.

Re-running the installer works too and is equivalent; it keeps your `.env`, database and
administrator account:

```bash
curl -fsSL https://raw.githubusercontent.com/shusanto294/Ubuntu-Panel/main/install.sh | sudo bash
```

### Knowing when there is an update

The panel's **Server** page shows the installed version and commit, and marks itself
`update available` when the repository has moved ahead. "Check for updates" re-asks
immediately; otherwise the answer is cached for an hour so the page never waits on GitHub.

Updating is deliberately a command rather than a button: it restarts the services that would
be serving the click, so it belongs somewhere you can watch it finish. The page shows the
exact command to paste — or run it straight from the panel's own **Terminal**.

### Publishing an update (maintainers)

**Bump `version` in `config/panel.php` in the same commit as every push to `main`. No
exceptions.**

Patch for a fix, minor for a feature, major for anything that changes how the panel is run.
The version is what an operator reads to know whether the thing in front of them is the
thing you shipped; a push that leaves it alone makes two different panels claim to be the
same one, and there is then no way to tell them apart from the outside.

Push to `main`. Every install compares its commit against the tip of that branch, so a push
is all it takes for panels to report an update. Cut a GitHub release and the version number
is compared instead of the commit — at which point the tag and `config/panel.php` have to
agree, or panels either miss updates or report one forever.

### Service management

```bash
sudo systemctl status ubuntu-panel-queue        # queued installs, deploys, mail, DNS
sudo systemctl status ubuntu-panel-terminal     # the browser terminal
sudo systemctl list-timers ubuntu-panel-*       # the scheduler
sudo journalctl -u ubuntu-panel-queue -f        # follow what the queue is doing
```

The queue worker is not optional: provisioning, deployments, database and mail work all run
as queued jobs. Without it, tasks sit at 0% forever. The terminal server is not optional
either if you want the browser shell or live task output — both travel over its websocket.

**Settings → Panel services** shows all three with their live state and restarts any of
them from the browser, which is the first thing to try when the terminal stops opening.
PHP-FPM is deliberately not on that list: the panel is being served by it, so restarting it
kills the request asking for the restart.

`panel:update` restarts all three, detached and a few seconds out, because it is usually
running inside one of them. It leaves an account of that in `/var/log/ubuntu-panel-restart.log`,
and `php artisan panel:doctor` reads it back along with each unit's current state — which is
the first thing to run when the terminal stops opening.

### Where the cache, sessions and queue live

The installer starts them on the database, because that is the one store guaranteed to exist
while the panel is being installed. It is also the most expensive: every page view becomes
session reads and writes against MariaDB, and the queue worker runs a locking `SELECT`
against it once a second for ever, on a machine where nothing is happening.

Once Redis is installed the installer moves all three onto it, and `panel:update` does the
same for a panel that predates this. It can also be done by hand:

```bash
php artisan panel:use-redis            # move them
php artisan panel:use-redis --revert   # put them back
```

It declines rather than guesses — no phpredis extension, no recorded Redis password, a Redis
that will not answer, or jobs still queued (they live in whichever store they were dispatched
to) and it changes nothing and says why. `php artisan panel:doctor` reports which stores are
in use.

## What it does

- **Settings** — put the panel on your own domain with a real certificate, and set the PHP
  and Node versions new sites inherit.
- **This server** — CPU, memory and disk updated every second, straight from `/proc`, pushed
  over a websocket. The reading costs microseconds; asking for it over HTTP once a second
  cost a framework boot each time, which was visible as a CPU spike every second on an
  otherwise idle machine. The daemon samples once for everyone and sends only what changed.
  If the socket cannot be reached the pages fall back to polling, more slowly.
- **Services** — install nginx, PHP-FPM, Composer, certbot, MariaDB, PostgreSQL,
  MongoDB, Redis, phpMyAdmin, Node.js, PM2, WP-CLI, a full mail server and Roundcube
  webmail, with live progress. Everything
  queued goes into a single apt transaction, because dpkg takes an exclusive lock and
  parallel installs would only serialise behind it. A service that fails costs you that
  service and nothing else.
- **Sites** — six types, each with its own deployment recipe:
  - **WordPress** — downloads core, creates the database and user, writes `wp-config.php`,
    runs `wp core install`, sets permalinks and hardens the vhost.
  - **Laravel** — fresh app or git clone, `composer install`, generates `.env` with real
    database credentials, `key:generate`, `storage:link`, `migrate`, caches config/routes.
  - **PHP / Static** — plain vhost, optional git clone, placeholder page otherwise.
  - **Node.js / Next.js** — git clone or starter app, `npm install`, build, a systemd unit
    on an auto-assigned port, and an nginx reverse proxy.
- **Databases** — create and drop databases and users on MariaDB, PostgreSQL and MongoDB.
  A **phpMyAdmin** button opens it already signed in over every database on the server; the
  credentials go into a session phpMyAdmin reads, never into the URL. Arriving at
  /phpmyadmin any other way gets the ordinary login form, where a database's own username
  and password reach that database and nothing else — which is all its MariaDB user is
  granted. The privileged account the panel signs you in as is its own, not MariaDB's
  `root`: root authenticates over a unix socket and has no password to give phpMyAdmin.
  Credentials are generated, encrypted and revealable in the UI. WordPress and Laravel sites
  get theirs automatically, dropped with the site.
- **Email** — Postfix + Dovecot + OpenDKIM with virtual mailboxes in MariaDB. Add a domain
  and the panel generates its DKIM key; add addresses with quotas and they work over
  IMAP/SMTP immediately. With a DNS provider connected it publishes A, MX, SPF, DKIM,
  DMARC, TLS-RPT and the autoconfig records in one go; without one it shows you the records
  to add.

  Each domain gets **Roundcube webmail at `mail.<domain>`** — its own vhost, its own A
  record and its own Let's Encrypt certificate, issued automatically. That certificate is
  then handed to Postfix and Dovecot over SNI, so `mail.<domain>` is a hostname an
  application can point its mailer at with certificate verification left on. The page shows
  the SMTP and IMAP host, port and encryption for each domain, ready to paste into a
  config.

  The one thing the panel cannot publish is the **reverse DNS (PTR)** for the server's own
  address — that belongs to whoever rents you the IP, and a missing one costs a point in
  every deliverability check. Set it to the mail hostname in your provider's control
  panel.
- **Terminal** — a real root login shell in the browser, backed by a pty. Tab completion,
  shell history, colours, Ctrl+C, `vim`, `top` and `htop` all behave normally.
- **DNS** — connect **Cloudflare, DigitalOcean, Linode, Vultr, Hetzner DNS** or
  **Porkbun**. Credentials are tried before they are saved and stored encrypted; records for
  a site and its aliases are then created, updated and deleted with it. Anywhere else works
  too — leave DNS management off and point an A record at the server yourself.

  Cloudflare gets one thing the others do not: certificates are issued over DNS-01 when a
  Cloudflare credential is attached, so the orange cloud can stay on. Everywhere else the
  origin answers directly and the webroot challenge is simpler.
- **Activity log** — every install, deployment, database, mail and DNS action, with full
  command output.

## How it works

The panel manages its own host, so there is no SSH anywhere: a `LocalConnection` runs
commands through `bash -c` with `sudo`, and writes files through a staged temp file with a
checksum read back afterwards. A command costs a fork instead of a network round trip.

Long-running work runs on the queue and streams into a live console in the browser. Nothing
in an HTTP request ever waits on a system command, so pages always load immediately.

Requirements are handled by the installer; for reference: Ubuntu 22.04/24.04, PHP 8.3+,
Composer 2, Node 20+.

## Development

Working on the panel itself, on a machine that is not a target server:

```bash
composer install
npm install
php artisan key:generate
touch database/database.sqlite
php artisan migrate
composer dev
```

`composer dev` runs the web server, queue worker, scheduler, terminal server and Vite
together. Then open http://127.0.0.1:8000 and register an account.

Some features shell out to Ubuntu-only tooling (`apt-get`, `systemctl`, `/proc`), so those
paths only do anything real on a Linux host.

```bash
php artisan test          # test suite
./vendor/bin/pint         # code style
```

## Project status

Young project, and honest about it:

- **The installer has not yet been run end to end on a clean VPS.** Every step is
  individually conventional and the script is syntax-checked, but expect the PHP-FPM pool
  and nginx vhost steps to be where something needs adjusting first.
- The panel was recently converted from a multi-server tool (managing remote machines over
  SSH) into a single-server one. The application, the pages and the installer are converted
  and every page renders; **part of the test suite still asserts the old shape** and is
  being finished.

## Security notes

- The panel's system user has passwordless sudo — it installs packages and edits system
  configuration, so it needs root. **Treat access to the panel as root access to the
  machine.**
- The browser terminal is a root shell. Use a strong administrator password, and keep the
  panel behind a firewall or a trusted hostname rather than exposing 8443 to the world
  indefinitely.
- Service passwords the panel generates (MongoDB, Redis, the mail database) are encrypted
  with `APP_KEY`. Back up `.env`; losing `APP_KEY` means losing those secrets.
- `.env` and the SQLite database are gitignored and never published.

## License

MIT.
