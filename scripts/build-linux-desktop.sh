#!/usr/bin/env bash
# Builds the Null Drive desktop app for Linux, for this machine's
# architecture (CI runs it on an x86_64 and an arm64 runner):
#
#   scripts/build-linux-desktop.sh 5.0.0 [dist-dir]
#
# Output (amd64 shown; arm64 alike):
#   NullDrive-<v>-x86_64.AppImage                 any distro, nothing to install
#   nulldrive-desktop-<v>-1.x86_64.rpm            Fedora, openSUSE, RHEL 10
#   nulldrive-desktop_<v>_amd64.deb               Debian, Ubuntu, Mint, Pop!_OS
#   nulldrive-desktop_<v>_linux_amd64.tar.gz      plain archive (+ install.sh)
#
# The app is Wails (Go + the system's WebKitGTK), so every format needs
# WebKitGTK 4.1 and GTK 3 on the machine: the packages depend on them; the
# AppImage and the archive expect them (preinstalled on GNOME, KDE, Cinnamon
# and most desktops; otherwise libwebkit2gtk-4.1-0 / webkit2gtk4.1).
#
# Needs: Go (see go.mod), Node 22, gcc, pkg-config, libgtk-3-dev,
# libwebkit2gtk-4.1-dev. nfpm (.deb/.rpm) and appimagetool are downloaded
# into a temporary folder if they aren't on PATH.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:?usage: build-linux-desktop.sh <version> [dist-dir]}"
VERSION="${VERSION#v}"
OUT="$(mkdir -p "${2:-dist}" && cd "${2:-dist}" && pwd)"
NFPM_VERSION=2.46.3
APPIMAGETOOL_VERSION=1.9.1

case "$(uname -m)" in
  x86_64) ARCH=amd64 AI_ARCH=x86_64 RPM_ARCH=x86_64 ;;
  aarch64 | arm64) ARCH=arm64 AI_ARCH=aarch64 RPM_ARCH=aarch64 ;;
  *) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
step() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }

pkg-config --exists webkit2gtk-4.1 gtk+-3.0 ||
  { echo "needs libgtk-3-dev and libwebkit2gtk-4.1-dev (apt) / gtk3-devel webkit2gtk4.1-devel (dnf)" >&2; exit 1; }

step "Frontend"
(cd client/frontend && { [[ -d node_modules ]] || npm ci --no-audit --no-fund; } && npm run build)

step "nulldrive-desktop (linux/$ARCH)"
BIN="$WORK/nulldrive-desktop"
(cd client && CGO_ENABLED=1 go build -tags desktop,production,webkit2_41 -trimpath \
  -ldflags "-s -w -X main.version=v$VERSION" -o "$BIN" .)

# One staged tree: the layout of an installed system, used by every format.
STAGE="$WORK/root"
install -Dm755 "$BIN" "$STAGE/usr/bin/nulldrive-desktop"
install -Dm644 client/build/linux/nulldrive-desktop.desktop "$STAGE/usr/share/applications/nulldrive-desktop.desktop"
for s in 32 48 64 128 256 512; do
  install -Dm644 "client/build/linux/icons/$s.png" "$STAGE/usr/share/icons/hicolor/${s}x${s}/apps/nulldrive-desktop.png"
done
install -Dm644 LICENSE "$STAGE/usr/share/doc/nulldrive-desktop/copyright"

tool() { # tool NAME URL: on PATH, or downloaded once into $WORK/tools
  if command -v "$1" >/dev/null; then command -v "$1"; return; fi
  mkdir -p "$WORK/tools"
  local f="$WORK/tools/$1"
  if [[ "$2" == *.tar.gz ]]; then
    curl -fsSL "$2" | tar -xz -C "$WORK/tools" "$1"
  else
    curl -fsSL -o "$f" "$2" && chmod +x "$f"
  fi
  echo "$f"
}

step ".deb and .rpm (nfpm)"
case "$ARCH" in amd64) NFPM_ARCH=x86_64 ;; arm64) NFPM_ARCH=arm64 ;; esac
NFPM="$(tool nfpm "https://github.com/goreleaser/nfpm/releases/download/v${NFPM_VERSION}/nfpm_${NFPM_VERSION}_Linux_${NFPM_ARCH}.tar.gz")"
cat >"$WORK/postinst.sh" <<'EOF'
#!/bin/sh
# Menu entry, icon cache and the nulldrive:// handler (MimeType in the .desktop file).
command -v update-desktop-database >/dev/null && update-desktop-database -q /usr/share/applications || true
command -v gtk-update-icon-cache >/dev/null && gtk-update-icon-cache -q -t /usr/share/icons/hicolor || true
exit 0
EOF
cat >"$WORK/nfpm.yaml" <<EOF
name: nulldrive-desktop
arch: $ARCH
platform: linux
version: $VERSION
release: 1
section: net
priority: optional
maintainer: NULLGRID <noreply@nullgrid.dev>
vendor: NULLGRID
homepage: https://nullgrid.dev
license: Proprietary
description: |-
  Null Drive desktop app: keeps a folder in sync with your own Null Drive server.
  Part of Null Grid. Needs a Null Drive server (see https://nullgrid.dev).
contents:
$(cd "$STAGE" && find usr -type f | sort | while read -r f; do
  # Files only: system folders such as /usr/share/icons/hicolor belong to
  # other packages, and an rpm that also claims them conflicts on install.
  printf '  - src: %s\n    dst: /%s\n' "$STAGE/$f" "$f"
done)
scripts:
  postinstall: $WORK/postinst.sh
  postremove: $WORK/postinst.sh
overrides:
  deb:
    depends:
      - libgtk-3-0t64 | libgtk-3-0
      - libwebkit2gtk-4.1-0
      - xdg-utils
  rpm:
    # By library, not package name: Fedora calls it webkit2gtk4.1,
    # openSUSE libwebkit2gtk-4_1-0.
    depends:
      - libwebkit2gtk-4.1.so.0()(64bit)
      - libgtk-3.so.0()(64bit)
      - xdg-utils
EOF
"$NFPM" package --config "$WORK/nfpm.yaml" --packager deb --target "$OUT/nulldrive-desktop_${VERSION}_${ARCH}.deb"
"$NFPM" package --config "$WORK/nfpm.yaml" --packager rpm --target "$OUT/nulldrive-desktop-${VERSION}-1.${RPM_ARCH}.rpm"

step "Plain archive"
TB="nulldrive-desktop_${VERSION}_linux_${ARCH}"
mkdir -p "$WORK/$TB"
cp "$BIN" "$WORK/$TB/nulldrive-desktop"
cp client/build/linux/nulldrive-desktop.desktop "$WORK/$TB/"
cp client/build/linux/icons/512.png "$WORK/$TB/nulldrive-desktop.png"
cp LICENSE "$WORK/$TB/LICENSE"
# AppStream metadata for Flatpak/Flathub (packaging/flatpak), with this
# version's release entry (unless it's already listed).
META=client/build/linux/dev.nullgrid.NullDrive.metainfo.xml
if grep -q "<release version=\"$VERSION\"" "$META"; then
  grep -v '@CURRENT_RELEASE@' "$META" >"$WORK/$TB/dev.nullgrid.NullDrive.metainfo.xml"
else
  REL="    <release version=\"$VERSION\" date=\"$(date -u +%F)\"/>"
  sed "s|^ *<!-- @CURRENT_RELEASE@ -->|$REL|" "$META" >"$WORK/$TB/dev.nullgrid.NullDrive.metainfo.xml"
fi
cat >"$WORK/$TB/install.sh" <<'EOF'
#!/bin/sh
# Installs Null Drive for this user only (no root): ~/.local/bin, the app
# menu entry, the icon and the nulldrive:// link handler.
# Uninstall: ./install.sh --uninstall
set -e
cd "$(dirname "$0")"
BIN="$HOME/.local/bin"; DATA="${XDG_DATA_HOME:-$HOME/.local/share}"
if [ "$1" = "--uninstall" ]; then
  rm -f "$BIN/nulldrive-desktop" "$DATA/applications/nulldrive-desktop.desktop" \
        "$DATA/icons/hicolor/512x512/apps/nulldrive-desktop.png"
  update-desktop-database -q "$DATA/applications" 2>/dev/null || true
  echo "Removed. Your synced folder and settings are untouched."; exit 0
fi
mkdir -p "$BIN" "$DATA/applications" "$DATA/icons/hicolor/512x512/apps"
install -m755 nulldrive-desktop "$BIN/"
install -m644 nulldrive-desktop.png "$DATA/icons/hicolor/512x512/apps/"
sed "s|^Exec=nulldrive-desktop|Exec=\"$BIN/nulldrive-desktop\"|" nulldrive-desktop.desktop > "$DATA/applications/nulldrive-desktop.desktop"
update-desktop-database -q "$DATA/applications" 2>/dev/null || true
xdg-mime default nulldrive-desktop.desktop x-scheme-handler/nulldrive 2>/dev/null || true
echo "Installed: find Null Drive in your app menu, or run $BIN/nulldrive-desktop"
EOF
cat >"$WORK/$TB/README.txt" <<EOF
Null Drive $VERSION, desktop app for Linux ($ARCH)

  ./nulldrive-desktop     run it from here, or
  ./install.sh            install for this user (app menu, icon, nulldrive:// links)

Needs WebKitGTK 4.1 and GTK 3 (preinstalled on most desktops; otherwise
"sudo apt install libwebkit2gtk-4.1-0" or "sudo dnf install webkit2gtk4.1").
The tray icon needs a StatusNotifier tray (KDE, Cinnamon, XFCE, MATE; on GNOME
the "AppIndicator and KStatusNotifierItem Support" extension). Without one,
start Null Drive again from the app menu to show its window.

You need a Null Drive server: https://nullgrid.dev
EOF
chmod +x "$WORK/$TB/install.sh" "$WORK/$TB/nulldrive-desktop"
tar -C "$WORK" -czf "$OUT/$TB.tar.gz" "$TB"

step "AppImage"
APPTOOL="$(tool appimagetool "https://github.com/AppImage/appimagetool/releases/download/${APPIMAGETOOL_VERSION}/appimagetool-${AI_ARCH}.AppImage")"
APPDIR="$WORK/NullDrive.AppDir"
mkdir -p "$APPDIR"
cp -a "$STAGE/usr" "$APPDIR/"
cp client/build/linux/nulldrive-desktop.desktop "$APPDIR/"
cp client/build/linux/icons/256.png "$APPDIR/nulldrive-desktop.png"
cat >"$APPDIR/AppRun" <<'EOF'
#!/bin/sh
HERE="$(dirname "$(readlink -f "$0")")"
exec "$HERE/usr/bin/nulldrive-desktop" "$@"
EOF
chmod +x "$APPDIR/AppRun"
# appimagetool is itself an AppImage: extract-and-run works without FUSE (CI, containers).
APPIMAGE_EXTRACT_AND_RUN=1 ARCH="$AI_ARCH" "$APPTOOL" --no-appstream "$APPDIR" "$OUT/NullDrive-${VERSION}-${AI_ARCH}.AppImage"

step "Done"
(cd "$OUT" && ls -la NullDrive-"$VERSION"-*.AppImage nulldrive-desktop*"$VERSION"*)
