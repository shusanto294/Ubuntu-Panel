#!/usr/bin/env bash
#
# Ubuntu Panel installer and updater.
#
# The same script does both. Run it on a clean machine and it installs the
# panel; run it again over an existing install and it updates in place, keeping
# the database, the application key, the panel's own nginx vhost and every
# setting the panel wrote for itself.
#
#   sudo ./install.sh
#
# It runs in one of two modes, and picks between them itself:
#
#   prebuilt  the directory is an unpacked release tarball — it carries a
#             RELEASE file and a Linux binary. Nothing is compiled here: no Go
#             toolchain, no Node, no npm, no Vite. This is what `get.sh`
#             downloads, and it is the fast path.
#
#   source    the directory is a checkout. The frontend is built with Vite and
#             embedded into the binary by the Go build, on this machine.
#
# The frontend lives inside the binary either way, so there is no public/
# directory to install and no window where the assets and the binary disagree.
#
# Environment overrides:
#
#   PANEL_USER=ubuntupanel   unix user the panel runs as
#   PANEL_ROOT=/opt/ubuntu-panel
#   PANEL_PORT=8088          loopback port nginx proxies to
#   PANEL_SWAP=1             create a swapfile if the box has none and < 1.5 GB
#   PANEL_SKIP_APT=0         skip the apt dependency step
#   PANEL_INSTALL_APPS=all   what a first start installs: all | core | none
#   PANEL_FROM_SOURCE=0      1 forces a source build even inside a release
#   NO_COLOR=1               plain output
#
set -Eeuo pipefail

# Whether the port was chosen on the command line matters: if it was not, an
# existing panel.conf gets to keep the port it is already on.
PANEL_PORT_EXPLICIT=0
[ -n "${PANEL_PORT:-}" ] && PANEL_PORT_EXPLICIT=1 || true

PANEL_USER="${PANEL_USER:-ubuntupanel}"
PANEL_ROOT="${PANEL_ROOT:-/opt/ubuntu-panel}"
PANEL_PORT="${PANEL_PORT:-8088}"

# Which PHP-FPM socket the phpMyAdmin location in the vhost below talks to.
# The panel's own default, because PHP is not on the machine yet when this
# runs — and the panel rewrites the vhost with the real version the moment
# phpMyAdmin is installed from Apps.
PANEL_PHP_VERSION="${PANEL_PHP_VERSION:-8.3}"
PANEL_SWAP="${PANEL_SWAP:-1}"
PANEL_SKIP_APT="${PANEL_SKIP_APT:-0}"
PANEL_FROM_SOURCE="${PANEL_FROM_SOURCE:-0}"

GET_URL="https://raw.githubusercontent.com/shusanto294/Ubuntu-Panel-Go/main/get.sh"

CONFIG_DIR=/etc/ubuntu-panel
CONFIG_FILE="$CONFIG_DIR/panel.conf"
STATE_DIR=/var/lib/ubuntu-panel
LOG_DIR=/var/log/ubuntu-panel
BACKUP_DIR="$STATE_DIR/backups"
NGINX_SITE=/etc/nginx/sites-available/ubuntu-panel
UNIT_FILE=/etc/systemd/system/ubuntu-panel.service
DB_FILE="$STATE_DIR/panel.db"
INSTALL_LOG="$LOG_DIR/install.log"

# apt 2.0+ waits for the dpkg lock itself, which is what unattended-upgrades on
# a freshly booted VPS is holding. Without this apt fails outright.
APT_OPTS=(-o "DPkg::Lock::Timeout=600")

# Vite 8 refuses to run below this. The panel installs Node for *sites*
# separately; this is only about building the panel's own frontend.
NODE_MIN_MAJOR=20
NODE_MIN_MINOR=19

# The go directive in go.mod. A source build needs at least this.
GO_MIN_MAJOR=1
GO_MIN_MINOR=24
GO_INSTALL_VERSION=1.24.4

# ------------------------------------------------------------------ output

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
    BOLD=$'\033[1m'; DIM=$'\033[2m'; RED=$'\033[31m'; GREEN=$'\033[32m'
    YELLOW=$'\033[33m'; CYAN=$'\033[36m'; RESET=$'\033[0m'
else
    BOLD=''; DIM=''; RED=''; GREEN=''; YELLOW=''; CYAN=''; RESET=''
fi

STEP=0
STEP_TOTAL=13   # reset once the build mode is known: a release skips three steps

step()   { STEP=$((STEP + 1)); printf '\n%s[%2d/%d] %s%s\n' "$BOLD" "$STEP" "$STEP_TOTAL" "$*" "$RESET"; }
detail() { printf '        %s\n' "$*"; }
kv()     { printf '        %-22s %s\n' "$1" "$2"; }
ok()     { printf '        %s+%s %s\n' "$GREEN" "$RESET" "$*"; }
same()   { printf '        %s=%s %s\n' "$DIM" "$RESET" "$*"; }
warn()   { printf '        %s!%s %s\n' "$YELLOW" "$RESET" "$*"; }
note()   { printf '        %s>%s %s\n' "$CYAN" "$RESET" "$*"; }

die() {
    trap - ERR
    printf '\n%s!! %s%s\n' "$RED" "$*" "$RESET" >&2
    if [ -f "${INSTALL_LOG:-/nonexistent}" ]; then
        printf '   Full log: %s\n' "$INSTALL_LOG" >&2
    fi
    exit 1
}

on_error() {
    local rc=$1 line=$2 cmd=$3

    trap - ERR

    printf '\n%s!! Failed at line %s (exit %s)%s\n' "$RED" "$line" "$rc" "$RESET" >&2
    printf '   %s\n' "$cmd" >&2

    if [ -s "${INSTALL_LOG:-/nonexistent}" ]; then
        printf '\n%s   last 30 lines of %s:%s\n' "$DIM" "$INSTALL_LOG" "$RESET" >&2
        tail -n 30 "$INSTALL_LOG" | sed 's/^/   | /' >&2
    fi

    rollback_if_staged
    exit "$rc"
}

trap 'on_error $? $LINENO "$BASH_COMMAND"' ERR

# Everything verbose goes to the log; the terminal gets the condensed version.
logged() {
    printf '\n$ %s\n' "$*" >>"$INSTALL_LOG"
    "$@" >>"$INSTALL_LOG" 2>&1
}

logged_sh() {
    printf '\n$ %s\n' "$1" >>"$INSTALL_LOG"
    bash -c "$1" >>"$INSTALL_LOG" 2>&1
}

human_bytes() {
    local b="${1:-0}"

    case "$b" in ''|*[!0-9]*) printf 'unknown'; return 0 ;; esac

    if   [ "$b" -ge 1073741824 ]; then awk -v b="$b" 'BEGIN{printf "%.1f GB", b/1073741824}'
    elif [ "$b" -ge 1048576 ];    then awk -v b="$b" 'BEGIN{printf "%.1f MB", b/1048576}'
    elif [ "$b" -ge 1024 ];       then awk -v b="$b" 'BEGIN{printf "%.0f KB", b/1024}'
    else                               printf '%s B' "$b"
    fi
}

file_size() { stat -c %s "$1" 2>/dev/null || echo 0; }

log_offset() { wc -c <"$INSTALL_LOG" 2>/dev/null | tr -d ' ' || echo 0; }

# --------------------------------------------------------------- rollback

STAGED_BINARY=0
SERVICE_WAS_ACTIVE=0

rollback_if_staged() {
    if [ "$STAGED_BINARY" = 1 ] && [ -f "$PANEL_ROOT/bin/ubuntu-panel.previous" ]; then
        mv -f "$PANEL_ROOT/bin/ubuntu-panel.previous" "$PANEL_ROOT/bin/ubuntu-panel" || true

        printf '\n%s   Rolled back to the previous binary.%s\n' "$YELLOW" "$RESET" >&2

        if [ "$SERVICE_WAS_ACTIVE" = 1 ]; then
            systemctl start ubuntu-panel >/dev/null 2>&1 || true
        fi
    fi
}

# =========================================================== 1. preflight

[ "$(id -u)" -eq 0 ] || die "Run this with sudo:  sudo ./install.sh"

mkdir -p "$LOG_DIR"
: >"$INSTALL_LOG"
chmod 640 "$INSTALL_LOG"

SOURCE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SOURCE_DIR"

# ------------------------------------------------------------- build mode

RELEASE_FILE="$SOURCE_DIR/RELEASE"
RELEASE_VERSION=""
RELEASE_COMMIT=""
RELEASE_ARCH=""
RELEASE_BUILT=""

release_field() {
    [ -f "$RELEASE_FILE" ] || return 0
    sed -n "/^[[:space:]]*$1[[:space:]]*=/{ s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*//; p; q; }" "$RELEASE_FILE"
}

# The first four bytes are the ELF magic; e_machine sits at offset 18, little
# endian. Reading it here means an amd64 tarball on an arm64 box is a sentence
# rather than "cannot execute binary file".
elf_arch() {
    local magic machine

    magic="$(od -An -tx1 -N4 "$1" 2>/dev/null | tr -d ' \n')"
    [ "$magic" = "7f454c46" ] || { printf 'not-an-elf'; return 0; }

    machine="$(od -An -tx1 -j18 -N2 "$1" 2>/dev/null | tr -d ' \n')"
    case "$machine" in
        3e00) printf 'amd64' ;;
        b700) printf 'arm64' ;;
        *)    printf 'unknown' ;;
    esac
}

BUILD_MODE=source

if [ -f "$RELEASE_FILE" ] && [ "$PANEL_FROM_SOURCE" != "1" ] && [ -f "$SOURCE_DIR/ubuntu-panel" ]; then
    BUILD_MODE=prebuilt
    RELEASE_VERSION="$(release_field version)"
    RELEASE_COMMIT="$(release_field commit)"
    RELEASE_ARCH="$(release_field arch)"
    RELEASE_BUILT="$(release_field built)"
fi

# 13 steps from source; a release skips the toolchain checks and the build, and
# adds one step that checks what it shipped with instead. The `|| true` is not
# decoration: under `set -e` a bare failing test ends the script.
[ "$BUILD_MODE" = prebuilt ] && STEP_TOTAL=11 || true

printf '\n%s==> Ubuntu Panel%s\n' "$BOLD" "$RESET"

step "Checking the machine"

if [ -r /etc/os-release ]; then
    # shellcheck disable=SC1091
    . /etc/os-release
else
    die "No /etc/os-release — this installer is for Ubuntu."
fi

if [ "${ID:-}" != "ubuntu" ]; then
    warn "Tested on Ubuntu; ${PRETTY_NAME:-this system} may behave differently."
fi

ARCH="$(dpkg --print-architecture 2>/dev/null || uname -m)"
CPUS="$(nproc 2>/dev/null || echo 1)"
MEM_KB="$(awk '/^MemTotal:/{print $2}' /proc/meminfo)"
SWAP_KB="$(awk '/^SwapTotal:/{print $2}' /proc/meminfo)"
DISK_FREE_KB="$(df -Pk / | awk 'NR==2{print $4}')"

case "$ARCH" in
    x86_64|amd64)   HOST_ARCH=amd64 ;;
    aarch64|arm64)  HOST_ARCH=arm64 ;;
    *)              HOST_ARCH="$ARCH" ;;
esac

kv "System"    "${PRETTY_NAME:-unknown} ($ARCH)"
kv "Kernel"    "$(uname -r)"
kv "CPU"       "$CPUS core(s)"
kv "Memory"    "$(human_bytes $((MEM_KB * 1024))) RAM, $(human_bytes $((SWAP_KB * 1024))) swap"
kv "Disk free" "$(human_bytes $((DISK_FREE_KB * 1024))) on /"
kv "Source"    "$SOURCE_DIR"

SOURCE_COMMIT=""

if [ "$BUILD_MODE" = prebuilt ]; then
    kv "Mode" "prebuilt release — nothing is compiled on this machine"

    for required in ubuntu-panel scripts/ubuntu-panel.service RELEASE; do
        if [ ! -e "$SOURCE_DIR/$required" ]; then
            die "$required is missing — this release directory is incomplete."
        fi
    done

    if [ -n "$RELEASE_COMMIT" ] && printf '%s' "$RELEASE_COMMIT" | grep -qE '^[0-9a-f]{40}$'; then
        SOURCE_COMMIT="$RELEASE_COMMIT"
        kv "Built from" "${RELEASE_COMMIT:0:12}"
    else
        kv "Built from" "unrecorded (the panel cannot report available updates)"
    fi
else
    kv "Mode" "source — the frontend and the binary are built here"

    for required in Makefile go.mod package.json package-lock.json cmd internal resources scripts/ubuntu-panel.service; do
        if [ ! -e "$SOURCE_DIR/$required" ]; then
            die "$required is missing — run this from inside a complete source tree."
        fi
    done

    if command -v git >/dev/null 2>&1 && git -C "$SOURCE_DIR" rev-parse --git-dir >/dev/null 2>&1; then
        SOURCE_COMMIT="$(git -C "$SOURCE_DIR" rev-parse HEAD 2>/dev/null || true)"
        SOURCE_BRANCH="$(git -C "$SOURCE_DIR" rev-parse --abbrev-ref HEAD 2>/dev/null || echo detached)"
        if [ -n "$SOURCE_COMMIT" ]; then
            kv "Git" "${SOURCE_BRANCH} @ ${SOURCE_COMMIT:0:12}"
        fi
    else
        kv "Git" "not a checkout (the panel cannot report available updates)"
    fi
fi

# node_modules and the Go build cache are what need the room, and a release
# carries neither.
if [ "$BUILD_MODE" = prebuilt ]; then
    if [ "$DISK_FREE_KB" -lt 307200 ]; then
        die "Less than 300 MB free on / — there is not enough room to install."
    fi
elif [ "$DISK_FREE_KB" -lt 3145728 ]; then
    die "Less than 3 GB free on / — the Go toolchain, its build cache and node_modules will not fit."
fi

# The Vite build is the one memory-hungry moment in the whole install, so a
# release has no reason to touch swap at all.
TOTAL_MEM_KB=$((MEM_KB + SWAP_KB))
if [ "$BUILD_MODE" = prebuilt ]; then
    same "Prebuilt: nothing is built here, so memory is not a concern."
elif [ "$TOTAL_MEM_KB" -lt 1536000 ] && [ "$SWAP_KB" -eq 0 ] && [ "$PANEL_SWAP" = "1" ]; then
    warn "$(human_bytes $((MEM_KB * 1024))) of memory and no swap; the frontend build would be killed."
    detail "Creating a 2 GB swapfile at /swapfile (set PANEL_SWAP=0 to skip this)."

    logged_sh "fallocate -l 2G /swapfile || dd if=/dev/zero of=/swapfile bs=1M count=2048"
    chmod 600 /swapfile
    logged mkswap /swapfile
    logged swapon /swapfile
    grep -q '^/swapfile' /etc/fstab || printf '/swapfile none swap sw 0 0\n' >>/etc/fstab
    ok "Swap enabled: 2.0 GB at /swapfile (kept, and added to /etc/fstab)"
elif [ "$TOTAL_MEM_KB" -lt 1536000 ]; then
    warn "Low memory and no swap. If the frontend build gets killed, add swap and re-run."
fi

# ==================================================== 2. what is already here

step "Inspecting the existing installation"

MODE=install
OLD_VERSION=""
OLD_BINARY_SIZE=0

if [ -x "$PANEL_ROOT/bin/ubuntu-panel" ]; then
    MODE=update
    OLD_VERSION="$("$PANEL_ROOT/bin/ubuntu-panel" --version 2>/dev/null || echo 'unknown')"
    OLD_BINARY_SIZE="$(file_size "$PANEL_ROOT/bin/ubuntu-panel")"
fi

# An existing panel.conf owns the port unless the caller asked for another one.
if [ "$PANEL_PORT_EXPLICIT" = 0 ] && [ -f "$CONFIG_FILE" ]; then
    # First occurrence only, matching how the panel itself parses the file.
    EXISTING_PORT="$(sed -n '/^[[:space:]]*PANEL_BIND_PORT[[:space:]]*=/{ s/^[[:space:]]*PANEL_BIND_PORT[[:space:]]*=[[:space:]]*//; s/[^0-9].*$//; p; q; }' "$CONFIG_FILE")"
    if [ -n "$EXISTING_PORT" ] && [ "$EXISTING_PORT" -gt 0 ] && [ "$EXISTING_PORT" -le 65535 ]; then
        PANEL_PORT="$EXISTING_PORT"
    fi
fi

if [ "$MODE" = update ]; then
    note "Update — an installation already exists at $PANEL_ROOT"
    kv "Installed version" "$OLD_VERSION"
    kv "Installed binary"  "$(human_bytes "$OLD_BINARY_SIZE")"

    if systemctl is-active --quiet ubuntu-panel 2>/dev/null; then
        SERVICE_WAS_ACTIVE=1
        SINCE="$(systemctl show ubuntu-panel -p ActiveEnterTimestamp --value 2>/dev/null || true)"
        kv "Service" "running${SINCE:+, up since $SINCE}"
    else
        kv "Service" "installed but not running"
    fi

    if [ -f "$DB_FILE" ]; then
        kv "Database" "$(human_bytes "$(file_size "$DB_FILE")") at $DB_FILE"
    else
        warn "No database at $DB_FILE yet; it will be created on the first start."
    fi

    [ -f "$CONFIG_FILE" ] && kv "Config" "$CONFIG_FILE" || true

    if [ -f "$NGINX_SITE" ]; then
        EXISTING_NAME="$(awk '/^[[:space:]]*server_name/{gsub(/;/,""); print $2; exit}' "$NGINX_SITE" 2>/dev/null || true)"
        kv "nginx vhost" "server_name ${EXISTING_NAME:-unset}"
    fi

    # The C panel installed a public/ tree beside the binary. The Go binary
    # carries its assets inside it, so that tree is now dead weight — and worse,
    # a PANEL_PUBLIC pointing at it would serve last year's JavaScript.
    if [ -d "$PANEL_ROOT/public" ]; then
        note "Found $PANEL_ROOT/public from an older install; the assets now live in the binary."
    fi
else
    note "Fresh install — nothing found at $PANEL_ROOT"
fi

kv "Panel port" "127.0.0.1:$PANEL_PORT"

# The port has to be free, or already ours.
if command -v ss >/dev/null 2>&1; then
    PORT_OWNER="$(ss -lntp 2>/dev/null | awk -v p=":$PANEL_PORT\$" '$4 ~ p {print $NF; exit}' || true)"
    if [ -n "$PORT_OWNER" ] && ! printf '%s' "$PORT_OWNER" | grep -q 'ubuntu-panel'; then
        die "Port $PANEL_PORT is already taken by $PORT_OWNER. Re-run with PANEL_PORT=<other>."
    fi
fi

# =================================================== 3. system dependencies

step "Installing system dependencies"

# The binary is statically linked (CGO_ENABLED=0, a pure-Go SQLite), so nothing
# here is for the panel to *run*. nginx is the front door, sqlite3 takes the
# backups, and the rest is what the panel shells out to.
PACKAGES=(
    git curl ca-certificates openssl sqlite3
    nginx sudo psmisc iproute2
)

if [ "$PANEL_SKIP_APT" = "1" ]; then
    same "PANEL_SKIP_APT=1 — skipping apt entirely."
else
    export DEBIAN_FRONTEND=noninteractive

    MISSING=()
    for package in "${PACKAGES[@]}"; do
        if dpkg-query -W -f='${Status}' "$package" 2>/dev/null | grep -q 'ok installed'; then
            same "$package $(dpkg-query -W -f='${Version}' "$package" 2>/dev/null || true)"
        else
            MISSING+=("$package")
        fi
    done

    # Older apt has no Lock::Timeout, so wait the lock out by hand as well.
    if command -v fuser >/dev/null 2>&1; then
        WAITED=0
        while fuser /var/lib/dpkg/lock-frontend >/dev/null 2>&1; do
            if [ "$WAITED" = 0 ]; then
                detail "Another apt process holds the dpkg lock; waiting for it…"
            fi
            WAITED=$((WAITED + 3))
            if [ "$WAITED" -gt 600 ]; then
                die "The dpkg lock was still held after 10 minutes."
            fi
            sleep 3
        done
        [ "$WAITED" -gt 0 ] && ok "Lock released after ${WAITED}s" || true
    fi

    detail "Refreshing the package lists…"
    logged apt-get "${APT_OPTS[@]}" update -y -qq || die "apt-get update failed. See $INSTALL_LOG."

    if [ "${#MISSING[@]}" -gt 0 ]; then
        detail "Installing ${#MISSING[@]} missing package(s): ${MISSING[*]}"

        logged apt-get "${APT_OPTS[@]}" install -y -qq "${MISSING[@]}" \
            || die "Could not install: ${MISSING[*]}. See $INSTALL_LOG."

        for package in "${MISSING[@]}"; do
            ok "$package $(dpkg-query -W -f='${Version}' "$package" 2>/dev/null || true)"
        done
    else
        same "All ${#PACKAGES[@]} dependencies were already present."
    fi
fi

kv "nginx" "$(nginx -v 2>&1 | sed 's|nginx version: ||' || echo unknown)"

# =============================================== 4. the build toolchain

if [ "$BUILD_MODE" = prebuilt ]; then
    : # Go and Node are build dependencies only, and a release brings neither.
else

step "Checking the build toolchain (Go and Node.js)"

version_at_least() {
    # $1 actual "1.24.4", $2 major, $3 minor
    local major minor
    major="${1%%.*}"
    minor="$(printf '%s' "$1" | cut -d. -f2)"

    case "$major" in ''|*[!0-9]*) return 1 ;; esac
    case "$minor" in ''|*[!0-9]*) minor=0 ;; esac

    [ "$major" -gt "$2" ] && return 0
    [ "$major" -eq "$2" ] && [ "$minor" -ge "$3" ] && return 0

    return 1
}

# Ubuntu's golang-go trails the go.mod directive by a year or more on LTS, so
# the toolchain comes from go.dev and lands in /usr/local/go, which is where
# Go's own instructions put it.
GO_VERSION=""
if command -v go >/dev/null 2>&1; then
    GO_VERSION="$(go env GOVERSION 2>/dev/null | sed 's/^go//' || true)"
elif [ -x /usr/local/go/bin/go ]; then
    export PATH="/usr/local/go/bin:$PATH"
    GO_VERSION="$(go env GOVERSION 2>/dev/null | sed 's/^go//' || true)"
fi

if [ -n "$GO_VERSION" ] && version_at_least "$GO_VERSION" "$GO_MIN_MAJOR" "$GO_MIN_MINOR"; then
    same "go $GO_VERSION — at or above the ${GO_MIN_MAJOR}.${GO_MIN_MINOR} go.mod asks for."
else
    if [ -n "$GO_VERSION" ]; then
        warn "go $GO_VERSION is older than ${GO_MIN_MAJOR}.${GO_MIN_MINOR}; this source tree will not build on it."
    else
        detail "Go is not installed."
    fi

    GO_TARBALL="go${GO_INSTALL_VERSION}.linux-${HOST_ARCH}.tar.gz"
    detail "Installing Go ${GO_INSTALL_VERSION} into /usr/local/go…"

    logged_sh "curl -fsSL -o /tmp/$GO_TARBALL https://go.dev/dl/$GO_TARBALL" \
        || die "Could not download $GO_TARBALL from go.dev. See $INSTALL_LOG."

    rm -rf /usr/local/go
    logged_sh "tar -C /usr/local -xzf /tmp/$GO_TARBALL" || die "Could not unpack $GO_TARBALL."
    rm -f "/tmp/$GO_TARBALL"

    export PATH="/usr/local/go/bin:$PATH"
    printf 'export PATH=$PATH:/usr/local/go/bin\n' >/etc/profile.d/go.sh
    chmod 644 /etc/profile.d/go.sh

    hash -r
    GO_VERSION="$(go env GOVERSION 2>/dev/null | sed 's/^go//' || true)"
    version_at_least "${GO_VERSION:-0}" "$GO_MIN_MAJOR" "$GO_MIN_MINOR" \
        || die "Go is still too old after installing: ${GO_VERSION:-not installed}"

    ok "go $GO_VERSION in /usr/local/go (also added to /etc/profile.d/go.sh)"
fi

NODE_VERSION=""
command -v node >/dev/null 2>&1 && NODE_VERSION="$(node -v 2>/dev/null | sed 's/^v//' || true)" || true

if [ -n "$NODE_VERSION" ] && version_at_least "$NODE_VERSION" "$NODE_MIN_MAJOR" "$NODE_MIN_MINOR"; then
    same "node v$NODE_VERSION, npm $(npm -v 2>/dev/null || echo '?') — at or above the v${NODE_MIN_MAJOR}.${NODE_MIN_MINOR} Vite needs."
else
    if [ -n "$NODE_VERSION" ]; then
        warn "node v$NODE_VERSION is older than v${NODE_MIN_MAJOR}.${NODE_MIN_MINOR}; Vite will not run on it."
    else
        detail "Node.js is not installed."
    fi

    detail "Installing Node.js 22 from NodeSource…"
    logged_sh "curl -fsSL https://deb.nodesource.com/setup_22.x | bash -" \
        || die "Could not add the NodeSource repository. See $INSTALL_LOG."

    if ! logged apt-get "${APT_OPTS[@]}" install -y -qq nodejs; then
        # Ubuntu's `npm` package conflicts with the npm NodeSource bundles.
        warn "nodejs clashed with the distribution npm package; removing it and retrying."
        logged apt-get "${APT_OPTS[@]}" remove -y -qq npm || true
        logged apt-get "${APT_OPTS[@]}" install -y -qq nodejs \
            || die "Could not install Node.js. See $INSTALL_LOG."
    fi

    hash -r
    NODE_VERSION="$(node -v 2>/dev/null | sed 's/^v//' || true)"
    version_at_least "${NODE_VERSION:-0}" "$NODE_MIN_MAJOR" "$NODE_MIN_MINOR" \
        || die "Node.js is still too old: ${NODE_VERSION:-not installed}"

    ok "node v$NODE_VERSION, npm $(npm -v)"
fi

fi  # end of the source-only toolchain step

# ==================================================== 5. user and privileges

step "Setting up the ${PANEL_USER} user"

if id -u "$PANEL_USER" >/dev/null 2>&1; then
    same "User ${PANEL_USER} already exists (uid $(id -u "$PANEL_USER"))."
else
    logged useradd --system --create-home --shell /bin/bash "$PANEL_USER"
    ok "Created system user ${PANEL_USER} (uid $(id -u "$PANEL_USER"))"
fi

# The panel manages the machine, so it needs to run privileged commands. It gets
# passwordless sudo rather than running as root: the privilege is then requested
# per command and every one of them lands in the audit log.
SUDOERS_FILE=/etc/sudoers.d/ubuntu-panel
SUDOERS_NEW="$(mktemp)"
cat >"$SUDOERS_NEW" <<EOF
# Managed by the Ubuntu Panel installer.
${PANEL_USER} ALL=(ALL) NOPASSWD: ALL
Defaults:${PANEL_USER} !requiretty
EOF

if visudo -cf "$SUDOERS_NEW" >>"$INSTALL_LOG" 2>&1; then
    if [ -f "$SUDOERS_FILE" ] && cmp -s "$SUDOERS_NEW" "$SUDOERS_FILE"; then
        same "sudoers drop-in unchanged."
    else
        install -m 0440 "$SUDOERS_NEW" "$SUDOERS_FILE"
        ok "Wrote $SUDOERS_FILE (validated by visudo before installing)"
    fi
    rm -f "$SUDOERS_NEW"
else
    rm -f "$SUDOERS_NEW"
    die "The sudoers drop-in did not validate — refusing to install it."
fi

warn "${PANEL_USER} has passwordless sudo: compromising the panel is compromising the machine."

# ================================================================ 6. backup

step "Backing up"

if [ "$MODE" = update ] && [ -f "$DB_FILE" ]; then
    install -d -m 0700 "$BACKUP_DIR"
    STAMP="$(date +%Y%m%d-%H%M%S)"

    if sqlite3 "$DB_FILE" ".backup '$BACKUP_DIR/panel-$STAMP.db'" >>"$INSTALL_LOG" 2>&1; then
        ok "Database -> $BACKUP_DIR/panel-$STAMP.db ($(human_bytes "$(file_size "$BACKUP_DIR/panel-$STAMP.db")"))"
    else
        # A hot copy is worse than .backup, and much better than nothing.
        cp -a "$DB_FILE" "$BACKUP_DIR/panel-$STAMP.db"
        warn "sqlite3 .backup failed; took a plain copy instead."
    fi

    if [ -f "$CONFIG_FILE" ]; then
        cp -a "$CONFIG_FILE" "$BACKUP_DIR/panel-$STAMP.conf"
        ok "Config   -> $BACKUP_DIR/panel-$STAMP.conf (holds the application key)"
    fi

    chown -R root:root "$BACKUP_DIR"
    chmod 700 "$BACKUP_DIR"
    chmod 600 "$BACKUP_DIR"/* 2>/dev/null || true

    # Keep the last ten. A panel database is small, but not free.
    KEPT="$(find "$BACKUP_DIR" -maxdepth 1 -name 'panel-*.db' | wc -l | tr -d ' ')"
    if [ "$KEPT" -gt 10 ]; then
        ls -1t "$BACKUP_DIR"/panel-*.db | tail -n +11 | xargs -r rm -f
        detail "Pruned old backups, keeping the 10 most recent (had $KEPT)."
    fi
else
    same "Fresh install — there is nothing to back up yet."
fi

# =========================================================== 7. the binary
#
# Either it arrives prebuilt in the release, or it is built here. Both paths
# end with NEW_VERSION and NEW_BINARY_SIZE set, and a binary at
# $SOURCE_DIR/ubuntu-panel.

if [ "$BUILD_MODE" = prebuilt ]; then

step "Checking the prebuilt release"

kv "Version" "${RELEASE_VERSION:-unrecorded}"
kv "Built"   "${RELEASE_BUILT:-unrecorded}"

BINARY_ARCH="$(elf_arch "$SOURCE_DIR/ubuntu-panel")"
kv "Architecture" "binary $BINARY_ARCH, machine $HOST_ARCH"

if [ -n "$RELEASE_ARCH" ] && [ "$RELEASE_ARCH" != "$BINARY_ARCH" ]; then
    warn "RELEASE claims $RELEASE_ARCH but the ELF header says $BINARY_ARCH — going by the header."
fi

if [ "$BINARY_ARCH" = not-an-elf ]; then
    die "ubuntu-panel is not a Linux executable — the download is corrupt. Re-run get.sh."
fi

if [ "$BINARY_ARCH" != "$HOST_ARCH" ]; then
    die "This is the $BINARY_ARCH build and the machine is $HOST_ARCH. Download the $HOST_ARCH tarball, or build from source with PANEL_FROM_SOURCE=1."
fi

chmod 0755 "$SOURCE_DIR/ubuntu-panel"

# Static, so there is nothing to resolve — but say so, because the C panel's
# releases needed the right libssl and this one deliberately does not.
if ldd "$SOURCE_DIR/ubuntu-panel" 2>&1 | grep -q 'not a dynamic executable'; then
    ok "Statically linked — no shared libraries to satisfy"
else
    MISSING_LIBS="$(ldd "$SOURCE_DIR/ubuntu-panel" 2>/dev/null | grep 'not found' || true)"
    if [ -n "$MISSING_LIBS" ]; then
        printf '%s\n' "$MISSING_LIBS" | sed 's/^/        /' >&2
        die "The prebuilt binary is missing shared libraries on this system. Build from source instead: PANEL_FROM_SOURCE=1."
    fi
fi

if ! NEW_VERSION="$("$SOURCE_DIR/ubuntu-panel" --version 2>>"$INSTALL_LOG")"; then
    tail -n 10 "$INSTALL_LOG" | sed 's/^/        /' >&2 || true
    die "The prebuilt binary would not run here. Build from source instead: PANEL_FROM_SOURCE=1."
fi

NEW_BINARY_SIZE="$(file_size "$SOURCE_DIR/ubuntu-panel")"

ok "Runs on this machine: $NEW_VERSION"
kv "Binary" "$(human_bytes "$NEW_BINARY_SIZE") (frontend included)"

else  # ------------------------------------------------ built on this machine

step "Building the frontend"

kv "node" "$(node -v)"
kv "npm"  "$(npm -v)"

detail "Installing JavaScript dependencies (npm ci)…"

NPM_MARK="$(log_offset)"

# sed rather than grep for the filter: grep exits 1 when it matches nothing,
# and under `pipefail` that would report a perfectly good build as a failure.
if npm ci --no-audit --no-fund 2>&1 | tee -a "$INSTALL_LOG" \
    | sed -E -n '/added |removed |changed |packages in|npm (warn|error|ERR)/ s/^/        /p'
then
    NPM_RC=0
else
    NPM_RC=1
fi

if [ "$NPM_RC" -ne 0 ]; then
    tail -c "+$((NPM_MARK + 1))" "$INSTALL_LOG" | grep -E 'npm (error|ERR)' | head -n 10 | sed 's/^/        /' >&2 || true
    die "npm ci failed. See $INSTALL_LOG."
fi

VITE_MARK="$(log_offset)"

detail "Compiling assets (vite build)…"

if npm run build 2>&1 | tee -a "$INSTALL_LOG" \
    | sed -E -n '/modules transformed|built in|rendering chunks|error|ERROR|Killed/ s/^/        /p'
then
    VITE_RC=0
else
    VITE_RC=1
fi

if [ "$VITE_RC" -ne 0 ]; then
    if tail -c "+$((VITE_MARK + 1))" "$INSTALL_LOG" | grep -qiE 'killed|out of memory|heap out of'; then
        die "The frontend build ran out of memory. Add swap (PANEL_SWAP=1) and re-run."
    fi
    die "The frontend build failed. See $INSTALL_LOG."
fi

ASSET_DIR="$SOURCE_DIR/internal/assets/public/build"
[ -d "$ASSET_DIR" ] || die "vite finished but $ASSET_DIR does not exist."

ASSET_COUNT="$(find "$ASSET_DIR" -type f | wc -l | tr -d ' ')"
ASSET_BYTES="$(du -sb "$ASSET_DIR" 2>/dev/null | awk '{print $1}' || echo 0)"

[ "$ASSET_COUNT" -gt 0 ] || die "vite produced no files in $ASSET_DIR."

ok "$ASSET_COUNT asset(s), $(human_bytes "${ASSET_BYTES:-0}") — to be embedded in the binary"

# The panel hashes this to version the Inertia payload. Without it the browser
# will not hard-reload after an update, and new JavaScript runs against an old
# page payload.
if [ -f "$ASSET_DIR/.vite/manifest.json" ] || [ -f "$ASSET_DIR/manifest.json" ]; then
    ok "Asset manifest present — browsers will hard-reload after this update"
else
    warn "No Vite manifest; browsers may keep running cached JavaScript."
fi

step "Building the panel binary"

BUILD_START="$(date +%s)"

# GOFLAGS/GOCACHE: root's cache, not the panel user's, and never the source
# tree. `go build` with no cache directory refuses to run at all.
export GOCACHE="${GOCACHE:-/root/.cache/go-build}"
export GOPATH="${GOPATH:-/root/go}"
export CGO_ENABLED=0

kv "go"      "$(go env GOVERSION)"
kv "Target"  "$(go env GOOS)/$(go env GOARCH), CGO disabled"

detail "Compiling (this fetches modules on the first run)…"

if ! logged_sh "cd '$SOURCE_DIR' && go build -trimpath -ldflags '-s -w' -o ubuntu-panel ./cmd/ubuntu-panel"; then
    tail -n 20 "$INSTALL_LOG" | sed 's/^/        /' >&2 || true
    die "The Go build failed. The full output is in $INSTALL_LOG."
fi

[ -x "$SOURCE_DIR/ubuntu-panel" ] || die "go build finished but produced no ubuntu-panel binary."

BUILD_SECONDS=$(( $(date +%s) - BUILD_START ))
NEW_VERSION="$("$SOURCE_DIR/ubuntu-panel" --version 2>/dev/null || echo unknown)"
NEW_BINARY_SIZE="$(file_size "$SOURCE_DIR/ubuntu-panel")"

ok "Built in ${BUILD_SECONDS}s"
kv "Version" "$NEW_VERSION"
kv "Binary"  "$(human_bytes "$NEW_BINARY_SIZE") (frontend included)"

fi  # end of the prebuilt / source split

# ========================================================= 8. install files

step "Installing to $PANEL_ROOT"

if [ "$SERVICE_WAS_ACTIVE" = 1 ]; then
    detail "Stopping ubuntu-panel for the swap…"
    # It has to actually stop: writing over a binary that is still executing
    # fails with ETXTBSY, and a partially written one is worse than either.
    logged systemctl stop ubuntu-panel || die "Could not stop the running panel; nothing was changed."
    ok "Service stopped"
fi

install -d "$PANEL_ROOT/bin"

# Writing over a running binary gives ETXTBSY, and a half-copied one is worse.
# Stage beside it, keep the old one for rollback, then rename over the top.
if [ -f "$PANEL_ROOT/bin/ubuntu-panel" ]; then
    cp -a "$PANEL_ROOT/bin/ubuntu-panel" "$PANEL_ROOT/bin/ubuntu-panel.previous"
    STAGED_BINARY=1
fi

install -m 0755 "$SOURCE_DIR/ubuntu-panel" "$PANEL_ROOT/bin/ubuntu-panel.new"
mv -f "$PANEL_ROOT/bin/ubuntu-panel.new" "$PANEL_ROOT/bin/ubuntu-panel"

if [ "$MODE" = update ] && [ "$OLD_VERSION" != "$NEW_VERSION" ]; then
    ok "Binary   $OLD_VERSION  ->  $NEW_VERSION"
elif [ "$MODE" = update ]; then
    ok "Binary   rebuilt at $NEW_VERSION ($(human_bytes "$OLD_BINARY_SIZE") -> $(human_bytes "$NEW_BINARY_SIZE"))"
else
    ok "Binary   $NEW_VERSION -> $PANEL_ROOT/bin/ubuntu-panel"
fi

install -d -o "$PANEL_USER" -g "$PANEL_USER" -m 0750 "$STATE_DIR" "$LOG_DIR"
install -d -m 0755 "$CONFIG_DIR"
install -d -m 0755 /var/www

kv "State"  "$STATE_DIR (owner $PANEL_USER, mode 0750)"
kv "Logs"   "$LOG_DIR (owner $PANEL_USER, mode 0750)"
kv "Sites"  "/var/www"

# ========================================================= 9. configuration

step "Writing configuration"

conf_has() { [ -f "$CONFIG_FILE" ] && grep -qE "^[[:space:]]*$1[[:space:]]*=" "$CONFIG_FILE"; }

# First match only, and no pipe to `head`: under `pipefail` a SIGPIPE'd sed
# would look like a failure and trip the error trap.
conf_value() {
    sed -n "/^[[:space:]]*$1[[:space:]]*=/{ s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*//; p; q; }" "$CONFIG_FILE"
}

conf_ensure() {
    if conf_has "$1"; then
        same "$1 = $(conf_value "$1")"
    else
        # Never merge two settings onto one line if the file lacks a final newline.
        if [ -s "$CONFIG_FILE" ] && [ -n "$(tail -c1 "$CONFIG_FILE")" ]; then
            printf '\n' >>"$CONFIG_FILE"
        fi
        printf '%s=%s\n' "$1" "$2" >>"$CONFIG_FILE"
        ok "$1 = $2   (added by this run)"
    fi
}

if [ ! -f "$CONFIG_FILE" ]; then
    cat >"$CONFIG_FILE" <<'EOF'
# Ubuntu Panel configuration.  KEY=value, one per line, # for comments.
# The environment overrides anything set here.
#
# PANEL_APP_KEY encrypts every stored credential — DNS tokens, database and
# mailbox passwords. Back it up with the database: losing it, or changing it,
# makes all of them unreadable.

EOF
    ok "Created $CONFIG_FILE"
else
    same "Keeping $CONFIG_FILE and merging in anything new."
fi

conf_ensure PANEL_ROOT          "$PANEL_ROOT"
conf_ensure PANEL_DATABASE      "$DB_FILE"
conf_ensure PANEL_LOG           "$LOG_DIR/panel.log"
conf_ensure PANEL_BIND_HOST     "127.0.0.1"
conf_ensure PANEL_BIND_PORT     "$PANEL_PORT"
conf_ensure PANEL_SITES_ROOT    "/var/www"
conf_ensure PANEL_SYSTEM_USER   "$PANEL_USER"

# Sized to the machine. Two queue workers are right on a server with room; on a
# 1 GB VPS they are threads held against work that arrives once an hour, next
# to a MariaDB that needs every megabyte it can get.
if [ "$MEM_KB" -lt 1572864 ]; then     # under 1.5 GB
    conf_ensure PANEL_WORKERS   "1"
else
    conf_ensure PANEL_WORKERS   "2"
fi

# What a first start puts on the machine by itself. `all` is every app on the
# Apps page, which is what makes a fresh server ready to host a site without
# anybody installing anything first; `core` is only what a site cannot run
# without; `none` leaves it to you. It happens once, on a panel that has never
# installed an app.
conf_ensure PANEL_INSTALL_APPS  "${PANEL_INSTALL_APPS:-all}"

conf_ensure PANEL_REPOSITORY    "https://github.com/shusanto294/Ubuntu-Panel-Go"
conf_ensure PANEL_UPDATE_BRANCH "main"

# The Go binary embeds its frontend. A PANEL_PUBLIC left behind by the C panel
# would point the debug asset server at a stale public/ tree, so it goes.
if conf_has PANEL_PUBLIC; then
    sed -i '/^[[:space:]]*PANEL_PUBLIC[[:space:]]*=/d' "$CONFIG_FILE"
    ok "Removed PANEL_PUBLIC — the frontend is inside the binary now"
fi

# Same story for the repository: an install carried over from the C panel would
# check the wrong tree for updates forever.
CURRENT_REPO="$(conf_value PANEL_REPOSITORY)"
case "$CURRENT_REPO" in
    https://github.com/shusanto294/Ubuntu-Panel|https://github.com/shusanto294/Ubuntu-Panel-C)
        sed -i "s|^\([[:space:]]*PANEL_REPOSITORY[[:space:]]*=[[:space:]]*\).*$|\1https://github.com/shusanto294/Ubuntu-Panel-Go|" "$CONFIG_FILE"
        ok "PANEL_REPOSITORY repointed at Ubuntu-Panel-Go (it named an older panel)"
        ;;
esac

# The application key has to exist before the first start, and it has to be
# written here rather than by the panel: the unit runs with ProtectSystem=full
# and the file is not writable by the panel's user, so a key the panel minted
# for itself could never be persisted — it would mint a fresh one on every
# restart and silently orphan every encrypted column in the database.
if conf_has PANEL_APP_KEY; then
    same "PANEL_APP_KEY present (not shown, not changed)"
else
    APP_KEY="$(openssl rand -base64 32)"
    [ "${#APP_KEY}" -ge 44 ] || die "Could not generate an application key with openssl."

    {
        printf '\n# Generated by the installer. Back this up with the database.\n'
        printf 'PANEL_APP_KEY=%s\n' "$APP_KEY"
    } >>"$CONFIG_FILE"
    unset APP_KEY

    ok "PANEL_APP_KEY generated (32 random bytes, base64)"

    if [ "$MODE" = update ]; then
        warn "This install had no stored key, so any credential encrypted before now is unreadable."
        detail "Re-enter DNS API tokens under DNS if they stop working."
    fi
fi

chmod 640 "$CONFIG_FILE"
chown root:"$PANEL_USER" "$CONFIG_FILE"
kv "Permissions" "0640 root:$PANEL_USER — the panel reads it, only root writes it"

# ========================================================== 10. systemd unit

step "Installing the systemd unit"

UNIT_NEW="$(mktemp)"
sed "s|^User=.*|User=${PANEL_USER}|; \
     s|^Group=.*|Group=${PANEL_USER}|; \
     s|^WorkingDirectory=.*|WorkingDirectory=${PANEL_ROOT}|; \
     s|^ExecStart=.*|ExecStart=${PANEL_ROOT}/bin/ubuntu-panel --config ${CONFIG_FILE}|" \
    "$SOURCE_DIR/scripts/ubuntu-panel.service" >"$UNIT_NEW"

if [ -f "$UNIT_FILE" ] && cmp -s "$UNIT_NEW" "$UNIT_FILE"; then
    same "$UNIT_FILE is already up to date."
    rm -f "$UNIT_NEW"
else
    install -m 0644 "$UNIT_NEW" "$UNIT_FILE"
    rm -f "$UNIT_NEW"
    ok "Wrote $UNIT_FILE"
    logged systemctl daemon-reload || die "systemctl daemon-reload failed. See $INSTALL_LOG."
    ok "systemd configuration reloaded"
fi

kv "Runs as"   "$(awk -F= '/^User=/{print $2; exit}' "$UNIT_FILE")"
kv "ExecStart" "$PANEL_ROOT/bin/ubuntu-panel --config $CONFIG_FILE"

if systemctl is-enabled --quiet ubuntu-panel 2>/dev/null; then
    same "Already enabled at boot."
else
    logged systemctl enable ubuntu-panel || die "Could not enable ubuntu-panel. See $INSTALL_LOG."
    ok "Enabled at boot"
fi

# ================================================================ 11. nginx

step "Configuring nginx"

install -d -m 0755 /var/www/letsencrypt/.well-known/acme-challenge
chown -R www-data:www-data /var/www/letsencrypt 2>/dev/null || true

WRITE_DEFAULT_VHOST=1

if [ -f "$NGINX_SITE" ]; then
    VHOST_NAME="$(awk '/^[[:space:]]*server_name/{gsub(/;/,""); print $2; exit}' "$NGINX_SITE" 2>/dev/null || true)"

    if grep -q 'ssl_certificate' "$NGINX_SITE"; then
        WRITE_DEFAULT_VHOST=0
        note "Keeping the HTTPS vhost the panel wrote for ${VHOST_NAME:-your domain}."
        detail "Overwriting it would drop the panel back to plain HTTP and break the redirect."
    elif [ -n "$VHOST_NAME" ] && [ "$VHOST_NAME" != "_" ]; then
        WRITE_DEFAULT_VHOST=0
        note "Keeping the vhost for ${VHOST_NAME} (set from Settings -> Panel domain)."
    fi
fi

if [ "$WRITE_DEFAULT_VHOST" = 1 ]; then
    # Plain HTTP on the server's address to begin with. Once you have a
    # hostname, Settings -> Panel domain writes the TLS vhost and requests a
    # certificate — and re-running this script will then leave it alone.
    cat >"$NGINX_SITE" <<EOF
# Managed by the Ubuntu Panel installer.
server {
    listen 80 default_server;
    listen [::]:80 default_server;
    server_name _;

    location ^~ /.well-known/acme-challenge/ {
        root /var/www/letsencrypt;
        default_type "text/plain";
    }

    # phpMyAdmin, when it is installed — a 404 until then. Kept in step with
    # PhpMyAdminLocation in internal/sites/nginx.go, which is what rewrites
    # this vhost once the panel is running and knows which PHP is on the box.
    location ^~ /phpmyadmin {
        root /usr/share/ubuntu-panel;
        index index.php;
        try_files \$uri \$uri/ =404;

        location ~ ^/phpmyadmin/.+\.php\$ {
            root /usr/share/ubuntu-panel;
            include snippets/fastcgi-php.conf;
            fastcgi_pass unix:/run/php/php${PANEL_PHP_VERSION}-fpm.sock;
            fastcgi_read_timeout 300;
            fastcgi_param SCRIPT_FILENAME \$document_root\$fastcgi_script_name;
        }

        location ~ ^/phpmyadmin/(libraries|templates|setup)/ {
            deny all;
        }
    }

    location / {
        proxy_pass http://127.0.0.1:${PANEL_PORT};
        proxy_http_version 1.1;

        # The browser terminal is a websocket on this same origin.
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";

        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;

        # A deployment console streams for minutes and a terminal session is
        # idle for long stretches; the default 60s would drop both.
        proxy_read_timeout 3600;
    }
}
EOF
    ok "Wrote $NGINX_SITE — port 80, proxying to 127.0.0.1:$PANEL_PORT"

    # nginx allows exactly one default_server per address, and Debian ships one.
    # A second is not a warning: nginx refuses the whole configuration.
    if [ -e /etc/nginx/sites-enabled/default ]; then
        rm -f /etc/nginx/sites-enabled/default
        ok "Removed the distribution default_server vhost, which would clash"
    fi
else
    # Still make sure the vhost we kept points at the port we are about to bind.
    if grep -q "127.0.0.1:${PANEL_PORT}" "$NGINX_SITE"; then
        same "The kept vhost already proxies to 127.0.0.1:${PANEL_PORT}."
    else
        WAS="$(grep -o 'proxy_pass http://127\.0\.0\.1:[0-9]*' "$NGINX_SITE" | head -1 || true)"
        sed -i "s|proxy_pass http://127\.0\.0\.1:[0-9]*|proxy_pass http://127.0.0.1:${PANEL_PORT}|g" "$NGINX_SITE"
        ok "Repointed ${WAS:-the proxy} at 127.0.0.1:${PANEL_PORT}"
    fi
fi

ln -sfn "$NGINX_SITE" /etc/nginx/sites-enabled/ubuntu-panel

# Heal a duplicate default server before testing the configuration.
#
# A panel built before this check wrote its catch-all with `listen 80
# default_server` while this vhost already had one — and nginx does not warn
# about a second, it refuses the whole configuration, so the machine is left
# with an nginx that will not reload and an update that cannot get past
# `nginx -t`. The catch-all is the panel's own file and the one that yields:
# this vhost holds port 80 so the panel keeps answering on the server's bare
# address.
CATCH_ALL=/etc/nginx/conf.d/000-panel-default.conf

if [ -f "$CATCH_ALL" ] && grep -qE '^[[:space:]]*listen[[:space:]]+(\[::\]:)?80[^;]*default_server' "$CATCH_ALL" \
   && grep -qE '^[[:space:]]*listen[[:space:]]+(\[::\]:)?80[^;]*default_server' "$NGINX_SITE"; then
    sed -i -E '/^[[:space:]]*listen[[:space:]]+(\[::\]:)?80[^;]*default_server/s/ default_server//' "$CATCH_ALL"
    ok "Removed a duplicate port 80 default server from $CATCH_ALL"
fi

if logged nginx -t; then
    ok "nginx accepted the configuration"
else
    tail -n 20 "$INSTALL_LOG" | sed 's/^/        /' >&2 || true
    die "nginx rejected the configuration — nothing was reloaded."
fi

if systemctl is-enabled --quiet nginx 2>/dev/null; then
    same "nginx already enabled at boot."
else
    logged systemctl enable nginx || warn "Could not enable nginx at boot; check $INSTALL_LOG."
    ok "nginx enabled at boot"
fi

if systemctl is-active --quiet nginx; then
    logged systemctl reload nginx || die "nginx would not reload. See $INSTALL_LOG."
    ok "nginx reloaded"
else
    logged systemctl start nginx || die "nginx would not start. See $INSTALL_LOG."
    ok "nginx started"
fi

# ====================================================== 12. start the panel

step "Starting the panel"

# A failure here falls through to the health check below, which prints the
# journal and rolls back — much more useful than the generic error trap.
logged systemctl start ubuntu-panel || true

detail "Waiting for it to answer on 127.0.0.1:${PANEL_PORT}…"

HEALTHY=0

for attempt in $(seq 1 30); do
    if ! systemctl is-active --quiet ubuntu-panel; then
        break
    fi

    # /up is the panel's own health endpoint, and it answers before anything
    # else is configured.
    if curl -fs --max-time 3 "http://127.0.0.1:${PANEL_PORT}/up" >/dev/null 2>&1; then
        HEALTHY=1
        ok "Answering after ${attempt}s (GET /up)"
        break
    fi

    sleep 1
done

if [ "$HEALTHY" != 1 ]; then
    warn "The panel did not answer. Recent journal:"
    journalctl -u ubuntu-panel --no-pager --lines=30 2>/dev/null | sed 's/^/        /' >&2 || true
    if [ -f "$LOG_DIR/panel.log" ]; then
        tail -n 20 "$LOG_DIR/panel.log" | sed 's/^/        /' >&2 || true
    fi

    systemctl stop ubuntu-panel >/dev/null 2>&1 || true
    rollback_if_staged
    die "The panel did not come up, so nothing was left half-installed."
fi

# It is up, so the staged copy is no longer needed for a rollback.
STAGED_BINARY=0
rm -f "$PANEL_ROOT/bin/ubuntu-panel.previous"
rm -rf "$PANEL_ROOT/public.previous"

MAIN_PID="$(systemctl show ubuntu-panel -p MainPID --value 2>/dev/null || echo 0)"
if [ "${MAIN_PID:-0}" -gt 0 ] && [ -r "/proc/$MAIN_PID/status" ]; then
    RSS_KB="$(awk '/^VmRSS:/{print $2}' "/proc/$MAIN_PID/status")"
    kv "PID"             "$MAIN_PID"
    kv "Resident memory" "$(human_bytes $((RSS_KB * 1024)))"
fi

if [ -f "$DB_FILE" ]; then
    TABLE_COUNT="$(sqlite3 "$DB_FILE" "SELECT count(*) FROM sqlite_master WHERE type='table';" 2>/dev/null || echo '?')"
    ACCOUNTS="$(sqlite3 "$DB_FILE" 'SELECT count(*) FROM users;' 2>/dev/null || echo 0)"
    SITES="$(sqlite3 "$DB_FILE" 'SELECT count(*) FROM sites;' 2>/dev/null || echo 0)"

    kv "Schema"   "$TABLE_COUNT tables (migrated on start)"
    kv "Accounts" "$ACCOUNTS"
    kv "Sites"    "$SITES"
fi

# ============================================================ 13. finishing

step "Finishing up"

# The panel's update check compares this against the tip of the update branch.
# Without it, an installed panel can never tell you an update exists.
if [ -n "$SOURCE_COMMIT" ] && printf '%s' "$SOURCE_COMMIT" | grep -qE '^[0-9a-f]{40}$' && [ -f "$DB_FILE" ]; then
    # The commit, and when it landed here: the version card shows "installed
    # <date>" under what is running, and the update check compares the commit
    # against what the repository has published.
    if sudo -u "$PANEL_USER" sqlite3 "$DB_FILE" \
        "INSERT INTO settings(key, value, secret, created_at, updated_at)
         VALUES('installed_commit', '$SOURCE_COMMIT', 0, datetime('now'), datetime('now'))
         ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = datetime('now');
         INSERT INTO settings(key, value, secret, created_at, updated_at)
         VALUES('installed_at', strftime('%Y-%m-%dT%H:%M:%SZ', 'now'), 0, datetime('now'), datetime('now'))
         ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = datetime('now');" \
        >>"$INSTALL_LOG" 2>&1
    then
        ok "Recorded installed commit ${SOURCE_COMMIT:0:12} — the panel can now report updates"
    else
        warn "Could not record the installed commit; update notifications will stay quiet."
    fi
else
    same "No git commit to record, so the panel will not report available updates."
fi

if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q 'Status: active'; then
    logged ufw allow 'Nginx Full' || true
    ok "ufw: allowed Nginx Full (ports 80 and 443)"
else
    same "ufw is not active; no firewall rules were changed."
fi

chmod 640 "$INSTALL_LOG" 2>/dev/null || true

IP="$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i <= NF; i++) if ($i == "src") print $(i + 1)}' | head -1 || true)"
PANEL_URL="http://${IP:-this-server}/"

if [ -f "$NGINX_SITE" ] && grep -q 'ssl_certificate' "$NGINX_SITE"; then
    DOMAIN="$(awk '/^[[:space:]]*server_name/{gsub(/;/,""); print $2; exit}' "$NGINX_SITE" 2>/dev/null || true)"
    if [ -n "$DOMAIN" ] && [ "$DOMAIN" != "_" ]; then
        PANEL_URL="https://${DOMAIN}/"
    fi
fi

if [ "$MODE" = update ]; then
    HEADLINE="Ubuntu Panel updated."
else
    HEADLINE="Ubuntu Panel installed."
fi

printf '\n%s==> %s%s\n\n' "$GREEN$BOLD" "$HEADLINE" "$RESET"

if [ "$MODE" = update ] && [ "$OLD_VERSION" != "$NEW_VERSION" ]; then
    printf '    %-13s %s  ->  %s\n' "Version" "$OLD_VERSION" "$NEW_VERSION"
else
    printf '    %-13s %s\n' "Version" "$NEW_VERSION"
fi

printf '    %-13s %s\n' "URL" "$PANEL_URL"

if [ "$MODE" = install ]; then
    printf '    %-13s open the URL — it will ask you to create the first one\n' "Account"

    APPS_CHOICE="$(conf_value PANEL_INSTALL_APPS)"

    case "${APPS_CHOICE:-all}" in
        none)
            printf '    %-13s nothing installed yet — pick what you need on the Apps page\n' "Apps"
            ;;
        *)
            printf '    %-13s installing %s in the background; watch it on the Apps page\n' \
                "Apps" "${APPS_CHOICE:-all}"
            printf '    %-13s nginx, PHP, MariaDB and the rest land over the next few minutes\n' ""
            ;;
    esac
fi

printf '\n'
printf '    %-13s systemctl status ubuntu-panel\n' "Service"
printf '    %-13s journalctl -u ubuntu-panel -f\n' "Journal"
printf '    %-13s %s/panel.log\n'                  "Panel log" "$LOG_DIR"
printf '    %-13s %s\n'                            "Install log" "$INSTALL_LOG"
printf '    %-13s %s\n'                            "Database" "$DB_FILE"

if [ -d "$BACKUP_DIR" ]; then
    printf '    %-13s %s\n' "Backups" "$BACKUP_DIR"
fi

printf '    %-13s %s  %s(back this up with the database)%s\n' "App key" "$CONFIG_FILE" "$DIM" "$RESET"
printf '\n'

if [ "$BUILD_MODE" = prebuilt ]; then
    printf '    To update, run the one-liner again:\n\n'
    printf '        curl -fsSL %s | sudo bash\n\n' "$GET_URL"
else
    printf '    Re-run this script any time to update.\n\n'
fi

printf '    Either way it keeps the database, the application key, and the\n'
printf '    panel'\''s own nginx vhost.\n\n'
