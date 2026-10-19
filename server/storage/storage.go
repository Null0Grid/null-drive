// Package storage stores immutable, content-addressed blobs keyed by their
// SHA-256. Since Phase 6 every blob is a chunk (see shared/chunker); files
// stored before that are single, whole-file chunks.
//
// The metadata DB maps (user, path) -> sha256; this package only knows
// hashes. Two consequences:
//   - Identical content is stored once, whoever uploads it and under
//     whatever name (the basis for Phase 6 deduplication).
//   - Renames and moves never touch file data, only metadata.
package storage

import (
	"context"
	"errors"
	"io"
	"regexp"
	"time"
)

var (
	ErrNotFound     = errors.New("blob not found")
	ErrHashMismatch = errors.New("content does not match expected sha256")
	ErrInvalidHash  = errors.New("invalid sha256 hex string")
)

// Blob describes stored content.
type Blob struct {
	SHA256 string
	Size   int64
}

// Store is the blob storage abstraction. The disk implementation is the
// only one today; the interface keeps an S3-style backend possible later
// without touching the API layer.
type Store interface {
	// Put streams r into the store and returns its hash and size. If
	// expectedSHA256 is non-empty and the content hashes differently,
	// nothing is stored and ErrHashMismatch is returned.
	Put(ctx context.Context, r io.Reader, expectedSHA256 string) (Blob, error)
	// Open returns a seekable reader for the blob and its size.
	Open(sha256 string) (io.ReadSeekCloser, int64, error)
	Exists(sha256 string) (bool, error)
	// Delete removes a blob. Callers must ensure nothing references it
	// (the garbage collector in chunkstore does, under a per-hash lock).
	Delete(sha256 string) error
	// Walk calls fn for every stored blob (for the orphan sweep).
	Walk(fn func(sha256 string, size int64, modTime time.Time) error) error
}

var hashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidHash reports whether s is a lowercase hex SHA-256. Hashes become
// file system paths, so they are validated before any disk access.
func ValidHash(s string) bool { return hashRE.MatchString(s) }
