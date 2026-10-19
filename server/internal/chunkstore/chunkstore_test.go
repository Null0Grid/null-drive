package chunkstore

import (
	"bytes"
	"context"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nulldrive/nulldrive/server/internal/db"
	"github.com/nulldrive/nulldrive/server/internal/meta"
	"github.com/nulldrive/nulldrive/server/storage"
	"github.com/nulldrive/nulldrive/shared/chunker"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := db.Migrate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	blobs, err := storage.NewDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := New(blobs, meta.New(d, nil), nil)
	s.Params = chunker.Params{Min: 256, Avg: 1024, Max: 4096}
	return s, dir
}

func TestIngestAndSeekableReader(t *testing.T) {
	s, _ := newStore(t)
	data := make([]byte, 50_000)
	r := rand.New(rand.NewPCG(1, 2))
	for i := range data {
		data[i] = byte(r.Uint32())
	}
	refs, _, size, err := s.Ingest(context.Background(), 0, bytes.NewReader(data))
	if err != nil || size != int64(len(data)) || len(refs) < 10 {
		t.Fatalf("ingest: %d refs, %v", len(refs), err)
	}
	rd := s.Open(refs)
	defer rd.Close()
	all, err := io.ReadAll(rd)
	if err != nil || !bytes.Equal(all, data) {
		t.Fatalf("read all: %v", err)
	}
	// Random seeks + reads across chunk boundaries.
	buf := make([]byte, 3000)
	for i := 0; i < 200; i++ {
		off := r.IntN(len(data))
		if _, err := rd.Seek(int64(off), io.SeekStart); err != nil {
			t.Fatal(err)
		}
		n, err := io.ReadFull(rd, buf[:min(len(buf), len(data)-off)])
		if err != nil || !bytes.Equal(buf[:n], data[off:off+n]) {
			t.Fatalf("at %d: %v", off, err)
		}
	}
	if n, _ := rd.Seek(0, io.SeekEnd); n != int64(len(data)) {
		t.Fatal("SeekEnd")
	}
}

// TestOrphanSweep: a blob written without a chunk row (crash between the
// two steps) is removed once it is older than the grace period, and a
// referenced chunk never is.
func TestOrphanSweep(t *testing.T) {
	s, dir := newStore(t)
	ctx := context.Background()
	b, err := s.Blobs.Put(ctx, bytes.NewReader([]byte("orphan bytes")), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutChunk(ctx, 0, b2hash(t, s, "kept bytes"), bytes.NewReader([]byte("kept bytes"))); err != nil {
		t.Fatal(err)
	}
	rep, err := s.Collect(ctx, time.Hour)
	if err != nil || rep.OrphansDeleted != 0 {
		t.Fatalf("young orphan deleted: %+v %v", rep, err)
	}
	old := time.Now().Add(-2 * time.Hour)
	p := filepath.Join(dir, "blobs", b.SHA256[:2], b.SHA256[2:4], b.SHA256)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	rep, err = s.Collect(ctx, time.Hour)
	if err != nil || rep.OrphansDeleted != 1 {
		t.Fatalf("orphan not swept: %+v %v", rep, err)
	}
	if ok, _ := s.Blobs.Exists(b.SHA256); ok {
		t.Fatal("orphan file still there")
	}
}

func b2hash(t *testing.T, s *Store, text string) string {
	t.Helper()
	b, err := s.Blobs.Put(context.Background(), bytes.NewReader([]byte(text)), "")
	if err != nil {
		t.Fatal(err)
	}
	return b.SHA256
}
