package meta

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Null0Grid/null-drive/server/internal/db"
)

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newStore(t *testing.T) (*Store, *fakeClock) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := db.Migrate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	clk := &fakeClock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	return New(d, clk.Now), clk
}

func newUser(t *testing.T, s *Store) User {
	t.Helper()
	u, err := s.CompleteSetup(context.Background(), "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestSetupOnlyOnce(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	if done, _ := s.SetupComplete(ctx); done {
		t.Fatal("fresh db reports setup complete")
	}
	u := newUser(t, s)
	if !u.IsAdmin || u.Username != "admin" {
		t.Fatalf("admin = %+v", u)
	}
	if done, _ := s.SetupComplete(ctx); !done {
		t.Fatal("setup not marked complete")
	}
	if _, err := s.CompleteSetup(ctx, "intruder", "x"); !errors.Is(err, ErrSetupComplete) {
		t.Fatalf("second setup err = %v", err)
	}
}

func TestConcurrentSetupCreatesOneAdmin(t *testing.T) {
	s, _ := newStore(t)
	var wg sync.WaitGroup
	results := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.CompleteSetup(context.Background(), "admin"+string(rune('a'+i)), "h")
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	ok := 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrSetupComplete):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d setups succeeded, want exactly 1", ok)
	}
}

func TestSessions(t *testing.T) {
	s, clk := newStore(t)
	ctx := context.Background()
	u := newUser(t, s)
	if _, err := s.CreateSession(ctx, u.ID, "h1", time.Hour); err != nil {
		t.Fatal(err)
	}
	got, sid, err := s.SessionUser(ctx, "h1")
	if err != nil || got.ID != u.ID {
		t.Fatalf("SessionUser = %+v, %v", got, err)
	}
	if _, _, err := s.SessionUser(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token err = %v", err)
	}
	clk.Advance(2 * time.Hour)
	if _, _, err := s.SessionUser(ctx, "h1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session err = %v", err)
	}
	if err := s.DeleteSession(ctx, sid); err != nil {
		t.Fatal(err)
	}
}

func TestDevices(t *testing.T) {
	s, clk := newStore(t)
	ctx := context.Background()
	u := newUser(t, s)
	d, err := s.CreateDevice(ctx, u.ID, "laptop", "dh")
	if err != nil {
		t.Fatal(err)
	}
	if d.AddedVia != AddedViaPassword {
		t.Fatalf("added_via = %q", d.AddedVia)
	}
	if err := s.TouchDevice(ctx, d.ID, "192.168.1.5", "NullDrive/0.5.0 (windows/amd64)"); err != nil {
		t.Fatal(err)
	}
	got, owner, err := s.DeviceByToken(ctx, "dh")
	if err != nil || owner.ID != u.ID || got.LastSeenAt == nil || got.LastIP != "192.168.1.5" || got.ClientVersion == "" {
		t.Fatalf("DeviceByToken = %+v %+v %v", got, owner, err)
	}
	firstSeen := *got.LastSeenAt

	clk.Advance(30 * time.Second)
	_ = s.TouchDevice(ctx, d.ID, "192.168.1.5", "") // throttled: no write within a minute
	got, _, _ = s.DeviceByToken(ctx, "dh")
	if !got.LastSeenAt.Equal(firstSeen) || got.ClientVersion == "" {
		t.Fatalf("touch was not throttled / version lost: %+v", got)
	}
	_ = s.TouchDevice(ctx, d.ID, "10.0.0.9", "") // address changed: written at once
	got, _, _ = s.DeviceByToken(ctx, "dh")
	if got.LastIP != "10.0.0.9" {
		t.Fatalf("ip change not recorded: %+v", got)
	}
	firstSeen = *got.LastSeenAt
	clk.Advance(time.Minute + time.Second)
	_ = s.TouchDevice(ctx, d.ID, "10.0.0.9", "")
	got, _, _ = s.DeviceByToken(ctx, "dh")
	if got.LastSeenAt.Equal(firstSeen) {
		t.Fatal("touch after a minute did not update")
	}

	if _, err := s.RevokeDevice(ctx, u.ID+1, d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoking someone else's device err = %v", err)
	}
	rev, err := s.RevokeDevice(ctx, u.ID, d.ID)
	if err != nil || rev.RevokedAt == nil {
		t.Fatalf("revoke = %+v %v", rev, err)
	}
	if _, _, err := s.DeviceByToken(ctx, "dh"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked token still valid: %v", err)
	}
	list, _ := s.ListDevices(ctx, u.ID)
	if len(list) != 1 {
		t.Fatalf("devices = %d", len(list))
	}
}

// TestPutFilePreconditions is the core optimistic-concurrency table.
func TestPutFilePreconditions(t *testing.T) {
	type setup func(t *testing.T, s *Store, uid int64) int64 // returns a version of interest
	live := func(t *testing.T, s *Store, uid int64) int64 {
		f, _, err := s.PutFile(context.Background(), uid, "doc.txt", shaA, 1, Precondition{})
		if err != nil {
			t.Fatal(err)
		}
		return f.Version
	}
	deleted := func(t *testing.T, s *Store, uid int64) int64 {
		live(t, s, uid)
		v, _, err := s.DeleteFile(context.Background(), uid, "doc.txt", Precondition{})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	none := func(*testing.T, *Store, int64) int64 { return 0 }

	tests := []struct {
		name        string
		setup       setup
		sha         string
		pre         func(v int64) Precondition
		wantErr     error // sentinel, or nil
		wantConfl   bool
		wantCreated bool
		wantBump    bool
	}{
		{"create, no If-Match", none, shaA, func(int64) Precondition { return Precondition{} }, nil, false, true, true},
		{"create, If-Match 0", none, shaA, func(int64) Precondition { return Precondition{Set: true} }, nil, false, true, true},
		{"create, If-Match stale version", none, shaA, func(int64) Precondition { return Precondition{Set: true, Version: 7} }, nil, true, false, false},
		{"update, matching If-Match", live, shaB, func(v int64) Precondition { return Precondition{Set: true, Version: v} }, nil, false, false, true},
		{"update, stale If-Match", live, shaB, func(v int64) Precondition { return Precondition{Set: true, Version: v + 99} }, nil, true, false, false},
		{"update, If-Match 0 on live file", live, shaB, func(int64) Precondition { return Precondition{Set: true} }, nil, true, false, false},
		{"update, no If-Match", live, shaB, func(int64) Precondition { return Precondition{} }, ErrPreconditionRequired, false, false, false},
		{"same content is idempotent even if stale", live, shaA, func(v int64) Precondition { return Precondition{Set: true, Version: v + 99} }, nil, false, false, false},
		{"recreate deleted, no If-Match", deleted, shaB, func(int64) Precondition { return Precondition{} }, nil, false, true, true},
		{"recreate deleted, tombstone version", deleted, shaB, func(v int64) Precondition { return Precondition{Set: true, Version: v} }, nil, false, true, true},
		{"edit after remote delete", deleted, shaB, func(v int64) Precondition { return Precondition{Set: true, Version: v - 1} }, nil, true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newStore(t)
			ctx := context.Background()
			u := newUser(t, s)
			v := tt.setup(t, s, u.ID)
			before, _ := s.CurrentVersion(ctx)

			f, created, err := s.PutFile(ctx, u.ID, "doc.txt", tt.sha, 1, tt.pre(v))
			after, _ := s.CurrentVersion(ctx)

			var ce *ConflictError
			switch {
			case tt.wantConfl:
				if !errors.As(err, &ce) {
					t.Fatalf("err = %v, want ConflictError", err)
				}
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
			default:
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				if f.SHA256 != tt.sha || !f.Live() {
					t.Fatalf("file = %+v", f)
				}
				if created != tt.wantCreated {
					t.Fatalf("created = %v, want %v", created, tt.wantCreated)
				}
			}
			if bumped := after > before; bumped != tt.wantBump {
				t.Fatalf("version %d -> %d, wantBump %v", before, after, tt.wantBump)
			}
			if tt.wantBump && f.Version != after {
				t.Fatalf("file version %d != server version %d", f.Version, after)
			}
		})
	}
}

func TestVersionsAreMonotonicAndFeedIsComplete(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	u := newUser(t, s)

	_, start, _ := s.ListFiles(ctx, u.ID, nil)
	f1, _, _ := s.PutFile(ctx, u.ID, "a.txt", shaA, 1, Precondition{})
	f2, _, _ := s.PutFile(ctx, u.ID, "b.txt", shaB, 1, Precondition{})
	if f1.Version <= start || f2.Version <= f1.Version {
		t.Fatalf("versions not increasing: %d %d %d", start, f1.Version, f2.Version)
	}
	_, _, _ = s.DeleteFile(ctx, u.ID, "a.txt", Precondition{})

	// Plain listing: live files only.
	files, current, _ := s.ListFiles(ctx, u.ID, nil)
	if len(files) != 1 || files[0].Path != "b.txt" {
		t.Fatalf("live listing = %+v", files)
	}
	// Change feed from the start includes the tombstone.
	feed, _, _ := s.ListFiles(ctx, u.ID, &start)
	if len(feed) != 2 {
		t.Fatalf("feed = %+v", feed)
	}
	if feed[len(feed)-1].Path != "a.txt" || feed[len(feed)-1].Live() {
		t.Fatalf("last change should be a.txt tombstone: %+v", feed[len(feed)-1])
	}
	// Nothing after current.
	empty, _, _ := s.ListFiles(ctx, u.ID, &current)
	if len(empty) != 0 {
		t.Fatalf("feed after current = %+v", empty)
	}
}

func TestUsersAreIsolated(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	u := newUser(t, s)
	other, err := scanUser(s.db.QueryRowContext(ctx,
		`INSERT INTO users (username, password_hash, created_at) VALUES ('bob', 'h', 'x') RETURNING `+userCols))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = s.PutFile(ctx, u.ID, "secret.txt", shaA, 1, Precondition{})
	files, _, _ := s.ListFiles(ctx, other.ID, nil)
	if len(files) != 0 {
		t.Fatal("other user sees files")
	}
	if _, err := s.GetFile(ctx, other.ID, "secret.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user can read metadata: %v", err)
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		files     []string
		target    string
		pre       Precondition
		wantPaths []string
		wantErr   error
		wantConfl bool
	}{
		{"single file", []string{"a.txt", "b.txt"}, "a.txt", Precondition{}, []string{"a.txt"}, nil, false},
		{"folder", []string{"docs/a", "docs/sub/b", "docsx", "other"}, "docs", Precondition{}, []string{"docs", "docs/a", "docs/sub", "docs/sub/b"}, nil, false},
		{"missing", []string{"a.txt"}, "nope", Precondition{}, nil, ErrNotFound, false},
		{"stale If-Match", []string{"a.txt"}, "a.txt", Precondition{Set: true, Version: 999}, nil, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newStore(t)
			u := newUser(t, s)
			for _, p := range tt.files {
				if _, _, err := s.PutFile(ctx, u.ID, p, shaA, 1, Precondition{}); err != nil {
					t.Fatal(err)
				}
			}
			v, deleted, err := s.DeleteFile(ctx, u.ID, tt.target, tt.pre)
			var ce *ConflictError
			if tt.wantConfl {
				if !errors.As(err, &ce) {
					t.Fatalf("err = %v, want conflict", err)
				}
				return
			}
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(deleted) != len(tt.wantPaths) {
				t.Fatalf("deleted %d, want %d", len(deleted), len(tt.wantPaths))
			}
			for i, f := range deleted {
				if f.Path != tt.wantPaths[i] || f.Live() || f.Version != v {
					t.Fatalf("deleted[%d] = %+v (version %d)", i, f, v)
				}
			}
			// Deleting again is a 404, not a second tombstone.
			if _, _, err := s.DeleteFile(ctx, u.ID, tt.target, Precondition{}); !errors.Is(err, ErrNotFound) {
				t.Fatalf("second delete err = %v", err)
			}
		})
	}
}

func TestMove(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name     string
		files    []string
		from, to string
		pre      Precondition
		wantLive []string
		wantErr  error
		confl    bool
	}{
		{"rename file", []string{"a.txt"}, "a.txt", "b.txt", Precondition{}, []string{"b.txt"}, nil, false},
		{"move into new folder", []string{"a.txt"}, "a.txt", "x/y/a.txt", Precondition{}, []string{"x", "x/y", "x/y/a.txt"}, nil, false},
		{"rename folder", []string{"p/1", "p/q/2", "pz"}, "p", "r", Precondition{}, []string{"pz", "r", "r/1", "r/q", "r/q/2"}, nil, false},
		{"onto own parent folder", []string{"a/b/x"}, "a/b", "a", Precondition{}, nil, ErrTargetExists, false},
		{"target file exists", []string{"a", "b"}, "a", "b", Precondition{}, nil, ErrTargetExists, false},
		{"target folder exists", []string{"a", "b/c"}, "a", "b", Precondition{}, nil, ErrTargetExists, false},
		{"target parent is a file", []string{"a", "b"}, "a", "b/a", Precondition{}, nil, ErrTargetExists, false},
		{"into itself", []string{"p/1"}, "p", "p/sub", Precondition{}, nil, ErrInvalidMove, false},
		{"same path", []string{"a"}, "a", "a", Precondition{}, nil, ErrInvalidMove, false},
		{"missing", []string{"a"}, "zzz", "b", Precondition{}, nil, ErrNotFound, false},
		{"stale If-Match", []string{"a"}, "a", "b", Precondition{Set: true, Version: 999}, nil, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newStore(t)
			u := newUser(t, s)
			for _, p := range tt.files {
				if _, _, err := s.PutFile(ctx, u.ID, p, shaA, 1, Precondition{}); err != nil {
					t.Fatalf("seed %s: %v", p, err)
				}
			}
			_, start, _ := s.ListFiles(ctx, u.ID, nil)
			v, moved, err := s.MoveFile(ctx, u.ID, tt.from, tt.to, tt.pre)
			var ce *ConflictError
			switch {
			case tt.confl:
				if !errors.As(err, &ce) {
					t.Fatalf("err = %v, want conflict", err)
				}
				return
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			case err != nil:
				t.Fatal(err)
			}
			for _, m := range moved {
				if m.File.Version != v || (!m.File.IsDir && m.File.SHA256 != shaA) {
					t.Fatalf("moved entry %+v", m)
				}
			}
			files, _, _ := s.ListFiles(ctx, u.ID, nil)
			var got []string
			for _, f := range files {
				got = append(got, f.Path)
			}
			if len(got) != len(tt.wantLive) {
				t.Fatalf("live = %v, want %v", got, tt.wantLive)
			}
			for i := range got {
				if got[i] != tt.wantLive[i] {
					t.Fatalf("live = %v, want %v", got, tt.wantLive)
				}
			}
			// Feed shows old paths as tombstones so clients delete them.
			feed, _, _ := s.ListFiles(ctx, u.ID, &start)
			tombs := 0
			for _, f := range feed {
				if !f.Live() {
					tombs++
				}
			}
			if tombs != len(moved) {
				t.Fatalf("feed has %d tombstones, want %d", tombs, len(moved))
			}
		})
	}
}

func TestPathCollisionOnUpload(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	u := newUser(t, s)
	_, _, _ = s.PutFile(ctx, u.ID, "a", shaA, 1, Precondition{})
	if _, _, err := s.PutFile(ctx, u.ID, "a/b", shaA, 1, Precondition{}); !errors.Is(err, ErrPathCollision) {
		t.Fatalf("file under a file: %v", err)
	}
	_, _, _ = s.PutFile(ctx, u.ID, "d/e", shaA, 1, Precondition{})
	if _, _, err := s.PutFile(ctx, u.ID, "d", shaA, 1, Precondition{}); !errors.Is(err, ErrPathCollision) {
		t.Fatalf("file over a folder: %v", err)
	}
	// After deleting the folder contents, the name is free again.
	_, _, _ = s.DeleteFile(ctx, u.ID, "d", Precondition{})
	if _, _, err := s.PutFile(ctx, u.ID, "d", shaA, 1, Precondition{}); err != nil {
		t.Fatalf("file over deleted folder: %v", err)
	}
}

func TestConcurrentUpdatesOneWinner(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	u := newUser(t, s)
	f, _, _ := s.PutFile(ctx, u.ID, "race.txt", shaA, 1, Precondition{})

	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sha := shaB[:63] + string(rune('0'+i))
			_, _, err := s.PutFile(ctx, u.ID, "race.txt", sha, 1, Precondition{Set: true, Version: f.Version})
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		var ce *ConflictError
		switch {
		case err == nil:
			wins++
		case errors.As(err, &ce):
			conflicts++
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if wins != 1 || conflicts != 7 {
		t.Fatalf("wins=%d conflicts=%d, want 1 and 7", wins, conflicts)
	}
}

func TestFolders(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	u := newUser(t, s)
	livePaths := func() []string {
		files, _, err := s.ListFiles(ctx, u.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, f := range files {
			p := f.Path
			if f.IsDir {
				p += "/"
			}
			out = append(out, p)
		}
		return out
	}
	assertLive := func(want ...string) {
		t.Helper()
		got := livePaths()
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("live = %v, want %v", got, want)
		}
	}

	// mkdir -p semantics: parents are created in the same version.
	f, created, err := s.CreateFolder(ctx, u.ID, "a/b/c")
	if err != nil || !created || !f.IsDir {
		t.Fatalf("CreateFolder = %+v %v %v", f, created, err)
	}
	assertLive("a/", "a/b/", "a/b/c/")
	parent, _ := s.GetFile(ctx, u.ID, "a")
	if parent.Version != f.Version {
		t.Fatalf("parent version %d != folder version %d", parent.Version, f.Version)
	}

	// Idempotent: no new version.
	before, _ := s.CurrentVersion(ctx)
	if _, created, err := s.CreateFolder(ctx, u.ID, "a/b"); err != nil || created {
		t.Fatalf("re-create = %v %v", created, err)
	}
	if after, _ := s.CurrentVersion(ctx); after != before {
		t.Fatal("re-creating a folder bumped the version")
	}

	// Uploading into a new nested path materializes parents.
	if _, _, err := s.PutFile(ctx, u.ID, "x/y/z.txt", shaA, 1, Precondition{}); err != nil {
		t.Fatal(err)
	}
	assertLive("a/", "a/b/", "a/b/c/", "x/", "x/y/", "x/y/z.txt")

	// Files and folders cannot share a path.
	if _, _, err := s.PutFile(ctx, u.ID, "a/b", shaA, 1, Precondition{}); !errors.Is(err, ErrPathCollision) {
		t.Fatalf("file over folder: %v", err)
	}
	if _, _, err := s.CreateFolder(ctx, u.ID, "x/y/z.txt"); !errors.Is(err, ErrPathCollision) {
		t.Fatalf("folder over file: %v", err)
	}
	if _, _, err := s.CreateFolder(ctx, u.ID, "x/y/z.txt/sub"); !errors.Is(err, ErrPathCollision) {
		t.Fatalf("folder under file: %v", err)
	}

	// Deleting the last file keeps the (now empty) folder.
	if _, _, err := s.DeleteFile(ctx, u.ID, "x/y/z.txt", Precondition{}); err != nil {
		t.Fatal(err)
	}
	assertLive("a/", "a/b/", "a/b/c/", "x/", "x/y/")

	// Empty folders can be moved and deleted.
	if _, moved, err := s.MoveFile(ctx, u.ID, "a/b", "m", Precondition{}); err != nil || len(moved) != 2 {
		t.Fatalf("move empty folders: %v %v", moved, err)
	}
	assertLive("a/", "m/", "m/c/", "x/", "x/y/")
	if _, deleted, err := s.DeleteFile(ctx, u.ID, "x", Precondition{}); err != nil || len(deleted) != 2 {
		t.Fatalf("delete folder: %v %v", deleted, err)
	}
	assertLive("a/", "m/", "m/c/")

	// A folder's name can later be reused for a file, and vice versa.
	if _, _, err := s.PutFile(ctx, u.ID, "x", shaA, 1, Precondition{}); err != nil {
		t.Fatalf("file over deleted folder: %v", err)
	}
	got, _ := s.GetFile(ctx, u.ID, "x")
	if got.IsDir {
		t.Fatal("revived row is still a folder")
	}
}

func TestUpdatePassword(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	u := newUser(t, s)
	for _, h := range []string{"keep", "other1", "other2"} {
		if _, err := s.CreateSession(ctx, u.ID, h, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	_, keepID, _ := s.SessionUser(ctx, "keep")
	if err := s.UpdatePassword(ctx, u.ID, "newhash", keepID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.UserByID(ctx, u.ID)
	if got.PasswordHash != "newhash" {
		t.Fatalf("hash = %q", got.PasswordHash)
	}
	if _, _, err := s.SessionUser(ctx, "keep"); err != nil {
		t.Fatalf("current session ended: %v", err)
	}
	for _, h := range []string{"other1", "other2"} {
		if _, _, err := s.SessionUser(ctx, h); !errors.Is(err, ErrNotFound) {
			t.Fatalf("session %s survived: %v", h, err)
		}
	}
	if err := s.UpdatePassword(ctx, 999, "x", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
}
