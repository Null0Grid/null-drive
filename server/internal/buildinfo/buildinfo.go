// Package buildinfo exposes version metadata injected at build time:
//
//	go build -ldflags "-X github.com/nulldrive/nulldrive/server/internal/buildinfo.Version=v0.1.0 \
//	                   -X github.com/nulldrive/nulldrive/server/internal/buildinfo.Commit=abc123"
package buildinfo

var (
	Version = "dev"
	Commit  = "none"
)
