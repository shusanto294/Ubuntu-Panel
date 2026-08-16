#!/usr/bin/env bash
#
# Build the release tarballs.
#
#   ./scripts/build-release.sh            # both architectures
#   ./scripts/build-release.sh amd64      # just one
#
# Each tarball is what get.sh downloads and install.sh runs from: a Linux
# binary with the frontend already inside it, the systemd unit, and the
# installer. Nothing is compiled on the server.
#
# The frontend is built once — it is architecture-independent, and it is
# embedded into both binaries by the Go build.
#
# Run this anywhere Go and Node run; CGO is off, so cross-compiling needs no
# toolchain for the other architecture.
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

DIST="$ROOT/dist"
NAME=ubuntu-panel

VERSION="${PANEL_VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
VERSION="${VERSION#v}"
COMMIT="$(git rev-parse HEAD 2>/dev/null || echo unknown)"
BUILT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

ARCHES=("$@")
[ "${#ARCHES[@]}" -gt 0 ] || ARCHES=(amd64 arm64)

say() { printf '\n==> %s\n' "$*"; }

say "Ubuntu Panel $VERSION (${COMMIT:0:12})"

command -v go   >/dev/null 2>&1 || { echo "go is not installed" >&2; exit 1; }
command -v node >/dev/null 2>&1 || { echo "node is not installed" >&2; exit 1; }

# ------------------------------------------------------------- the frontend

say "Building the frontend"

[ -d node_modules ] || npm ci --no-audit --no-fund
npm run build

ASSETS=internal/assets/public/build
[ -d "$ASSETS" ] || { echo "vite produced no $ASSETS" >&2; exit 1; }

printf '    %s file(s)\n' "$(find "$ASSETS" -type f | wc -l | tr -d ' ')"

# --------------------------------------------------------------- binaries

rm -rf "$DIST"
mkdir -p "$DIST"

for ARCH in "${ARCHES[@]}"; do
    say "Building linux/$ARCH"

    STAGE="$DIST/${NAME}-${VERSION}-linux-${ARCH}"
    mkdir -p "$STAGE/scripts"

    CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" \
        go build -trimpath -ldflags "-s -w" -o "$STAGE/$NAME" ./cmd/ubuntu-panel

    install -m 0755 install.sh "$STAGE/install.sh"
    install -m 0644 scripts/ubuntu-panel.service "$STAGE/scripts/ubuntu-panel.service"
    [ -f README.md ] && install -m 0644 README.md "$STAGE/README.md" || true

    cat >"$STAGE/RELEASE" <<EOF
# What this release is, read by install.sh.
version = $VERSION
commit  = $COMMIT
arch    = $ARCH
built   = $BUILT
base    = $(uname -srm)
EOF

    TARBALL="${NAME}-${VERSION}-linux-${ARCH}.tar.gz"

    tar -czf "$DIST/$TARBALL" -C "$DIST" "$(basename "$STAGE")"
    rm -rf "$STAGE"

    # get.sh fetches this alongside the tarball and refuses a mismatch.
    (cd "$DIST" && sha256sum "$TARBALL" >"$TARBALL.sha256")

    printf '    %s (%s KB)\n' "$TARBALL" "$(( $(wc -c <"$DIST/$TARBALL") / 1024 ))"
done

say "Done — $DIST"
ls -1 "$DIST"

cat <<EOF

Publish with:

    gh release create v$VERSION dist/*.tar.gz dist/*.sha256 --generate-notes

Then the one-liner installs it:

    curl -fsSL https://raw.githubusercontent.com/shusanto294/Ubuntu-Panel/main/get.sh | sudo bash
EOF
