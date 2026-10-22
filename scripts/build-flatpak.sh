#!/usr/bin/env bash
# Builds the Flatpak of the desktop app for this machine's architecture,
# from the .tar.gz that scripts/build-linux-desktop.sh made:
#
#   scripts/build-flatpak.sh 5.1.1 [dist-dir]   → <dist-dir>/NullDrive-<v>-<x86_64|aarch64>.flatpak
#
# Install the result with: flatpak install ./NullDrive-<v>-x86_64.flatpak
# (Flatpak fetches the GNOME runtime from Flathub if it isn't there yet).
#
# Needs flatpak and flatpak-builder, and network access to Flathub for the
# GNOME runtime and SDK. FLATPAK_SCOPE=system builds into the system
# installation (CI does, so the same runtime serves the install test).
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:?usage: build-flatpak.sh <version> [dist-dir]}"
VERSION="${VERSION#v}"
OUT="$(cd "${2:-dist}" && pwd)"
SCOPE="--${FLATPAK_SCOPE:-user}"
APP_ID=dev.nullgrid.NullDrive
case "$(uname -m)" in
  x86_64) ARCH=amd64 FP_ARCH=x86_64 ;;
  aarch64 | arm64) ARCH=arm64 FP_ARCH=aarch64 ;;
  *) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac
TGZ="$OUT/nulldrive-desktop_${VERSION}_linux_${ARCH}.tar.gz"
[[ -f "$TGZ" ]] || { echo "missing $TGZ: run scripts/build-linux-desktop.sh $VERSION first" >&2; exit 1; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

flatpak remote-add "$SCOPE" --if-not-exists flathub https://dl.flathub.org/repo/flathub.flatpakrepo
python3 scripts/flatpak-manifest.py "$VERSION" --local "$TGZ" >"$WORK/$APP_ID.yml"
cat "$WORK/$APP_ID.yml"

# --disable-rofiles-fuse: no FUSE in containers and CI runners.
flatpak-builder "$SCOPE" --install-deps-from=flathub --disable-rofiles-fuse --force-clean \
  --default-branch=stable --repo="$WORK/repo" "$WORK/build" "$WORK/$APP_ID.yml"
flatpak build-bundle --runtime-repo=https://dl.flathub.org/repo/flathub.flatpakrepo \
  "$WORK/repo" "$OUT/NullDrive-${VERSION}-${FP_ARCH}.flatpak" "$APP_ID" stable
ls -la "$OUT/NullDrive-${VERSION}-${FP_ARCH}.flatpak"
