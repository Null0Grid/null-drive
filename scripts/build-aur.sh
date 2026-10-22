#!/usr/bin/env bash
# AUR packages (PKGBUILD + .SRCINFO) for a release, from the .tar.gz files
# in dist-dir:
#
#   scripts/build-aur.sh 5.1.1 [dist-dir] [--test]
#     → <dist-dir>/aur/nulldrive-bin/, <dist-dir>/aur/nulldrive-desktop-bin/
#     → <dist-dir>/nulldrive_<v>_aur.tar.gz (both folders, for the release)
#
# .SRCINFO is written by makepkg itself. Without makepkg on this machine, it
# runs in an archlinux container (Docker). --test also builds both packages
# with makepkg there, using the local archives in place of the downloads
# (same checksums), installs them with pacman, and checks: the server runs
# as the nulldrive user from its sysusers/tmpfiles setup and answers
# /api/v1/health; the desktop app has every library it links against.
#
# Publishing on the AUR is a git push per package (docs/INSTALL.md, Arch).
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:?usage: build-aur.sh <version> [dist-dir] [--test]}"
VERSION="${VERSION#v}"
OUT="$(cd "${2:-dist}" && pwd)"
TEST="${3:-}"
IMAGE="${ARCH_IMAGE:-archlinux:base-devel}"

rm -rf "$OUT/aur"
python3 scripts/aur.py "$VERSION" "$OUT"

# Runs inside Arch, as root; makepkg itself runs as an unprivileged user.
cat >"$OUT/aur/.inside.sh" <<'EOF'
#!/bin/bash
set -euo pipefail
cd /aur
id builder >/dev/null 2>&1 || useradd -m builder
# Hand the folder back to the caller's user, whatever happens.
trap 'chown -R "$HOST_UID:$HOST_GID" /aur' EXIT
chown -R builder /aur
for p in nulldrive-bin nulldrive-desktop-bin; do
  (cd "$p" && runuser -u builder -- makepkg --printsrcinfo > .SRCINFO)
done
[[ "${TEST:-}" == --test ]] || exit 0

pacman -Syu --noconfirm --needed namcap >/dev/null
for p in nulldrive-bin nulldrive-desktop-bin; do
  (cd "$p"
   # The release archives are already here under the names makepkg would
   # download them to, so it uses them after checking their sha256.
   runuser -u builder -- makepkg -f --nodeps --noconfirm
   namcap PKGBUILD || true
   pacman -U --noconfirm ./*.pkg.tar.zst)
done

fail() { echo "::error::AUR test: $*"; exit 1; }
nulldrive version | grep -q "v${VERSION}" || fail "nulldrive version is not v$VERSION"
getent passwd nulldrive >/dev/null || fail "sysusers did not create the nulldrive user"
[[ "$(stat -c %U /var/lib/nulldrive)" == nulldrive ]] || fail "/var/lib/nulldrive is not owned by nulldrive"
# The unit's command line, as its user, with its environment file.
set -a; . /etc/conf.d/nulldrive; set +a
NULLDRIVE_MDNS=off runuser -u nulldrive -- /usr/bin/nulldrive >/tmp/server.log 2>&1 &
for _ in $(seq 50); do curl -fsS localhost:8080/api/v1/health >/dev/null 2>&1 && break; sleep 0.2; done
curl -fsS localhost:8080/api/v1/health | grep -q '"status":"ok"' || { cat /tmp/server.log; fail "server did not start"; }
[[ -f /var/lib/nulldrive/nulldrive.db ]] || fail "no database in /var/lib/nulldrive"
echo "ok   nulldrive-bin: installed, user + data dir created, server healthy"

missing=$(ldd /usr/bin/nulldrive-desktop | grep 'not found' || true)
[[ -z "$missing" ]] || fail "nulldrive-desktop is missing libraries: $missing"
desktop-file-validate /usr/share/applications/nulldrive-desktop.desktop 2>/dev/null || true
echo "ok   nulldrive-desktop-bin: installed, every library found"
EOF
chmod +x "$OUT/aur/.inside.sh"

# Archives under the names the PKGBUILDs download them to (only for --test).
if [[ "$TEST" == --test ]]; then
  for f in "$OUT"/nulldrive_"$VERSION"_linux_*.tar.gz "$OUT"/nulldrive-desktop_"$VERSION"_linux_*.tar.gz; do
    [[ -f "$f" ]] || continue
    case "$f" in
      *_linux_amd64.tar.gz) arch=x86_64 ;;
      *_linux_arm64.tar.gz) arch=aarch64 ;;
    esac
    case "$(basename "$f")" in
      nulldrive-desktop_*) cp "$f" "$OUT/aur/nulldrive-desktop-bin/nulldrive-desktop-bin-${VERSION//-/_}-$arch.tar.gz" ;;
      *) cp "$f" "$OUT/aur/nulldrive-bin/nulldrive-bin-${VERSION//-/_}-$arch.tar.gz" ;;
    esac
  done
fi

if command -v makepkg >/dev/null && [[ "$TEST" != --test ]]; then
  for p in nulldrive-bin nulldrive-desktop-bin; do (cd "$OUT/aur/$p" && makepkg --printsrcinfo > .SRCINFO); done
else
  docker run --rm -e TEST="$TEST" -e VERSION="$VERSION" -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" -v "$OUT/aur:/aur" "$IMAGE" /aur/.inside.sh
fi

# What goes on the AUR: the PKGBUILD folders, without test leftovers.
rm -f "$OUT/aur/.inside.sh"
for p in nulldrive-bin nulldrive-desktop-bin; do
  (cd "$OUT/aur/$p" && rm -rf src pkg ./*.pkg.tar.zst ./*-bin-*.tar.gz)
done
tar -C "$OUT/aur" --owner=0 --group=0 -czf "$OUT/nulldrive_${VERSION}_aur.tar.gz" nulldrive-bin nulldrive-desktop-bin
tar -tzf "$OUT/nulldrive_${VERSION}_aur.tar.gz"
