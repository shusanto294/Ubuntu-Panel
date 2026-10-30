#!/usr/bin/env bash
#
# Ubuntu Panel — one-command install.
#
#   curl -fsSL https://raw.githubusercontent.com/shusanto294/Ubuntu-Panel/main/get.sh | sudo bash
#
# It finds the newest release, downloads the tarball for this machine's
# architecture, checks it against the published SHA-256, unpacks it and runs
# the installer inside it. Nothing is compiled on this server: the binary was
# built somewhere else, and the frontend is inside it. No Go, no Node, no
# compiler, and no build memory on a machine whose job is to serve sites.
#
# Building here is possible and never automatic: `--from-source` asks for it
# explicitly. Falling back to it silently is how a one-minute install becomes a
# five-minute one that leaves a Go toolchain behind on a 1 GB VPS.
#
# Re-running it is the update path, and the installer keeps the database, the
# application key and the panel's own nginx vhost.
#
# Options, as environment variables or flags:
#
#   PANEL_VERSION=v1.0.0     install a specific tag rather than the newest
#   PANEL_FROM_SOURCE=1      clone the repository and build here instead
#   PANEL_KEEP_DOWNLOAD=1    leave the unpacked release behind after installing
#   NO_COLOR=1               plain output
#
# Anything else on the command line is passed through to install.sh's own
# environment — PANEL_PUBLIC_PORT=9443, PANEL_ROOT=/srv/panel and so on all work:
#
#   curl -fsSL <url> | sudo PANEL_PORT=9000 bash
#
set -Eeuo pipefail

REPO="${PANEL_REPO:-shusanto294/Ubuntu-Panel}"
REPO_URL="https://github.com/$REPO"
API="https://api.github.com/repos/$REPO"

PANEL_VERSION="${PANEL_VERSION:-}"
PANEL_FROM_SOURCE="${PANEL_FROM_SOURCE:-0}"
PANEL_KEEP_DOWNLOAD="${PANEL_KEEP_DOWNLOAD:-0}"

usage() {
    cat <<'EOF'
Ubuntu Panel — one-command install.

  curl -fsSL https://raw.githubusercontent.com/shusanto294/Ubuntu-Panel/main/get.sh | sudo bash

Options (flags need `bash -s --` when piped):

  --version TAG    install a specific release rather than the newest
  --from-source    clone the repository and build on this machine instead
  --keep           leave the unpacked release behind after installing

Environment: PANEL_VERSION, PANEL_FROM_SOURCE, PANEL_KEEP_DOWNLOAD, NO_COLOR,
plus anything install.sh reads (PANEL_PUBLIC_PORT, PANEL_ROOT, PANEL_USER, …):

  curl -fsSL <url> | sudo PANEL_PORT=9000 bash
EOF
}

while [ $# -gt 0 ]; do
    case "$1" in
        --version)     PANEL_VERSION="${2:?--version needs a tag}"; shift 2 ;;
        --from-source) PANEL_FROM_SOURCE=1; shift ;;
        --keep)        PANEL_KEEP_DOWNLOAD=1; shift ;;
        -h|--help)     usage; exit 0 ;;
        *)             printf 'Unknown option: %s\n' "$1" >&2; usage >&2; exit 1 ;;
    esac
done

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
    BOLD=$'\033[1m'; DIM=$'\033[2m'; RED=$'\033[31m'
    GREEN=$'\033[32m'; YELLOW=$'\033[33m'; RESET=$'\033[0m'
else
    BOLD=''; DIM=''; RED=''; GREEN=''; YELLOW=''; RESET=''
fi

say()  { printf '%s==>%s %s\n' "$BOLD" "$RESET" "$*"; }
ok()   { printf '    %s+%s %s\n' "$GREEN" "$RESET" "$*"; }
info() { printf '    %s%s%s\n' "$DIM" "$*" "$RESET"; }
warn() { printf '    %s!%s %s\n' "$YELLOW" "$RESET" "$*"; }
die()  { printf '\n%s!! %s%s\n' "$RED" "$*" "$RESET" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "Run this as root:  curl -fsSL https://raw.githubusercontent.com/$REPO/main/get.sh | sudo bash"

command -v curl >/dev/null 2>&1 || die "curl is required and is not installed."
command -v tar  >/dev/null 2>&1 || die "tar is required and is not installed."

# ------------------------------------------------- not from inside the panel

# The panel's own terminal is a process of the panel's service, and so is
# everything typed into it — this script included. The installer stops that
# service to swap the binary, systemd stops every process in it, and the
# update dies with the panel it had just stopped and never starts it again.
# So from in there, start again in a unit of its own, which is what the
# panel's Update button does, and follow its log until the panel goes away.
if grep -qE '/ubuntu-panel\.service(/|$)' /proc/self/cgroup 2>/dev/null; then
    command -v systemd-run >/dev/null 2>&1 \
        || die "This is the panel's own terminal, which the update would kill. Run it over SSH instead."

    passed=(--setenv=PANEL_VERSION="$PANEL_VERSION" --setenv=PANEL_FROM_SOURCE="$PANEL_FROM_SOURCE"
        --setenv=PANEL_KEEP_DOWNLOAD="$PANEL_KEEP_DOWNLOAD" --setenv=PANEL_REPO="$REPO")
    while IFS= read -r variable; do
        case "$variable" in
            PANEL_VERSION=*|PANEL_FROM_SOURCE=*|PANEL_KEEP_DOWNLOAD=*|PANEL_REPO=*) ;;
            PANEL_*=*|NO_COLOR=*) passed+=(--setenv="$variable") ;;
        esac
    done < <(env)

    systemctl reset-failed ubuntu-panel-update >/dev/null 2>&1 || true
    systemd-run --unit=ubuntu-panel-update --description='Ubuntu Panel update' --collect \
        "${passed[@]}" /bin/bash -c "curl -fsSL https://raw.githubusercontent.com/$REPO/main/get.sh | bash" \
        >/dev/null || die "Could not start the update outside the panel. Run it over SSH instead."

    printf '\n%s==> Updating outside the panel%s\n\n' "$BOLD" "$RESET"
    info "This terminal is part of the panel, and the update restarts the panel."
    info "It is running in its own service instead; this terminal will disconnect"
    info "when the panel restarts. Reload the page in a minute or two."
    info "Progress: journalctl -u ubuntu-panel-update   Log: /var/log/ubuntu-panel/install.log"
    printf '\n'

    exec journalctl -u ubuntu-panel-update -f -n 50 --no-hostname -o cat
fi

printf '\n%s==> Ubuntu Panel%s\n\n' "$BOLD" "$RESET"

# ------------------------------------------------------------ architecture

MACHINE="$(uname -m)"
case "$MACHINE" in
    x86_64|amd64)  ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) die "No prebuilt release for $MACHINE. Build from source: PANEL_FROM_SOURCE=1" ;;
esac

if [ -r /etc/os-release ]; then
    # shellcheck disable=SC1091
    . /etc/os-release
    info "${PRETTY_NAME:-unknown system} on $ARCH"
else
    info "$MACHINE — no /etc/os-release"
fi

# Unlike the C panel's releases, this binary is static: no glibc version to
# match, no OpenSSL to find. Any Linux of this architecture runs it.

TMP="$(mktemp -d /tmp/ubuntu-panel-XXXXXX)"
cleanup() {
    if [ "$PANEL_KEEP_DOWNLOAD" = "1" ]; then
        printf '    %sLeft the download in %s%s\n' "$DIM" "$TMP" "$RESET"
    else
        rm -rf "$TMP"
    fi
}
trap cleanup EXIT

# ----------------------------------------------------------- source build

# Only ever reached through --from-source or PANEL_FROM_SOURCE=1. This is the
# path that installs a Go toolchain and Node on the server.
build_from_source() {
    say "Installing from source"

    warn "This compiles on this machine: it installs Go and Node here and needs a"
    warn "few minutes and ~1 GB of memory. Publishing a release builds it on GitHub"
    warn "instead, and the install becomes a download."

    export DEBIAN_FRONTEND=noninteractive
    command -v git >/dev/null 2>&1 || {
        info "Installing git…"
        apt-get -o "DPkg::Lock::Timeout=600" update -qq
        apt-get -o "DPkg::Lock::Timeout=600" install -y -qq git
    }

    BRANCH="${PANEL_VERSION:-main}"
    info "Cloning $REPO_URL at $BRANCH…"
    git clone --depth 1 --branch "$BRANCH" "$REPO_URL" "$TMP/src" >/dev/null 2>&1 \
        || git clone --depth 1 "$REPO_URL" "$TMP/src" >/dev/null 2>&1 \
        || die "Could not clone $REPO_URL"

    ok "Cloned into $TMP/src"
    info "install.sh will fetch Go and Node if this machine does not have them."
    printf '\n'

    # exec, so the installer owns the terminal and its exit status is ours. The
    # EXIT trap goes with the replaced process, so the checkout stays put — it
    # is under /tmp and the machine clears it on the next boot.
    trap - EXIT
    exec env PANEL_FROM_SOURCE=1 bash "$TMP/src/install.sh"
}

if [ "$PANEL_FROM_SOURCE" = "1" ]; then
    build_from_source
fi

# ------------------------------------------------------------- the release

say "Finding the release"

# No jq on a fresh box, so the release JSON is read by hand. Asking the API
# for the asset URL rather than assembling one means the tag and the version
# inside the panel do not have to agree.
#
# The status code is kept rather than -f'd away: "no release has been
# published" and "GitHub is rate-limiting this IP" are different problems with
# different answers, and guessing between them wastes someone's evening.
if [ -n "$PANEL_VERSION" ]; then
    API_URL="$API/releases/tags/$PANEL_VERSION"
    info "Pinned to $PANEL_VERSION"
else
    API_URL="$API/releases/latest"
fi

HTTP_CODE="$(curl -sSL --max-time 30 -o "$TMP/release.json" -w '%{http_code}' "$API_URL" 2>/dev/null || echo 000)"
RELEASE_JSON="$(cat "$TMP/release.json" 2>/dev/null || true)"

case "$HTTP_CODE" in
    200) ;;
    404)
        if [ -n "$PANEL_VERSION" ]; then
            die "No release tagged $PANEL_VERSION at $REPO_URL/releases."
        fi

        printf '\n%s!! No release has been published yet, so there is no binary to download.%s\n' \
            "$RED" "$RESET" >&2
        printf '\n   Publish one — the tag builds it on GitHub and attaches the tarballs:\n\n' >&2
        printf '       git tag v1.0.0 && git push origin v1.0.0\n\n' >&2
        printf '   Then this command works as written. To build on this server instead,\n' >&2
        printf '   which installs Go and Node here:\n\n' >&2
        printf '       curl -fsSL https://raw.githubusercontent.com/%s/main/get.sh | sudo bash -s -- --from-source\n\n' "$REPO" >&2
        exit 1
        ;;
    403|429)
        die "GitHub is rate-limiting this IP (HTTP $HTTP_CODE). Try again shortly, pin a release with PANEL_VERSION=v1.0.0, or build here with PANEL_FROM_SOURCE=1."
        ;;
    000)
        die "Could not reach api.github.com at all — check the server's DNS and outbound HTTPS."
        ;;
    *)
        die "The GitHub release API answered HTTP $HTTP_CODE. Build from source with PANEL_FROM_SOURCE=1."
        ;;
esac

[ -n "$RELEASE_JSON" ] || die "The GitHub release API returned an empty body. Build from source with PANEL_FROM_SOURCE=1."

TAG="$(printf '%s' "$RELEASE_JSON" \
       | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1 || true)"
[ -n "$TAG" ] || die "The GitHub release API returned something unexpected. Build from source with PANEL_FROM_SOURCE=1."

info "Release $TAG"

# One URL per line, then the one built for this architecture.
URL="$(printf '%s' "$RELEASE_JSON" \
       | tr ',' '\n' \
       | sed -n 's/.*"browser_download_url"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
       | grep -E "linux-${ARCH}\.tar\.gz$" | head -1 || true)"

# Whether the release actually lists a build for this architecture. It decides
# what a failed download means: a release missing the asset is one thing, and a
# connection that died halfway through fetching an asset that is right there is
# another entirely — and telling somebody the release has no build for their
# machine when it does sends them looking in the wrong place.
ASSET_LISTED=1

if [ -z "$URL" ]; then
    # An older release, or one published by hand, may not list assets the same
    # way. The conventional name is the fallback.
    URL="$REPO_URL/releases/download/$TAG/ubuntu-panel-${TAG#v}-linux-${ARCH}.tar.gz"
    ASSET_LISTED=0
    info "No $ARCH asset listed; trying the conventional name."
fi

TARBALL="${URL##*/}"

say "Downloading $TARBALL"

# Retried, because this is the step that fails on a working server. GitHub
# serves release assets from object storage, and a connection that dies partway
# is common enough on some hosts to be the normal way an install ends —
# curl: (56), five seconds in, on a file that downloads perfectly the next
# time. --retry-all-errors because the default only retries a handful of
# transient HTTP codes, and this is not an HTTP code at all.
# --retry-all-errors arrived in curl 7.71, and Ubuntu 20.04 ships 7.68 — where
# passing it is not a weaker retry but an immediate "option is unknown".
RETRY_ALL=""
curl --help all 2>/dev/null | grep -q -- '--retry-all-errors' && RETRY_ALL="--retry-all-errors"

STATUS=0
curl -fSL --progress-bar --max-time 600 \
     --retry 5 --retry-delay 3 $RETRY_ALL \
     -o "$TMP/$TARBALL" "$URL" || STATUS=$?

if [ "$STATUS" -ne 0 ]; then
    # 22 is curl's "the server said no" — a 404, which for a URL taken out of
    # the release itself should not happen, and for the guessed one means the
    # release has no such file.
    if [ "$STATUS" -eq 22 ] || [ "$ASSET_LISTED" -eq 0 ]; then
        die "Release $TAG has no $ARCH build. Check $REPO_URL/releases, or build on this server with --from-source."
    fi

    printf '\n' >&2
    printf '!! Could not download %s (curl exit %s).\n' "$TARBALL" "$STATUS" >&2
    printf '   The release has the file — %s\n' "$URL" >&2
    printf '   — so this is the connection to GitHub, not a missing build. It was retried five times.\n\n' >&2
    printf '   Run the same command again, or build on this server instead:\n' >&2
    printf '       curl -fsSL https://raw.githubusercontent.com/%s/main/get.sh | sudo bash -s -- --from-source\n\n' "$REPO" >&2
    exit 1
fi

SIZE="$(wc -c <"$TMP/$TARBALL" | tr -d ' ')"
ok "$TARBALL ($((SIZE / 1024)) KB)"

# ---------------------------------------------------------------- checksum

if curl -fsSL --max-time 60 --retry 3 --retry-delay 2 $RETRY_ALL \
        -o "$TMP/$TARBALL.sha256" "$URL.sha256" 2>/dev/null; then
    WANT="$(awk '{print $1; exit}' "$TMP/$TARBALL.sha256")"
    GOT="$(sha256sum "$TMP/$TARBALL" | awk '{print $1}')"

    if [ "$WANT" != "$GOT" ]; then
        die "Checksum mismatch — the download is corrupt or tampered with. Expected $WANT, got $GOT."
    fi
    ok "SHA-256 verified"
else
    warn "No published checksum for this release; skipping verification."
fi

# ----------------------------------------------------------------- install

say "Unpacking"

tar -xzf "$TMP/$TARBALL" -C "$TMP" || die "Could not unpack $TARBALL."

RELEASE_DIR="$TMP/${TARBALL%.tar.gz}"
[ -d "$RELEASE_DIR" ] || RELEASE_DIR="$(find "$TMP" -maxdepth 1 -type d -name 'ubuntu-panel-*' | head -1)"
[ -n "$RELEASE_DIR" ] && [ -f "$RELEASE_DIR/install.sh" ] \
    || die "The tarball does not contain an install.sh — it is not a panel release."

ok "Unpacked to $RELEASE_DIR"

say "Running the installer"
printf '\n'

chmod +x "$RELEASE_DIR/install.sh"
trap - EXIT

if [ "$PANEL_KEEP_DOWNLOAD" = "1" ]; then
    exec bash "$RELEASE_DIR/install.sh"
fi

STATUS=0
bash "$RELEASE_DIR/install.sh" || STATUS=$?

if [ "$STATUS" -ne 0 ]; then
    printf '\n    %sThe unpacked release is still at %s%s\n' "$DIM" "$RELEASE_DIR" "$RESET" >&2
    exit "$STATUS"
fi

rm -rf "$TMP"
