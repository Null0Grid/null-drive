package meta

import (
	"context"
	"errors"
	"testing"
	"time"
)

// otherUser inserts a second account directly (user management is Phase 7).
func otherUser(t *testing.T, s *Store) int64 {
	t.Helper()
	var id int64
	err := s.db.QueryRow(`INSERT INTO users (username, password_hash, created_at) VALUES ('bob', 'x', ?) RETURNING id`,
		fmtTime(s.now())).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func refcount(t *testing.T, s *Store, hash string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT refcount FROM chunks WHERE hash = ?`, hash).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

const (
	hA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

// TestChunkOwnershipIsPerUser: storage is shared, but the check and the
// commit only accept chunks this user uploaded or already owns, so dedup
// can't be used to learn or steal another user's data.
func TestChunkOwnershipIsPerUser(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	alice := newUser(t, s).ID
	bob := otherUser(t, s)
	if err := s.TouchChunk(ctx, alice, hA, 10); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CommitFile(ctx, alice, Commit{Path: "secret.txt", SHA256: hC, Size: 10, Chunks: []ChunkRef{{hA, 10}}}, Precondition{}); err != nil {
		t.Fatal(err)
	}
	// Bob asks about Alice's chunk: "missing", the same answer as for a
	// chunk nobody has.
	missing, err := s.MissingChunks(ctx, bob, []string{hA, hB})
	if err != nil || len(missing) != 2 {
		t.Fatalf("bob sees %v (%v)", missing, err)
	}
	// Bob can't reference it without sending the bytes...
	_, _, err = s.CommitFile(ctx, bob, Commit{Path: "steal.txt", SHA256: hC, Size: 10, Chunks: []ChunkRef{{hA, 10}}}, Precondition{})
	var mce *MissingChunksError
	if !errors.As(err, &mce) || len(mce.Hashes) != 1 {
		t.Fatalf("bob committed alice's chunk: %v", err)
	}
	if _, ok, _ := s.CanReadChunk(ctx, bob, hA); ok {
		t.Fatal("bob can read alice's chunk")
	}
	// ...but once he uploads the same bytes (verified by hash on upload),
	// it is stored once and both reference it.
	if err := s.TouchChunk(ctx, bob, hA, 10); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CommitFile(ctx, bob, Commit{Path: "mine.txt", SHA256: hC, Size: 10, Chunks: []ChunkRef{{hA, 10}}}, Precondition{}); err != nil {
		t.Fatal(err)
	}
	if n := refcount(t, s, hA); n != 2 {
		t.Fatalf("refcount %d, want 2", n)
	}
	var rows int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&rows)
	if rows != 1 {
		t.Fatalf("%d chunk rows, want 1", rows)
	}
	// Committing consumed the pending-upload record.
	var pending int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM chunk_uploads`).Scan(&pending)
	if pending != 0 {
		t.Fatalf("%d pending uploads left", pending)
	}
}

func TestRefcountsFollowVersions(t *testing.T) {
	s, clk := newStore(t)
	ctx := context.Background()
	u := newUser(t, s).ID
	for _, h := range []string{hA, hB} {
		_ = s.TouchChunk(ctx, u, h, 5)
	}
	f1, _, err := s.CommitFile(ctx, u, Commit{Path: "f", SHA256: hC, Size: 10, Chunks: []ChunkRef{{hA, 5}, {hA, 5}}}, Precondition{})
	if err != nil {
		t.Fatal(err)
	}
	if refcount(t, s, hA) != 2 {
		t.Fatal("a chunk used twice in one manifest counts twice")
	}
	clk.Advance(time.Hour)
	if _, _, err := s.CommitFile(ctx, u, Commit{Path: "f", SHA256: hB, Size: 10, Chunks: []ChunkRef{{hA, 5}, {hB, 5}}},
		Precondition{Set: true, Version: f1.Version}); err != nil {
		t.Fatal(err)
	}
	if refcount(t, s, hA) != 3 || refcount(t, s, hB) != 1 {
		t.Fatalf("refcounts A=%d B=%d", refcount(t, s, hA), refcount(t, s, hB))
	}
	// Retention 0/0 drops the old version (the current one is protected).
	if err := s.SetRetention(ctx, Retention{TrashDays: 1}); err != nil {
		t.Fatal(err)
	}
	n, err := s.PruneVersions(ctx, Retention{TrashDays: 1})
	if err != nil || n != 1 {
		t.Fatalf("pruned %d (%v)", n, err)
	}
	if refcount(t, s, hA) != 1 || refcount(t, s, hB) != 1 {
		t.Fatalf("after prune A=%d B=%d", refcount(t, s, hA), refcount(t, s, hB))
	}
	// Nothing is collectable: both chunks are still referenced.
	if c, _ := s.CollectableChunks(ctx, 0, 10); len(c) != 0 {
		t.Fatalf("collectable %v", c)
	}
	// Size mismatch in a manifest is refused.
	_, _, err = s.CommitFile(ctx, u, Commit{Path: "g", SHA256: hC, Size: 6, Chunks: []ChunkRef{{hA, 6}}}, Precondition{})
	if !errors.Is(err, ErrManifestMismatch) {
		t.Fatalf("size mismatch: %v", err)
	}
}

func TestPrunablePolicy(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	at := func(d time.Duration) time.Time { return now.Add(-d) }
	// Newest first, as PruneVersions supplies them.
	vs := []pruneRow{
		{id: 1, created: at(1 * time.Hour)},        // recent
		{id: 2, created: at(2 * day)},              // recent
		{id: 3, created: at(40*day + time.Hour)},   // daily window: newest of its day -> keep
		{id: 4, created: at(40*day + 2*time.Hour)}, // same day, older -> drop
		{id: 5, created: at(41 * day)},             // other day -> keep
		{id: 6, created: at(200 * day)},            // beyond window -> drop
		{id: 7, created: at(300 * day)},            // beyond window but protected -> keep
	}
	got := prunable(vs, map[int64]bool{7: true}, Retention{FullDays: 30, DailyDays: 90}, now)
	if len(got) != 2 || got[0] != 4 || got[1] != 6 {
		t.Fatalf("dropped %v, want [4 6]", got)
	}
}

func TestTrashKeepsDeletedFilesForTrashDays(t *testing.T) {
	s, clk := newStore(t)
	ctx := context.Background()
	u := newUser(t, s).ID
	_ = s.TouchChunk(ctx, u, hA, 5)
	_ = s.TouchChunk(ctx, u, hB, 5)
	f1, _, _ := s.CommitFile(ctx, u, Commit{Path: "gone.txt", SHA256: hA, Size: 5, Chunks: []ChunkRef{{hA, 5}}}, Precondition{})
	f2, _, err := s.CommitFile(ctx, u, Commit{Path: "gone.txt", SHA256: hB, Size: 5, Chunks: []ChunkRef{{hB, 5}}}, Precondition{Set: true, Version: f1.Version})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.DeleteFile(ctx, u, "gone.txt", Precondition{}); err != nil {
		t.Fatal(err)
	}
	deletedAt := clk.Now()
	// History is kept 2 days, the trash 30: the trash wins for deleted files.
	pol := Retention{FullDays: 1, DailyDays: 1, TrashDays: 30}
	if err := s.SetRetention(ctx, pol); err != nil {
		t.Fatal(err)
	}
	clk.Advance(29 * 24 * time.Hour)
	_, _ = s.PruneVersions(ctx, pol)
	items, _ := s.Trash(ctx, u)
	if len(items) != 1 || items[0].Version != f2.Version {
		t.Fatalf("day 29: trash %+v", items)
	}
	if want := deletedAt.Add(30 * 24 * time.Hour); !items[0].PurgeAt.Equal(want) {
		t.Fatalf("purge_at %v, want %v", items[0].PurgeAt, want)
	}
	if _, err := s.Manifest(ctx, mustVersion(t, s, u, "gone.txt", f2.Version).ID); err != nil {
		t.Fatalf("day 29: last version not restorable: %v", err)
	}
	clk.Advance(2 * 24 * time.Hour) // day 31
	if items, _ := s.Trash(ctx, u); len(items) != 0 {
		t.Fatalf("day 31: still listed %+v", items)
	}
	_, _ = s.PruneVersions(ctx, pol)
	if vs, _ := s.ListVersions(ctx, u, "gone.txt"); len(vs) != 0 {
		t.Fatalf("day 31: history not purged: %d versions", len(vs))
	}
	// The tombstone stays, so sync clients still see the delete.
	if f, err := s.GetFile(ctx, u, "gone.txt"); err != nil || f.Live() || f.VersionID != 0 {
		t.Fatalf("tombstone %+v %v", f, err)
	}
	if c, _ := s.CollectableChunks(ctx, 0, 10); len(c) != 2 {
		t.Fatalf("chunks not collectable after purge: %v", c)
	}
	// A live file's history is untouched by the trash.
	_ = s.TouchChunk(ctx, u, hA, 5)
	if _, _, err := s.CommitFile(ctx, u, Commit{Path: "kept.txt", SHA256: hA, Size: 5, Chunks: []ChunkRef{{hA, 5}}}, Precondition{}); err != nil {
		t.Fatal(err)
	}
	clk.Advance(400 * 24 * time.Hour)
	_, _ = s.PruneVersions(ctx, pol)
	if vs, _ := s.ListVersions(ctx, u, "kept.txt"); len(vs) != 1 {
		t.Fatal("live file's current version pruned")
	}
}

func mustVersion(t *testing.T, s *Store, u int64, p string, v int64) Version {
	t.Helper()
	ver, err := s.GetVersion(context.Background(), u, p, v)
	if err != nil {
		t.Fatal(err)
	}
	return ver
}

func TestRetentionSettings(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	r, err := s.Retention(ctx)
	if err != nil || r != (Retention{FullDays: 30, DailyDays: 90, TrashDays: 30}) {
		t.Fatalf("defaults %+v %v", r, err)
	}
	if err := s.SetRetention(ctx, Retention{FullDays: 7, DailyDays: 0, TrashDays: 0}); err == nil {
		t.Fatal("trash days 0 accepted")
	}
	if err := s.SetRetention(ctx, Retention{FullDays: 7, DailyDays: 0, TrashDays: 14}); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Retention(ctx); r.TrashDays != 14 || r.FullDays != 7 {
		t.Fatalf("%+v", r)
	}
}
