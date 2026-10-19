#!/usr/bin/env bash
# Cross-compile release archives for every supported OS/arch, plus a
# SHA256SUMS.txt. Used by .github/workflows/release.yml; runs locally too.
#
#   scripts/build-release.sh v0.3.0 [commit]      → dist/
#
# Requires the UI to be built first (`make ui-build`): it gets embedded.
# The server is pure Go (CGO_ENABLED=0), so all targets build from one machine.
set -euo pipefail

VERSION="${1:?usage: build-release.sh <version> [commit]}"
COMMIT="${2:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}"
OUT="${OUT:-dist}"
PKG=github.com/Null0Grid/null-drive/server/internal/buildinfo
TARGETS=(
  linux/amd64 linux/arm64
  darwin/amd64 darwin/arm64
  windows/amd64 windows/arm64
)

if [[ ! -f server/internal/webui/dist/index.html ]]; then
  echo "UI bundle missing: run 'make ui-build' first" >&2
  exit 1
fi

rm -rf "$OUT"
mkdir -p "$OUT"

for target in "${TARGETS[@]}"; do
  os="${target%/*}"
  arch="${target#*/}"
  name="nulldrive_${VERSION#v}_${os}_${arch}"
  stage="$OUT/$name"
  bin="nulldrive"
  [[ "$os" == windows ]] && bin="nulldrive.exe"

  echo "→ $os/$arch"
  mkdir -p "$stage"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X $PKG.Version=$VERSION -X $PKG.Commit=$COMMIT" \
    -o "$stage/$bin" ./server/cmd/nulldrive
  cp LICENSE README.md "$stage/"

  if [[ "$os" == windows ]]; then
    (cd "$OUT" && zip -qr "$name.zip" "$name")
  else
    tar -C "$OUT" -czf "$OUT/$name.tar.gz" "$name"
  fi
  rm -rf "$stage"
done

(cd "$OUT" && sha256sum -- *.tar.gz *.zip > SHA256SUMS.txt)
echo
cat "$OUT/SHA256SUMS.txt"
