#!/usr/bin/env bash
# The server as a snap, running as a background service, for this machine's
# architecture, from the release archive (same binary as the .deb):
#
#   scripts/build-snap.sh 5.1.1 [dist-dir]   → <dist-dir>/nulldrive_<v>_<amd64|arm64>.snap
#
# Run scripts/build-release.sh first. Needs snapcraft on Ubuntu 24.04 (the
# snap's base, core24): it builds in --destructive-mode, on this machine,
# which is safe here because it only copies files.
#
# What the snap does:
#   nulldrive.server   the service, started on install, data in /var/snap/nulldrive/common
#   nulldrive          the admin commands (sudo nulldrive reset-password <user>, …)
#   sudo snap set nulldrive port=8080 mdns=on|off trust-proxy=on|off search-origins=…
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:?usage: build-snap.sh <version> [dist-dir]}"
VERSION="${VERSION#v}"
OUT="$(cd "${2:-dist}" && pwd)"
case "$(uname -m)" in
  x86_64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac
TGZ="$OUT/nulldrive_${VERSION}_linux_${ARCH}.tar.gz"
[[ -f "$TGZ" ]] || { echo "missing $TGZ: run scripts/build-release.sh v$VERSION first" >&2; exit 1; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/root/bin" "$WORK/snap/hooks" "$WORK/snap/gui"
tar -xzf "$TGZ" -C "$WORK" "nulldrive_${VERSION}_linux_${ARCH}/nulldrive" "nulldrive_${VERSION}_linux_${ARCH}/LICENSE"
install -m755 "$WORK/nulldrive_${VERSION}_linux_${ARCH}/nulldrive" "$WORK/root/bin/nulldrive"
install -m644 "$WORK/nulldrive_${VERSION}_linux_${ARCH}/LICENSE" "$WORK/root/LICENSE"
install -m644 docs/brand/nulldrive-server-512.png "$WORK/snap/gui/icon.png"

# Settings come from `snap set` (snapctl), with the server's own defaults.
cat >"$WORK/root/bin/env.sh" <<'EOF'
#!/bin/sh
# Shared by the service and the admin commands: data in $SNAP_COMMON (kept
# across refreshes and reverts), settings from `snap set nulldrive …`.
export NULLDRIVE_DATA_DIR="$SNAP_COMMON"
v=$(snapctl get port 2>/dev/null) && [ -n "$v" ] && export NULLDRIVE_PORT="$v"
v=$(snapctl get mdns 2>/dev/null) && [ "$v" = off ] && export NULLDRIVE_MDNS=off
v=$(snapctl get trust-proxy 2>/dev/null) && [ "$v" = on ] && export NULLDRIVE_TRUST_PROXY=on
v=$(snapctl get search-origins 2>/dev/null) && [ -n "$v" ] && export NULLDRIVE_SEARCH_ORIGINS="$v"
v=$(snapctl get log-level 2>/dev/null) && [ -n "$v" ] && export NULLDRIVE_LOG_LEVEL="$v"
exec "$SNAP/bin/nulldrive" "$@"
EOF
chmod 755 "$WORK/root/bin/env.sh"

cat >"$WORK/snap/hooks/configure" <<'EOF'
#!/bin/sh
# Checks `snap set nulldrive …` and restarts the service to apply it.
set -e
fail() { echo "$*" >&2; exit 1; }
port=$(snapctl get port)
case "$port" in
  "") ;;
  *[!0-9]*) fail "port must be a number: $port" ;;
  *) [ "$port" -ge 1 ] && [ "$port" -le 65535 ] || fail "port must be 1-65535: $port" ;;
esac
for k in mdns trust-proxy; do
  v=$(snapctl get "$k")
  case "$v" in "" | on | off) ;; *) fail "$k must be on or off: $v" ;; esac
done
v=$(snapctl get log-level)
case "$v" in "" | debug | info | warn | error) ;; *) fail "log-level must be debug, info, warn or error: $v" ;; esac
if snapctl services nulldrive.server | grep -q ' active'; then
  snapctl restart nulldrive.server
fi
EOF
cat >"$WORK/snap/hooks/install" <<'EOF'
#!/bin/sh
# Defaults, so `snap get nulldrive` shows what can be set.
snapctl set port=8080 mdns=on trust-proxy=off log-level=info
EOF
chmod 755 "$WORK/snap/hooks/configure" "$WORK/snap/hooks/install"

cat >"$WORK/snap/snapcraft.yaml" <<EOF
name: nulldrive
base: core24
version: '$VERSION'
title: Null Drive
summary: Self-hosted file sync server with a built-in web app
description: |
  Null Drive keeps a folder on each of your computers in sync through a
  server you run yourself. One program serves the web app, the API and the
  database: chunked, deduplicated storage with version history, optional
  end-to-end encryption, per-user search for Null Search, and backup and
  two-way sync for Null Notes.

  After installing, open http://<this machine>:8080 and create the admin
  account. Settings:

    sudo snap set nulldrive port=8080          # HTTP port
    sudo snap set nulldrive mdns=off           # don't announce on the LAN
    sudo snap set nulldrive trust-proxy=on     # behind a reverse proxy
    sudo snap get nulldrive                    # current settings

  Data is in /var/snap/nulldrive/common. Admin commands: sudo nulldrive help.
  The desktop apps for Windows and Linux are on the GitHub releases page.
license: Proprietary
grade: stable
confinement: strict
website: https://nullgrid.dev
source-code: https://github.com/Null0Grid/null-drive
issues: https://github.com/Null0Grid/null-drive/issues
icon: snap/gui/icon.png

apps:
  server:
    command: bin/env.sh
    daemon: simple
    restart-condition: on-failure
    stop-timeout: 35s
    plugs: [network, network-bind]
  nulldrive:
    command: bin/env.sh
    plugs: [network]

parts:
  nulldrive:
    plugin: dump
    source: root
EOF

if ! (cd "$WORK" && snapcraft pack --destructive-mode --verbosity=verbose --output "$OUT/nulldrive_${VERSION}_${ARCH}.snap"); then
  # snapcraft keeps the details in its own log.
  log=$(find "$HOME/.local/state/snapcraft/log" -name '*.log' -printf '%T@ %p\n' 2>/dev/null | sort -n | tail -1 | cut -d' ' -f2-)
  [[ -n "$log" ]] && { echo "--- $log" >&2; tail -n 60 "$log" >&2; }
  exit 1
fi
ls -la "$OUT/nulldrive_${VERSION}_${ARCH}.snap"
