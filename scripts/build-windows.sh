#!/usr/bin/env bash
# Builds the Windows desktop app from any OS (CI runs it on Linux):
#
#   scripts/build-windows.sh [version]        # default: git describe, else 0.0.0-dev
#
# Output in dist/:
#   NullDrive-<v>-Setup.exe             per-user installer (NSIS)
#   NullDrive_<v>_windows_amd64.zip     portable: NullDrive.exe + LICENSE
#   winget/                             winget manifests for this version
#
# Needs: Go, Node 22 (npm), makensis (apt install nsis; choco install nsis),
# go-winres (installed on demand). RELEASE_TAG: the GitHub release tag the
# winget manifests point at (default v<version>; CI passes the real tag,
# which may have no "v"). Optional signing: set WINDOWS_CERT_PFX
# (path to a .pfx code-signing certificate) and WINDOWS_CERT_PASSWORD, and
# have osslsigncode installed; the exe and the installer are then signed.
#
# Why cross-compile instead of `wails build` on a Windows runner: Wails'
# Windows target is pure Go (WebView2 is loaded at runtime, no cgo), so a
# plain `go build` with Wails' build tags produces the same program. One
# script then builds identical files on a laptop, in CI and on Linux, and
# the installer is built in the same step. See docs/DESIGN.md.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$PWD

VERSION="${1:-$(git describe --tags --match 'v*' 2>/dev/null || echo v0.0.0-dev)}"
VERSION="${VERSION#v}"
# Windows version resources need four numeric parts: 0.7.0-rc.1 -> 0.7.0.0
NUM="${VERSION%%-*}"
IFS=. read -r MA MI PA <<<"$NUM"
VERSION4="${MA:-0}.${MI:-0}.${PA:-0}.0"
ARCH=amd64
OUT="$ROOT/dist"
WORK="$ROOT/client/build/bin"
mkdir -p "$OUT" "$WORK"

step() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }

step "Frontend"
(cd client/frontend && { [[ -d node_modules ]] || npm ci --no-audit --no-fund; } && npm run build)

step "Windows resources (icon, manifest, version $VERSION4)"
WINRES="$(command -v go-winres || true)"
if [[ -z "$WINRES" ]]; then
  GOBIN="$WORK/tools" go install github.com/tc-hib/go-winres@v0.3.3
  WINRES="$WORK/tools/go-winres"
fi
sed -e "s/{{VERSION4}}/$VERSION4/g" -e "s/{{VERSION}}/$VERSION/g" \
  client/build/windows/winres.json >"$WORK/winres.json"
cp client/build/windows/icon.ico "$WORK/icon.ico"
"$WINRES" make --in "$WORK/winres.json" --out "$ROOT/client/rsrc" --arch "$ARCH"

step "NullDrive.exe (windows/$ARCH)"
(cd client && GOOS=windows GOARCH=$ARCH CGO_ENABLED=0 go build \
  -tags desktop,production -trimpath \
  -ldflags "-s -w -H windowsgui -X main.version=v$VERSION" \
  -o "$WORK/NullDrive.exe" .)
rm -f client/rsrc_windows_*.syso

sign() {
  [[ -n "${WINDOWS_CERT_PFX:-}" ]] || return 0
  osslsigncode sign -pkcs12 "$WINDOWS_CERT_PFX" -pass "${WINDOWS_CERT_PASSWORD:-}" \
    -n "Null Drive" -i "https://github.com/${GITHUB_REPOSITORY:-Null0Grid/null-drive}" \
    -h sha256 -ts http://timestamp.digicert.com -in "$1" -out "$1.signed"
  mv "$1.signed" "$1"
  echo "signed $(basename "$1")"
}
sign "$WORK/NullDrive.exe"

step "Portable zip"
ZIP="NullDrive_${VERSION}_windows_${ARCH}.zip"
STAGE="$WORK/NullDrive_${VERSION}_windows_${ARCH}"
rm -rf "$STAGE" && mkdir -p "$STAGE"
cp "$WORK/NullDrive.exe" "$STAGE/"
cp LICENSE "$STAGE/LICENSE.txt"
(cd "$WORK" && rm -f "$OUT/$ZIP" && python3 -c '
import sys, zipfile, os
z = zipfile.ZipFile(sys.argv[1], "w", zipfile.ZIP_DEFLATED)
for root, _, files in os.walk(sys.argv[2]):
    for f in sorted(files):
        p = os.path.join(root, f)
        z.write(p, os.path.relpath(p, os.path.dirname(sys.argv[2])))
' "$OUT/$ZIP" "$(basename "$STAGE")")

step "Installer"
SETUP="NullDrive-${VERSION}-Setup.exe"
makensis -V2 -DVERSION="$VERSION" -DVERSION4="$VERSION4" \
  -DEXE="$WORK/NullDrive.exe" -DLICENSE="$ROOT/LICENSE" -DOUTFILE="$OUT/$SETUP" \
  client/build/windows/installer/nulldrive.nsi
sign "$OUT/$SETUP"

step "winget manifests"
SHA=$(sha256sum "$OUT/$SETUP" | cut -d' ' -f1 | tr a-f A-F)
REPO="${GITHUB_REPOSITORY:-Null0Grid/null-drive}"
python3 scripts/winget-manifest.py --version "$VERSION" --sha256 "$SHA" \
  --url "https://github.com/$REPO/releases/download/${RELEASE_TAG:-v$VERSION}/$SETUP" \
  --repo "$REPO" --out "$OUT/winget"

step "Done"
(cd "$OUT" && ls -la "$SETUP" "$ZIP" && sha256sum "$SETUP" "$ZIP")
