#!/usr/bin/env bash
# Debian/Ubuntu packages of the server, from the release archives:
#
#   scripts/build-deb.sh 5.0.0 [dist-dir]      → <dist-dir>/nulldrive_5.0.0_{amd64,arm64}.deb
#
# Run scripts/build-release.sh first; this repackages its linux tarballs
# (same binaries, so the .deb and the tarball are byte-identical inside).
#
# What the package does:
#   /usr/bin/nulldrive                    the server (and its admin commands)
#   /lib/systemd/system/nulldrive.service runs as the "nulldrive" system user,
#                                         hardened like docs/INSTALL.md's unit
#   /etc/default/nulldrive                settings (conffile: kept on upgrade)
#   /var/lib/nulldrive                    data (never deleted, not even on purge)
# On install it creates the user and starts the service on :8080.
# Needs dpkg-deb only.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:?usage: build-deb.sh <version> [dist-dir]}"
VERSION="${VERSION#v}"
OUT="${2:-dist}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

for ARCH in amd64 arm64; do
  TGZ="$OUT/nulldrive_${VERSION}_linux_${ARCH}.tar.gz"
  [[ -f "$TGZ" ]] || { echo "missing $TGZ: run scripts/build-release.sh v$VERSION first" >&2; exit 1; }
  R="$WORK/$ARCH"
  mkdir -p "$R/DEBIAN" "$R/usr/bin" "$R/lib/systemd/system" "$R/etc/default" \
    "$R/usr/share/doc/nulldrive"
  tar -xzf "$TGZ" -C "$WORK" "nulldrive_${VERSION}_linux_${ARCH}/nulldrive" "nulldrive_${VERSION}_linux_${ARCH}/LICENSE"
  install -m 0755 "$WORK/nulldrive_${VERSION}_linux_${ARCH}/nulldrive" "$R/usr/bin/nulldrive"
  install -m 0644 "$WORK/nulldrive_${VERSION}_linux_${ARCH}/LICENSE" "$R/usr/share/doc/nulldrive/copyright"

  cat >"$R/etc/default/nulldrive" <<'EOF'
# Null Drive settings (docs/INSTALL.md › Configuration). Restart after a change:
#   sudo systemctl restart nulldrive
NULLDRIVE_DATA_DIR=/var/lib/nulldrive
NULLDRIVE_PORT=8080
NULLDRIVE_LOG_LEVEL=info
# NULLDRIVE_TRUST_PROXY=on          behind a reverse proxy (Caddy, nginx, Cloudflare Tunnel)
# NULLDRIVE_SEARCH_ORIGINS=https://search.nullgrid.dev
# NULLDRIVE_MDNS=off                don't announce the server on the LAN
EOF

  cat >"$R/lib/systemd/system/nulldrive.service" <<'EOF'
[Unit]
Description=Null Drive
Documentation=https://github.com/Null0Grid/null-drive
After=network-online.target
Wants=network-online.target

[Service]
User=nulldrive
Group=nulldrive
EnvironmentFile=-/etc/default/nulldrive
ExecStart=/usr/bin/nulldrive
Restart=on-failure
TimeoutStopSec=35
# Hardening
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=/var/lib/nulldrive
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
CapabilityBoundingSet=

[Install]
WantedBy=multi-user.target
EOF

  cat >"$R/DEBIAN/control" <<EOF
Package: nulldrive
Version: ${VERSION}
Architecture: ${ARCH}
Maintainer: Null Drive <noreply@nulldrive.invalid>
Section: net
Priority: optional
Depends: adduser
Homepage: https://github.com/Null0Grid/null-drive
Description: Self-hosted file sync server with a built-in web UI
 Null Drive keeps files in sync between your computers through a server you
 run yourself: chunked, deduplicated storage with version history, a web
 app, optional end-to-end encryption, per-user search for Null Search, and
 backup and two-way sync for Null Notes. One static binary, SQLite inside.
EOF
  echo "/etc/default/nulldrive" >"$R/DEBIAN/conffiles"

  cat >"$R/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e
if [ "$1" = configure ]; then
  if ! getent passwd nulldrive >/dev/null; then
    adduser --system --group --home /var/lib/nulldrive --no-create-home \
      --gecos "Null Drive" --shell /usr/sbin/nologin nulldrive >/dev/null
  fi
  install -d -m 0750 -o nulldrive -g nulldrive /var/lib/nulldrive
  if [ -d /run/systemd/system ]; then
    systemctl daemon-reload >/dev/null || true
    if [ -z "$2" ]; then
      systemctl enable --now nulldrive.service >/dev/null || true
      echo "Null Drive is running: open http://$(hostname -I 2>/dev/null | cut -d' ' -f1 || echo localhost):8080 to create the admin account."
    else
      systemctl try-restart nulldrive.service >/dev/null || true
    fi
  fi
fi
exit 0
EOF
  cat >"$R/DEBIAN/prerm" <<'EOF'
#!/bin/sh
set -e
if [ -d /run/systemd/system ] && [ "$1" = remove ]; then
  systemctl disable --now nulldrive.service >/dev/null || true
fi
exit 0
EOF
  cat >"$R/DEBIAN/postrm" <<'EOF'
#!/bin/sh
set -e
if [ -d /run/systemd/system ]; then systemctl daemon-reload >/dev/null || true; fi
if [ "$1" = purge ]; then
  echo "Your files and settings are still in /var/lib/nulldrive (not deleted). Remove them yourself if you're sure."
fi
exit 0
EOF
  chmod 0755 "$R/DEBIAN/postinst" "$R/DEBIAN/prerm" "$R/DEBIAN/postrm"
  echo "Installed-Size: $(du -sk --exclude=DEBIAN "$R" | cut -f1)" >>"$R/DEBIAN/control"

  dpkg-deb --root-owner-group -Zxz --build "$R" "$OUT/nulldrive_${VERSION}_${ARCH}.deb" >/dev/null
  echo "$OUT/nulldrive_${VERSION}_${ARCH}.deb"
done
