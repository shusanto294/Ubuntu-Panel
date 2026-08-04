#!/usr/bin/env bash
#
# Ubuntu Panel installer.
#
# Installs the panel and everything it needs on the machine you run it on, then
# hands you a URL and an administrator account. Safe to run again: every step
# checks before it acts.
#
#   sudo bash install.sh
#   sudo bash install.sh --port 9000 --email me@example.com --password 'secret123'
#
set -eu

trap 'printf "\n\033[0;31mInstall failed\033[0m at line %s: %s\n" "$LINENO" "$BASH_COMMAND" >&2' ERR

PANEL_USER="${PANEL_USER:-ubuntupanel}"
PANEL_DIR="${PANEL_DIR:-/opt/ubuntu-panel}"
PANEL_PORT="${PANEL_PORT:-8443}"
PHP_VERSION="${PHP_VERSION:-8.3}"
BRANCH="${PANEL_BRANCH:-main}"
NODE_MAJOR="${NODE_MAJOR:-22}"
ADMIN_EMAIL=""
ADMIN_PASSWORD=""
PANEL_SERVICES="${PANEL_SERVICES:-all}"
REPO="${PANEL_REPO:-https://github.com/shusanto294/Ubuntu-Panel.git}"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --port) PANEL_PORT="$2"; shift 2 ;;
        --dir) PANEL_DIR="$2"; shift 2 ;;
        --user) PANEL_USER="$2"; shift 2 ;;
        --php) PHP_VERSION="$2"; shift 2 ;;
        --repo) REPO="$2"; shift 2 ;;
        --branch) BRANCH="$2"; shift 2 ;;
        --email) ADMIN_EMAIL="$2"; shift 2 ;;
        --password) ADMIN_PASSWORD="$2"; shift 2 ;;
        --services) PANEL_SERVICES="$2"; shift 2 ;;
        -h|--help)
            cat <<'USAGE'
Ubuntu Panel installer

  --port <n>        Port to serve the panel on (default 8443)
  --dir <path>      Where to install it (default /opt/ubuntu-panel)
  --user <name>     System user to run it as (default ubuntupanel)
  --php <version>   PHP version to install (default 8.3)
  --repo <url>      Git source to clone when not run from a checkout
  --branch <name>   Branch to clone (default main)
  --email <email>   Administrator email (prompted if omitted)
  --password <pw>   Administrator password (prompted if omitted)
  --services <what> Software to install: all (default), default, or a
                    comma-separated list of keys, e.g. nginx,php,mysql
USAGE
            exit 0 ;;
        *) echo "Unknown option: $1" >&2; exit 1 ;;
    esac
done

log()  { printf '\n\033[1;33m==>\033[0m \033[1m%s\033[0m\n' "$*"; }
ok()   { printf '    \033[0;32m✓\033[0m %s\n' "$*"; }
die()  { printf '\n\033[0;31mError:\033[0m %s\n' "$*" >&2; exit 1; }

[[ $EUID -eq 0 ]] || die "Run this as root: sudo bash install.sh"
[[ -f /etc/os-release ]] || die "This installer is for Ubuntu."
. /etc/os-release
[[ "${ID:-}" == "ubuntu" ]] || die "This installer is for Ubuntu (found ${PRETTY_NAME:-unknown})."

export DEBIAN_FRONTEND=noninteractive

# A third-party repository that publishes nothing for this release (an old
# MongoDB or PHP list, say) makes apt-get update exit non-zero. That is not a
# reason to abandon the install, so refreshing is advisory: if a package we
# actually need is missing, the install step below says so properly.
apt_update() {
    if ! apt-get update -qq 2>/tmp/panel-apt-update.log; then
        printf '    \033[0;33m!\033[0m some apt repositories failed to refresh (ignored):\n'
        grep -E "^(E|W):" /tmp/panel-apt-update.log | sed 's/^/      /' | head -5 || true
    fi
    return 0
}

have_package() {
    apt-cache show "$1" >/dev/null 2>&1
}

# ---------------------------------------------------------------- packages ---
log "Installing what the panel needs"

apt_update
apt-get install -y -qq software-properties-common curl git unzip ca-certificates gnupg ufw >/dev/null
ok "base tools"

# PHP: prefer the requested version. Ubuntu ships exactly one, and ondrej's PPA
# carries the rest — but it does not publish for every release the day it lands,
# so this falls back to whatever this Ubuntu does offer rather than failing.
if ! have_package "php${PHP_VERSION}-fpm"; then
    add-apt-repository -y ppa:ondrej/php >/dev/null 2>&1 || true
    apt_update
fi

if ! have_package "php${PHP_VERSION}-fpm"; then
    DISTRO_PHP="$(apt-cache depends php-fpm 2>/dev/null | awk -F'php' '/Depends: php[0-9]/ {print $2; exit}' | cut -d- -f1 || true)"

    if [[ -n "${DISTRO_PHP:-}" ]] && have_package "php${DISTRO_PHP}-fpm"; then
        printf '    \033[0;33m!\033[0m php%s is not available on %s; using php%s instead\n' \
            "$PHP_VERSION" "${VERSION_CODENAME:-this release}" "$DISTRO_PHP"
        PHP_VERSION="$DISTRO_PHP"
    else
        die "No PHP-FPM package is available (looked for php${PHP_VERSION}-fpm and the distro default). Pass --php <version>."
    fi
fi

apt-get install -y -qq nginx mariadb-server \
    "php${PHP_VERSION}-fpm" "php${PHP_VERSION}-cli" "php${PHP_VERSION}-mbstring" \
    "php${PHP_VERSION}-xml" "php${PHP_VERSION}-curl" "php${PHP_VERSION}-zip" \
    "php${PHP_VERSION}-sqlite3" "php${PHP_VERSION}-mysql" "php${PHP_VERSION}-bcmath" \
    "php${PHP_VERSION}-intl" "php${PHP_VERSION}-gd" >/dev/null

# Not packaged separately on every release; the terminal needs it for its pty.
apt-get install -y -qq "php${PHP_VERSION}-posix" >/dev/null 2>&1 || true

ok "nginx, MariaDB and PHP ${PHP_VERSION}"

if ! command -v composer >/dev/null; then
    curl -fsSL https://getcomposer.org/installer -o /tmp/composer-setup.php
    php /tmp/composer-setup.php --install-dir=/usr/local/bin --filename=composer --quiet
    rm -f /tmp/composer-setup.php
fi
ok "composer $(composer --version --no-ansi 2>/dev/null | awk '{print $3}' || true)"

if ! command -v node >/dev/null; then
    curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash - >/dev/null 2>&1 || true
    apt-get install -y -qq nodejs >/dev/null 2>&1 || true
fi

if ! command -v node >/dev/null; then
    # NodeSource has no build for this release yet: the distro's Node is fine
    # for building the panel's assets.
    apt-get install -y -qq nodejs npm >/dev/null
fi
command -v node >/dev/null || die "Could not install Node.js."
ok "node $(node --version)"

# ------------------------------------------------------------------- user ----
log "Creating the ${PANEL_USER} system user"

if ! id -u "$PANEL_USER" >/dev/null 2>&1; then
    useradd --system --create-home --home-dir "/home/${PANEL_USER}" --shell /bin/bash "$PANEL_USER"
fi

# The panel manages this machine: installs packages, writes vhosts, restarts
# services. It needs root, and it must never sit waiting for a password prompt.
cat > /etc/sudoers.d/ubuntu-panel <<SUDOERS
# Managed by the Ubuntu Panel installer
${PANEL_USER} ALL=(ALL) NOPASSWD: ALL
Defaults:${PANEL_USER} !requiretty
SUDOERS
chmod 440 /etc/sudoers.d/ubuntu-panel
visudo -cf /etc/sudoers.d/ubuntu-panel >/dev/null || die "Wrote an invalid sudoers file."
ok "${PANEL_USER} can run privileged commands without a password"

# ------------------------------------------------------------------ source ---
log "Putting the panel in ${PANEL_DIR}"

SOURCE_DIR=""
if [[ -n "${BASH_SOURCE[0]:-}" && -f "${BASH_SOURCE[0]}" ]]; then
    SOURCE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
fi

if [[ -n "$SOURCE_DIR" && -f "${SOURCE_DIR}/artisan" && "${SOURCE_DIR}" != "${PANEL_DIR}" ]]; then
    mkdir -p "$PANEL_DIR"
    # Copy the checkout this script came from, minus anything machine-specific.
    tar -C "$SOURCE_DIR" \
        --exclude=.git --exclude=node_modules --exclude=vendor \
        --exclude=.env --exclude='database/*.sqlite' --exclude=storage/logs \
        -cf - . | tar -C "$PANEL_DIR" -xf -
elif [[ -f "${PANEL_DIR}/artisan" ]]; then
    # Already installed: pull the latest code, keep .env and the database.
    sudo -u "$PANEL_USER" git -C "$PANEL_DIR" pull --ff-only 2>/dev/null || true
else
    [[ -n "$REPO" ]] || die "No panel source found. Run this from a checkout, or pass --repo <git-url>."
    git clone --depth 1 --branch "$BRANCH" "$REPO" "$PANEL_DIR"
fi

chown -R "${PANEL_USER}:${PANEL_USER}" "$PANEL_DIR"
ok "source in place"

log "Installing dependencies and building assets"
sudo -u "$PANEL_USER" -H bash -c "cd '$PANEL_DIR' && composer install --no-dev --no-interaction --optimize-autoloader --quiet"
sudo -u "$PANEL_USER" -H bash -c "cd '$PANEL_DIR' && npm ci --silent && npm run build --silent" >/dev/null
ok "dependencies installed, assets built"

# ------------------------------------------------------------- application ---
log "Configuring the application"

cd "$PANEL_DIR"

if [[ ! -f .env ]]; then
    sudo -u "$PANEL_USER" cp .env.example .env
fi

HOST_IP="$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i=1;i<=NF;i++) if ($i=="src") print $(i+1)}' | head -1 || true)"
HOST_IP="${HOST_IP:-127.0.0.1}"

set_env() {
    local key="$1" value="$2"
    if grep -q "^${key}=" .env; then
        sed -i "s|^${key}=.*|${key}=${value}|" .env
    else
        printf '%s=%s\n' "$key" "$value" >> .env
    fi
}

# Absent keys are normal on a first install, so a miss must not be an error.
read_env() {
    grep -E "^${1}=" .env 2>/dev/null | head -1 | cut -d= -f2- | tr -d '"' || true
}

systemctl enable --now mariadb >/dev/null 2>&1 || true

DB_NAME="${PANEL_DB_NAME:-ubuntu_panel}"
DB_USER="${PANEL_DB_USER:-ubuntu_panel}"
# Reuse the password already in .env on a re-run, so an existing database keeps
# working instead of being locked out by a fresh one.
DB_PASS="$(read_env DB_PASSWORD)"
[[ -n "$DB_PASS" ]] || DB_PASS="$(openssl rand -base64 24 | tr -d '/+=' | cut -c1-24)"

if ! mysql --protocol=socket -uroot -e 'SELECT 1' >/dev/null 2>&1; then
    die "Cannot log in to MariaDB as root over the unix socket. If you have set a root password, create the database manually and put the credentials in ${PANEL_DIR}/.env, then re-run."
fi

mysql --protocol=socket -uroot <<SQL
CREATE DATABASE IF NOT EXISTS \`${DB_NAME}\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS '${DB_USER}'@'localhost' IDENTIFIED BY '${DB_PASS}';
ALTER USER '${DB_USER}'@'localhost' IDENTIFIED BY '${DB_PASS}';
GRANT ALL PRIVILEGES ON \`${DB_NAME}\`.* TO '${DB_USER}'@'localhost';
FLUSH PRIVILEGES;
SQL
ok "database ${DB_NAME} ready"

set_env APP_ENV production
set_env APP_DEBUG false
set_env APP_URL "https://${HOST_IP}:${PANEL_PORT}"
set_env DB_CONNECTION mysql
set_env DB_HOST 127.0.0.1
set_env DB_PORT 3306
set_env DB_DATABASE "$DB_NAME"
set_env DB_USERNAME "$DB_USER"
set_env DB_PASSWORD "$DB_PASS"
set_env QUEUE_CONNECTION database
set_env SESSION_DRIVER database
set_env CACHE_STORE database
sudo -u "$PANEL_USER" php artisan key:generate --force --quiet
sudo -u "$PANEL_USER" php artisan migrate --force --quiet
sudo -u "$PANEL_USER" php artisan config:cache --quiet
sudo -u "$PANEL_USER" php artisan route:cache --quiet
chown -R "${PANEL_USER}:${PANEL_USER}" storage bootstrap/cache
ok "application configured"

# ----------------------------------------------------------------- services --
log "Installing services"

cat > /etc/systemd/system/ubuntu-panel-queue.service <<UNIT
# Managed by the Ubuntu Panel installer
[Unit]
Description=Ubuntu Panel queue worker
After=network.target

[Service]
Type=simple
User=${PANEL_USER}
WorkingDirectory=${PANEL_DIR}
ExecStart=/usr/bin/php ${PANEL_DIR}/artisan queue:work --tries=1 --timeout=3600 --sleep=1
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT

cat > /etc/systemd/system/ubuntu-panel-terminal.service <<UNIT
# Managed by the Ubuntu Panel installer
[Unit]
Description=Ubuntu Panel terminal server
After=network.target

[Service]
Type=simple
User=${PANEL_USER}
WorkingDirectory=${PANEL_DIR}
ExecStart=/usr/bin/php ${PANEL_DIR}/artisan panel:terminal-server
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT

cat > /etc/systemd/system/ubuntu-panel-scheduler.service <<UNIT
# Managed by the Ubuntu Panel installer
[Unit]
Description=Ubuntu Panel scheduler

[Service]
Type=oneshot
User=${PANEL_USER}
WorkingDirectory=${PANEL_DIR}
ExecStart=/usr/bin/php ${PANEL_DIR}/artisan schedule:run
UNIT

cat > /etc/systemd/system/ubuntu-panel-scheduler.timer <<UNIT
# Managed by the Ubuntu Panel installer
[Unit]
Description=Run the Ubuntu Panel scheduler every minute

[Timer]
OnCalendar=*-*-* *:*:00
AccuracySec=1s

[Install]
WantedBy=timers.target
UNIT

systemctl daemon-reload
systemctl enable --now ubuntu-panel-queue.service >/dev/null 2>&1
systemctl enable --now ubuntu-panel-terminal.service >/dev/null 2>&1
systemctl enable --now ubuntu-panel-scheduler.timer >/dev/null 2>&1
ok "queue worker, terminal server and scheduler running"

# --------------------------------------------------------------------- web ---
log "Publishing the panel on port ${PANEL_PORT} (PHP ${PHP_VERSION})"

CERT_DIR=/etc/ssl/ubuntu-panel
mkdir -p "$CERT_DIR"
if [[ ! -f "${CERT_DIR}/cert.pem" ]]; then
    # Self-signed so the login form is never served over plain HTTP. Point a
    # hostname at this box and run certbot for a trusted certificate.
    openssl req -x509 -nodes -newkey rsa:2048 -days 3650 \
        -keyout "${CERT_DIR}/key.pem" -out "${CERT_DIR}/cert.pem" \
        -subj "/CN=${HOST_IP}" >/dev/null 2>&1
fi

# The browser terminal's websocket. The daemon listens on loopback only, so
# nginx is how the browser reaches it: same origin, same certificate, no extra
# port to open. Kept in a snippet because the vhost is also rewritten by the
# panel's domain setup and by certbot, and both leave snippets alone.
mkdir -p /etc/nginx/snippets
cat > /etc/nginx/snippets/ubuntu-panel-terminal.conf <<'NGINX'
# Managed by Ubuntu Panel — the browser terminal's websocket.
location /terminal-ws {
    proxy_pass http://127.0.0.1:6001;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    # A shell can sit idle for hours; the default minute would drop it.
    proxy_read_timeout 86400s;
    proxy_send_timeout 86400s;
    proxy_buffering off;
}
NGINX

cat > /etc/nginx/sites-available/ubuntu-panel.conf <<NGINX
# Managed by the Ubuntu Panel installer
server {
    listen ${PANEL_PORT} ssl;
    listen [::]:${PANEL_PORT} ssl;
    server_name _;

    include snippets/ubuntu-panel-terminal.conf;

    ssl_certificate ${CERT_DIR}/cert.pem;
    ssl_certificate_key ${CERT_DIR}/key.pem;

    root ${PANEL_DIR}/public;
    index index.php;

    client_max_body_size 128M;

    location / {
        try_files \$uri \$uri/ /index.php?\$query_string;
    }

    location ~ \.php\$ {
        include snippets/fastcgi-php.conf;
        fastcgi_pass unix:/run/php/php${PHP_VERSION}-fpm.sock;
        fastcgi_read_timeout 300;
    }

    location ~ /\.(?!well-known).* {
        deny all;
    }
}
NGINX

ln -sf /etc/nginx/sites-available/ubuntu-panel.conf /etc/nginx/sites-enabled/ubuntu-panel.conf
nginx -t >/dev/null 2>&1 || die "nginx rejected the panel vhost."
systemctl reload nginx

# PHP-FPM runs the panel as its own user, so it can reach the files it owns.
POOL_DIR="/etc/php/${PHP_VERSION}/fpm/pool.d"
POOL="${POOL_DIR}/ubuntu-panel.conf"

if [[ ! -f "$POOL" && -f "${POOL_DIR}/www.conf" ]]; then
    sed -e "s/^\[www\]/[ubuntu-panel]/" \
        -e "s/^user = .*/user = ${PANEL_USER}/" \
        -e "s/^group = .*/group = ${PANEL_USER}/" \
        -e "s|^listen = .*|listen = /run/php/php${PHP_VERSION}-fpm.sock|" \
        "${POOL_DIR}/www.conf" > "$POOL"
    sed -i "s/^listen.owner = .*/listen.owner = www-data/" "$POOL" || true
    # One pool per socket: the stock one would fight this for the same path.
    rm -f "${POOL_DIR}/www.conf"
fi

systemctl restart "php${PHP_VERSION}-fpm"

# The vhost points at this socket; if PHP-FPM is listening somewhere else the
# panel answers 502 and it is not obvious why.
for _ in $(seq 1 10); do
    [[ -S "/run/php/php${PHP_VERSION}-fpm.sock" ]] && break
    sleep 1
done
[[ -S "/run/php/php${PHP_VERSION}-fpm.sock" ]] \
    || die "PHP-FPM is not listening on /run/php/php${PHP_VERSION}-fpm.sock — check: systemctl status php${PHP_VERSION}-fpm"
ok "nginx serving the panel over HTTPS"

if ufw status 2>/dev/null | grep -q "Status: active"; then
    ufw allow "${PANEL_PORT}/tcp" >/dev/null 2>&1 || true
    ok "opened port ${PANEL_PORT} in ufw"
fi

# ------------------------------------------------------------------- admin ---
log "Creating the administrator"

# Piped from curl, this script *is* stdin, so an interactive prompt inside
# artisan would have nothing to read. Ask here, from the terminal, and hand the
# answers over as arguments.
if [[ -z "$ADMIN_EMAIL" ]] && [[ -r /dev/tty ]]; then
    printf '    Administrator email: '
    read -r ADMIN_EMAIL < /dev/tty || true
fi

if [[ -z "$ADMIN_PASSWORD" ]] && [[ -r /dev/tty ]]; then
    while :; do
        printf '    Administrator password (min 8 characters): '
        read -rs ADMIN_PASSWORD < /dev/tty || true
        printf '\n'

        [[ ${#ADMIN_PASSWORD} -ge 8 ]] && break
        printf '    Too short.\n'
    done
fi

INSTALL_ARGS=()
[[ -n "$ADMIN_EMAIL" ]] && INSTALL_ARGS+=(--email="$ADMIN_EMAIL")
[[ -n "$ADMIN_PASSWORD" ]] && INSTALL_ARGS+=(--password="$ADMIN_PASSWORD")

# An empty array must expand to nothing at all, not to one empty argument.
if [[ ${#INSTALL_ARGS[@]} -gt 0 ]]; then
    sudo -u "$PANEL_USER" php artisan panel:install "${INSTALL_ARGS[@]}"
else
    sudo -u "$PANEL_USER" php artisan panel:install
fi

# ----------------------------------------------------------------- software --
# In the foreground, not through the queue: an installer that hands back before
# the work is done cannot tell you whether it worked. Every service is attempted
# even if one fails, and the failures are named at the end — a machine missing
# MongoDB is still a working panel, and stopping here would leave you with no
# panel at all.
SOFTWARE_OK=1

if [[ "$PANEL_SERVICES" != "none" ]]; then
    log "Installing software (${PANEL_SERVICES})"
    printf '    This is the long part — apt is fetching everything the panel can host with.\n'

    if sudo -u "$PANEL_USER" php artisan panel:install-services --services="$PANEL_SERVICES"; then
        ok "software installed"
    else
        SOFTWARE_OK=0
    fi
fi

printf '\n\033[1;32m━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\033[0m\n'
printf '\033[1;32m  Ubuntu Panel is installed.\033[0m\n'
printf '\033[1;32m━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\033[0m\n\n'
printf '  \033[1mLog in here:\033[0m  https://%s:%s\n' "$HOST_IP" "$PANEL_PORT"
if [[ -n "$ADMIN_EMAIL" ]]; then
    printf '  \033[1mUsername:\033[0m     %s\n' "$ADMIN_EMAIL"
fi
printf '  \033[1mPassword:\033[0m     the one you just chose\n\n'
printf '  The certificate is self-signed, so your browser warns once — click\n'
printf '  through it. To use your own domain with a real certificate, point it\n'
printf '  at this server and run:\n\n'
printf '      cd %s && sudo -u %s php artisan panel:domain panel.example.com\n\n' "$PANEL_DIR" "$PANEL_USER"
printf '  \033[1mInstalled at:\033[0m %s\n' "$PANEL_DIR"
printf '  \033[1mDatabase:\033[0m     MariaDB, %s\n' "$DB_NAME"
printf '  \033[1mServices:\033[0m     ubuntu-panel-queue, ubuntu-panel-terminal,\n'
printf '                ubuntu-panel-scheduler.timer\n'
printf '  \033[1mUpdate with:\033[0m  cd %s && sudo -u %s php artisan panel:update\n\n' "$PANEL_DIR" "$PANEL_USER"

if [[ "$SOFTWARE_OK" -eq 0 ]]; then
    printf '\033[0;31m  Some software did not install — the failures are listed above.\033[0m\n'
    printf '  The panel works; retry them from its Software page, or run:\n\n'
    printf '      cd %s && sudo -u %s php artisan panel:install-services --retry\n\n' "$PANEL_DIR" "$PANEL_USER"
    exit 1
fi
