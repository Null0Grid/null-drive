# syntax=docker/dockerfile:1.7
#
# Null Drive container image.
#
#   docker buildx build --platform linux/amd64,linux/arm64 -t ghcr.io/null0grid/null-drive .
#
# Three stages:
#   ui     Node builds the Svelte UI (architecture-independent, built once)
#   build  Go cross-compiles a static binary for each target platform
#   final  distroless/static: just the binary, CA certs and tzdata
#
# The ui and build stages run on the *build* machine's native architecture
# ($BUILDPLATFORM) and Go cross-compiles via GOOS/GOARCH. Because the server
# is pure Go (CGO_ENABLED=0, modernc.org/sqlite), a multi-arch build needs
# no QEMU emulation and is roughly as fast as a single-arch one.

ARG NODE_VERSION=22
ARG GO_VERSION=1.26

# ---------- 1. UI ----------
FROM --platform=$BUILDPLATFORM node:${NODE_VERSION}-alpine AS ui
WORKDIR /src/ui
COPY ui/package.json ui/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY ui/ ./
# vite.config.ts writes the bundle to ../server/internal/webui/dist
RUN npm run build

# ---------- 2. Go binary ----------
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY server/ server/
COPY shared/ shared/
COPY --from=ui /src/server/internal/webui/dist/ server/internal/webui/dist/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
      -ldflags "-s -w \
        -X github.com/Null0Grid/null-drive/server/internal/buildinfo.Version=${VERSION} \
        -X github.com/Null0Grid/null-drive/server/internal/buildinfo.Commit=${COMMIT}" \
      -o /out/nulldrive ./server/cmd/nulldrive \
 && mkdir -p /out/data

# ---------- 3. Runtime ----------
# distroless/static: no shell, no package manager, no libc. Only what a
# static Go binary needs. ":nonroot" sets user 65532.
FROM gcr.io/distroless/static-debian12:nonroot

ARG VERSION=dev
ARG COMMIT=none
LABEL org.opencontainers.image.title="Null Drive" \
      org.opencontainers.image.description="Self-hosted file sync server with a built-in web UI" \
      org.opencontainers.image.licenses="LicenseRef-NULLGRID-proprietary" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"

COPY --from=build /out/nulldrive /nulldrive
# Pre-create /data owned by the nonroot user. Docker copies this ownership
# into a *new named volume* on first use, so `-v nulldrive-data:/data`
# works with no chown. Bind mounts keep host ownership: see docs/INSTALL.md.
COPY --from=build --chown=65532:65532 /out/data /data

ENV NULLDRIVE_DATA_DIR=/data \
    NULLDRIVE_PORT=8080
USER 65532:65532
EXPOSE 8080
VOLUME ["/data"]

# The image has no curl/wget, so the binary checks itself.
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD ["/nulldrive", "healthcheck"]

ENTRYPOINT ["/nulldrive"]
