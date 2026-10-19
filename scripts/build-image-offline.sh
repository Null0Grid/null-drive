#!/usr/bin/env bash
# Container images without pulling anything: for machines that can't reach
# Docker Hub or gcr.io (air-gapped homelabs, locked-down build boxes).
#
#   scripts/build-image-offline.sh 5.0.0 [dist-dir]
#   → <dist-dir>/nulldrive_5.0.0_docker_{amd64,arm64}.tar     (docker load -i …)
#
# Same runtime as the main Dockerfile's final stage (distroless/static:
# nonroot user 65532, /data volume, CA certificates, tz database, the
# binary's own healthcheck), assembled FROM scratch from this machine's CA
# bundle and zoneinfo plus the release binaries. The image built by CI
# from ./Dockerfile is still the published one; use this when you can't.
# Needs docker (any driver: there are no RUN steps, so no emulation for arm64).
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:?usage: build-image-offline.sh <version> [dist-dir]}"
VERSION="${VERSION#v}"
OUT="${2:-dist}"
IMAGE="${IMAGE:-nulldrive}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

CA=/etc/ssl/certs/ca-certificates.crt
[[ -f "$CA" ]] || { echo "no CA bundle at $CA" >&2; exit 1; }
[[ -d /usr/share/zoneinfo ]] || { echo "no /usr/share/zoneinfo" >&2; exit 1; }

for ARCH in amd64 arm64; do
  TGZ="$OUT/nulldrive_${VERSION}_linux_${ARCH}.tar.gz"
  [[ -f "$TGZ" ]] || { echo "missing $TGZ: run scripts/build-release.sh v$VERSION first" >&2; exit 1; }
  C="$WORK/$ARCH"
  mkdir -p "$C/rootfs/etc/ssl/certs" "$C/rootfs/usr/share" "$C/rootfs/tmp" "$C/data"
  tar -xzf "$TGZ" -C "$C" "nulldrive_${VERSION}_linux_${ARCH}/nulldrive"
  install -m 0755 "$C/nulldrive_${VERSION}_linux_${ARCH}/nulldrive" "$C/rootfs/nulldrive"
  cp "$CA" "$C/rootfs/etc/ssl/certs/ca-certificates.crt"
  cp -rL /usr/share/zoneinfo "$C/rootfs/usr/share/zoneinfo"
  printf 'root:x:0:0:root:/root:/sbin/nologin\nnonroot:x:65532:65532:nonroot:/home/nonroot:/sbin/nologin\n' >"$C/rootfs/etc/passwd"
  printf 'root:x:0:\nnonroot:x:65532:\n' >"$C/rootfs/etc/group"
  chmod 1777 "$C/rootfs/tmp"
  cat >"$C/Dockerfile" <<EOF
FROM scratch
COPY rootfs/ /
COPY --chown=65532:65532 data/ /data/
LABEL org.opencontainers.image.title="Null Drive" \\
      org.opencontainers.image.description="Self-hosted file sync server with a built-in web UI" \\
      org.opencontainers.image.licenses="MIT" \\
      org.opencontainers.image.version="${VERSION}"
ENV NULLDRIVE_DATA_DIR=/data NULLDRIVE_PORT=8080 SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
USER 65532:65532
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 CMD ["/nulldrive", "healthcheck"]
ENTRYPOINT ["/nulldrive"]
EOF
  DOCKER_BUILDKIT=1 docker build -q --platform "linux/$ARCH" -t "$IMAGE:$VERSION" -t "$IMAGE:latest" "$C" >/dev/null
  docker save "$IMAGE:$VERSION" "$IMAGE:latest" -o "$OUT/nulldrive_${VERSION}_docker_${ARCH}.tar"
  docker image rm "$IMAGE:$VERSION" "$IMAGE:latest" >/dev/null
  echo "$OUT/nulldrive_${VERSION}_docker_${ARCH}.tar"
done
