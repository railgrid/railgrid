#!/bin/sh
# kedge CLI installer.
#
# Usage:
#   curl -fsSL https://downloads.faros.sh/install.sh | sh
#
# Environment variables:
#   KEDGE_VERSION    Install a specific version (default: latest GitHub release).
#   INSTALL_DIR      Target directory (default: $HOME/.local/bin — no sudo
#                    required). To install system-wide instead:
#                      curl -fsSL https://downloads.faros.sh/install.sh \
#                        | INSTALL_DIR=/usr/local/bin sudo -E sh
#   KEDGE_BASE_URL   Override the binary download base (default:
#                    https://downloads.faros.sh/cli/kedge).

set -eu

REPO="faroshq/kedge"
INSTALL_DIR="${INSTALL_DIR:-${HOME}/.local/bin}"
VERSION="${KEDGE_VERSION:-}"
BASE_URL="${KEDGE_BASE_URL:-https://downloads.faros.sh/cli/kedge}"

err() { printf 'error: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || err "missing required tool: $1"; }

need curl
need tar
need uname

os="$(uname -s)"
case "$os" in
    Linux)  os=linux ;;
    Darwin) os=darwin ;;
    *)      err "unsupported OS: $os (linux, darwin only)" ;;
esac

arch="$(uname -m)"
case "$arch" in
    x86_64|amd64)  arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    ppc64le)       arch=ppc64le ;;
    *)             err "unsupported architecture: $arch" ;;
esac

if [ -z "$VERSION" ]; then
    VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
        | sed -n 's/^[[:space:]]*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' \
        | head -n1)"
    [ -n "$VERSION" ] || err "could not resolve latest release tag from GitHub"
fi

archive="kubectl-kedge_${os}_${arch}.tar.gz"
url="${BASE_URL}/${VERSION}/${archive}"

tmp="$(mktemp -d 2>/dev/null || mktemp -d -t kedge-install)"
trap 'rm -rf "$tmp"' EXIT INT TERM

printf 'Downloading kedge %s for %s/%s...\n' "$VERSION" "$os" "$arch"
if ! curl -fsSL -o "${tmp}/${archive}" "$url"; then
    # Fallback: GitHub release asset.
    url="https://github.com/${REPO}/releases/download/${VERSION}/${archive}"
    printf 'Retrying via GitHub releases...\n'
    curl -fsSL -o "${tmp}/${archive}" "$url" \
        || err "failed to download ${archive} (${VERSION})"
fi

tar -xz -C "$tmp" -f "${tmp}/${archive}" kubectl-kedge \
    || err "failed to extract ${archive}"

target="${INSTALL_DIR}/kedge"
if ! mkdir -p "$INSTALL_DIR" 2>/dev/null; then
    err "cannot create ${INSTALL_DIR} — pick a writable INSTALL_DIR or rerun with sudo"
fi
if [ ! -w "$INSTALL_DIR" ]; then
    err "${INSTALL_DIR} is not writable — pick a writable INSTALL_DIR (e.g. \$HOME/.local/bin) or rerun with sudo"
fi

mv "${tmp}/kubectl-kedge" "$target"
chmod +x "$target"

cat <<EOF

Installed kedge ${VERSION} → ${target}

EOF

case ":${PATH}:" in
    *":${INSTALL_DIR}:"*)
        ;;
    *)
        cat <<EOF
Note: ${INSTALL_DIR} is not on your \$PATH. Add it with:

    echo 'export PATH="${INSTALL_DIR}:\$PATH"' >> ~/.profile
    export PATH="${INSTALL_DIR}:\$PATH"

EOF
        ;;
esac

cat <<EOF
Next:
    kedge login           # sign in (defaults to console.faros.sh)
    kedge edge create     # connect your first edge

EOF
