#!/bin/sh
# railgrid CLI installer.
#
# Usage:
#   curl -fsSL https://downloads.railgrid.ai/install.sh | sh
#
# Environment variables:
#   RAILGRID_VERSION    Install a specific version (default: latest GitHub release).
#   INSTALL_DIR      Target directory (default: $HOME/.local/bin — no sudo
#                    required; with HOME unset it must be given explicitly).
#                    To install system-wide instead:
#                      curl -fsSL https://downloads.railgrid.ai/install.sh \
#                        | INSTALL_DIR=/usr/local/bin sudo -E sh
#                    An agent registered with "agent join" runs the binary path
#                    recorded in its systemd unit, so upgrade that path.
#   RAILGRID_BASE_URL   Override the binary download base (default:
#                    https://downloads.railgrid.ai/cli/railgrid).
#   RAILGRID_HARNESS    Which coding harnesses a machine you register offers:
#                    auto (default — every harness installed on it), none, or a
#                    comma-separated list of claude,codex. It is passed through
#                    to the "edge create" and "agent join" commands printed
#                    below; "edge create --harness" writes spec.harness on the
#                    edge, which is the opt-out that sticks.

set -eu

REPO="railgrid/railgrid"
VERSION="${RAILGRID_VERSION:-}"
BASE_URL="${RAILGRID_BASE_URL:-https://downloads.railgrid.ai/cli/railgrid}"
HARNESS="${RAILGRID_HARNESS:-auto}"

err() { printf 'error: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || err "missing required tool: $1"; }

# `command -v` reports the PATH spelling, which can name the same file as
# ${target} while differing textually -- a trailing slash in INSTALL_DIR, or a
# symlinked bin directory. Compare identity so a clean install is never
# reported as shadowed. -ef is outside POSIX but implemented by dash, bash, ash
# and zsh; where it is missing the textual compare is the best available.
same_file() {
    [ "$1" = "$2" ] && return 0
    [ -e "$1" ] && [ -e "$2" ] && [ "$1" -ef "$2" ] 2>/dev/null
}

# $HOME is not always set. `sudo sh`, `docker exec`, systemd units, cron and ssh
# sessions without a login shell can all run without it, and under `set -u`
# dereferencing it aborted this script with "HOME: parameter not set" -- which
# names no fix and happens before anything is downloaded, so the install looks
# like a no-op. Resolve the default only when it can be resolved.
if [ -z "${INSTALL_DIR:-}" ]; then
    [ -n "${HOME:-}" ] || err 'HOME is not set, so the default install directory ($HOME/.local/bin) cannot be resolved -- pass one explicitly, e.g. INSTALL_DIR=/usr/local/bin'
    INSTALL_DIR="${HOME}/.local/bin"
fi
# Trailing slashes only make the printed paths ugly ("/usr/local/bin//railgrid").
while [ "$INSTALL_DIR" != "/" ] && [ "${INSTALL_DIR%/}" != "$INSTALL_DIR" ]; do
    INSTALL_DIR="${INSTALL_DIR%/}"
done

need curl
need tar
need uname

# Release archives are named after `uname` (see .goreleaser.yml): title-case
# OS and the machine name as uname reports it — kubectl-railgrid_Linux_x86_64,
# kubectl-railgrid_Linux_aarch64, kubectl-railgrid_Darwin_arm64, ….
os="$(uname -s)"
case "$os" in
    Linux)  os=Linux ;;
    Darwin) os=Darwin ;;
    *)      err "unsupported OS: $os (Linux, Darwin only)" ;;
esac

arch="$(uname -m)"
case "$arch" in
    x86_64|amd64)  arch=x86_64 ;;
    aarch64|arm64)
        if [ "$os" = "Linux" ]; then arch=aarch64; else arch=arm64; fi ;;
    ppc64le)       arch=ppc64le ;;
    *)             err "unsupported architecture: $arch" ;;
esac

if [ -z "$VERSION" ]; then
    VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
        | sed -n 's/^[[:space:]]*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' \
        | head -n1)"
    [ -n "$VERSION" ] || err "could not resolve latest release tag from GitHub"
fi

archive="kubectl-railgrid_${os}_${arch}.tar.gz"
url="${BASE_URL}/${VERSION}/${archive}"

tmp="$(mktemp -d 2>/dev/null || mktemp -d -t railgrid-install)"
trap 'rm -rf "$tmp"' EXIT INT TERM

printf 'Downloading railgrid %s for %s/%s...\n' "$VERSION" "$os" "$arch"
if ! curl -fsSL -o "${tmp}/${archive}" "$url"; then
    # Fallback: GitHub release asset.
    url="https://github.com/${REPO}/releases/download/${VERSION}/${archive}"
    printf 'Retrying via GitHub releases...\n'
    curl -fsSL -o "${tmp}/${archive}" "$url" \
        || err "failed to download ${archive} (${VERSION})"
fi

tar -xz -C "$tmp" -f "${tmp}/${archive}" kubectl-railgrid \
    || err "failed to extract ${archive}"

target="${INSTALL_DIR}/railgrid"
if ! mkdir -p "$INSTALL_DIR" 2>/dev/null; then
    err "cannot create ${INSTALL_DIR} — pick a writable INSTALL_DIR or rerun with sudo"
fi
if [ ! -w "$INSTALL_DIR" ]; then
    err "${INSTALL_DIR} is not writable — pick a writable INSTALL_DIR (e.g. \$HOME/.local/bin) or rerun with sudo"
fi

mv "${tmp}/kubectl-railgrid" "$target"
chmod +x "$target"

cat <<EOF

Installed railgrid ${VERSION} → ${target}

EOF

# A railgrid earlier on PATH shadows the one just installed, so `railgrid
# version` keeps reporting the old build and the install looks ineffective. The
# default INSTALL_DIR is $HOME/.local/bin, which is often not on PATH at all
# while an older binary sits in /usr/local/bin.
shadow="$(command -v railgrid 2>/dev/null || true)"
if [ -n "$shadow" ] && ! same_file "$shadow" "$target"; then
    cat <<EOF
Note: another railgrid comes earlier on your \$PATH and will shadow this
      install -- \`railgrid\` still runs that one:

          ${shadow}

      To upgrade that copy instead:

    curl -fsSL https://downloads.railgrid.ai/install.sh \\
      | INSTALL_DIR="${shadow%/*}" sh

EOF
fi

# Installing a binary restarts nothing. An agent registered with
# `railgrid agent join` keeps running its current build until its unit is
# restarted, and the unit records the symlink-resolved path of whichever binary
# registered it (os.Executable + EvalSymlinks), which can be neither ${target}
# nor ${shadow}. So name the units and have the operator read the path off the
# unit rather than assume one.
if command -v systemctl >/dev/null 2>&1; then
    units="$(systemctl list-units --all --no-legend --plain --type=service 'railgrid-agent-*' 2>/dev/null \
        | awk '{print $1}' | tr '\n' ' ')"
    units="${units% }"
    if [ -n "$units" ]; then
        cat <<EOF
Note: an agent on this host keeps running the binary recorded in its unit until
      that unit is restarted, and that path is resolved through symlinks when
      the unit is written -- so read it off the unit rather than assuming:

    systemctl show -p ExecStart --value ${units%% *}

      Upgrade that path (INSTALL_DIR above), then restart:

    systemctl restart ${units}

EOF
    fi
fi

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
    railgrid login --hub-url https://<your-hub>   # sign in (browser OIDC, or --token <token>)
    railgrid use                                  # pick an organization and workspace
    railgrid edge create <name> --harness ${HARNESS}
                                                  # register your first edge and print its join command
    railgrid --help                               # everything else

A server or macOS edge registered with --harness auto (the default) offers every
coding harness it has installed — Claude Code, Codex — to its workspace, and
picks up one installed later. Register with --harness none, or flip spec.harness
in the edge UI at any time, to switch that off. The agent is seeded the same way:

    sudo railgrid agent join --harness ${HARNESS} ...

EOF
