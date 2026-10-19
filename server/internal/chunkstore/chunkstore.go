// Package chunkstore joins blob storage (bytes on disk) and the chunk
// table (metadata and reference counts) into one consistent chunk store.
//
// The two can't share a transaction, so ordering and a per-chunk lock keep
// them consistent:
//   - write path: lock(hash) → write the blob if absent → upsert the row
//     (refreshing its grace period) → unlock.
//   - GC path:    lock(hash) → delete the row if still unreferenced and
//     past its grace period → only then delete the blob → unlock.
//
// So a chunk being uploaded can't be collected half-way, and a row never
// points at a deleted blob. Commits (CommitFile) need no lock: they run in
// a DB transaction that requires every chunk row to exist, and once
// committed the refcount protects the chunk.
package chunkstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/Null0Grid/null-drive/server/internal/meta"
	"github.com/Null0Grid/null-drive/server/storage"
	"github.com/Null0Grid/null-drive/shared/chunker"
)

// Store is the chunk store.
type Store struct {
	Blobs  storage.Store
	Meta   *meta.Store
	Params chunker.Params
	Log    *slog.Logger

	locks [64]sync.Mutex
}

// New returns a store with the default chunking parameters.
func New(blobs storage.Store, m *meta.Store, log *slog.Logger) *Store {
	if log == nil {
		log = slog.Default()
	}
	return &Store{Blobs: blobs, Meta: m, Params: chunker.Default, Log: log}
}

func (s *Store) lock(hash string) func() {
	h := fnv.New32a()
	_, _ = h.Write([]byte(hash))
	m := &s.locks[h.Sum32()%uint32(len(s.locks))]
	m.Lock()
	return m.Unlock
}

// ErrSizeMismatch: the uploaded chunk is not the declared size.
var ErrSizeMismatch = errors.New("chunk size does not match")

// MaxChunkSize bounds a single chunk upload. Larger than the chunker's
// maximum so that migrated whole-file chunks are never in the way, but
// uploads through the v2 API are refused above it.
const MaxChunkSize = 16 << 20

// PutChunk stores one chunk uploaded by userID. The content is verified
// against hash (storage.ErrHashMismatch otherwise). If the chunk is
// already stored, by anyone, the verified bytes are discarded: data is
// stored once, but a user can only reference what they actually sent.
func (s *Store) PutChunk(ctx context.Context, userID int64, hash string, r io.Reader) (int64, error) {
	unlock := s.lock(hash)
	defer unlock()
	b, err := s.Blobs.Put(ctx, r, hash)
	if err != nil {
		return 0, err
	}
	return b.Size, s.Meta.TouchChunk(ctx, userID, hash, b.Size)
}

// Ingest chunks a whole stream on the server (v1 uploads, which send a
// file in one request) and stores every chunk, so web uploads get the same
// dedup and history as chunked clients.
func (s *Store) Ingest(ctx context.Context, userID int64, r io.Reader) (refs []meta.ChunkRef, whole string, size int64, err error) {
	cr, whole, size, err := chunker.Manifest(r, s.Params, func(c chunker.Chunk) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := s.PutChunk(ctx, userID, c.HexHash(), bytes.NewReader(c.Data))
		return err
	})
	if err != nil {
		return nil, "", 0, err
	}
	refs = make([]meta.ChunkRef, len(cr))
	for i, c := range cr {
		refs[i] = meta.ChunkRef{Hash: c.Hash, Size: c.Size}
	}
	return refs, whole, size, nil
}

// GCReport summarizes one collection run.
type GCReport struct {
	VersionsPruned int           `json:"versions_pruned"`
	UploadsExpired int64         `json:"uploads_expired"`
	ChunksDeleted  int           `json:"chunks_deleted"`
	BytesFreed     int64         `json:"bytes_freed"`
	OrphansDeleted int           `json:"orphans_deleted"`
	Duration       time.Duration `json:"duration_ns"`
}

// Collect runs retention and garbage collection:
//  1. prune versions per the retention policy (refcounts drop),
//  2. forget uncommitted uploads older than grace,
//  3. delete unreferenced chunks untouched for grace,
//  4. sweep blob files with no chunk row at all (left by a crash between
//     writing a blob and recording it) once they are older than grace.
//
// grace protects uploads in flight: a client checks, uploads chunks, then
// commits; any chunk touched within grace survives until it commits.
func (s *Store) Collect(ctx context.Context, grace time.Duration) (GCReport, error) {
	start := time.Now()
	var rep GCReport
	pol, err := s.Meta.Retention(ctx)
	if err != nil {
		return rep, err
	}
	if rep.VersionsPruned, err = s.Meta.PruneVersions(ctx, pol); err != nil {
		return rep, fmt.Errorf("prune versions: %w", err)
	}
	if rep.UploadsExpired, err = s.Meta.PruneChunkUploads(ctx, grace); err != nil {
		return rep, err
	}
	for {
		hashes, err := s.Meta.CollectableChunks(ctx, grace, 500)
		if err != nil {
			return rep, err
		}
		if len(hashes) == 0 {
			break
		}
		progress := false
		for _, h := range hashes {
			if err := ctx.Err(); err != nil {
				return rep, err
			}
			ok, size, err := s.deleteChunk(ctx, h, grace)
			if err != nil {
				return rep, err
			}
			if ok {
				progress = true
				rep.ChunksDeleted++
				rep.BytesFreed += size
			}
		}
		if !progress {
			break
		}
	}
	cutoff := time.Now().Add(-grace)
	err = s.Blobs.Walk(func(hash string, size int64, mod time.Time) error {
		if mod.After(cutoff) {
			return nil
		}
		unlock := s.lock(hash)
		defer unlock()
		known, err := s.Meta.KnownChunk(ctx, hash)
		if err != nil || known {
			return err
		}
		if err := s.Blobs.Delete(hash); err != nil && !errors.Is(err, storage.ErrNotFound) {
			return err
		}
		rep.OrphansDeleted++
		rep.BytesFreed += size
		return nil
	})
	rep.Duration = time.Since(start)
	return rep, err
}

func (s *Store) deleteChunk(ctx context.Context, hash string, grace time.Duration) (bool, int64, error) {
	unlock := s.lock(hash)
	defer unlock()
	ok, size, err := s.Meta.DeleteChunkIfUnused(ctx, hash, grace)
	if err != nil || !ok {
		return false, 0, err
	}
	if err := s.Blobs.Delete(hash); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return false, 0, err
	}
	return true, size, nil
}

// Purge deletes the given chunks immediately if they are unreferenced and
// nobody has a pending upload of them (end-to-end encryption's plaintext
// purge). Chunks still used by another account's files are kept.
func (s *Store) Purge(ctx context.Context, hashes []string) (deleted int, freed int64, err error) {
	for _, h := range hashes {
		if err := ctx.Err(); err != nil {
			return deleted, freed, err
		}
		ok, size, err := func() (bool, int64, error) {
			unlock := s.lock(h)
			defer unlock()
			ok, size, err := s.Meta.DeleteChunkIfOrphaned(ctx, h)
			if err != nil || !ok {
				return false, 0, err
			}
			if err := s.Blobs.Delete(h); err != nil && !errors.Is(err, storage.ErrNotFound) {
				return false, 0, err
			}
			return true, size, nil
		}()
		if err != nil {
			return deleted, freed, err
		}
		if ok {
			deleted++
			freed += size
		}
	}
	return deleted, freed, nil
}

// Run collects every interval until ctx ends.
func (s *Store) Run(ctx context.Context, interval, grace time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		rep, err := s.Collect(ctx, grace)
		if err != nil {
			if ctx.Err() == nil {
				s.Log.Error("garbage collection failed", "err", err)
			}
			continue
		}
		s.Log.Info("garbage collection", "versions_pruned", rep.VersionsPruned, "chunks_deleted", rep.ChunksDeleted,
			"orphans_deleted", rep.OrphansDeleted, "bytes_freed", rep.BytesFreed, "duration_ms", rep.Duration.Milliseconds())
	}
}
