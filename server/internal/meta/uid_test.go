package meta

import (
	"context"
	"errors"
	"testing"
)

// The stable file id (uid) is what search results and nulldrive:// links
// point at, so it must follow the file, not the path.
func TestStableFileIDs(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	u := newUser(t, s).ID
	put := func(path, sha string) File {
		t.Helper()
		f, _, err := s.PutFile(ctx, u, path, sha, 10, Precondition{})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	get := func(path string) File {
		t.Helper()
		f, err := s.GetFile(ctx, u, path)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}

	a := put("Docs/a.txt", hA)
	if len(a.UID) != 32 {
		t.Fatalf("uid %q", a.UID)
	}
	// Overwriting keeps it.
	cur := get("Docs/a.txt")
	f, _, err := s.PutFile(ctx, u, "Docs/a.txt", hB, 10, Precondition{Set: true, Version: cur.Version})
	if err != nil || f.UID != a.UID {
		t.Fatalf("overwrite: %v uid %s vs %s", err, f.UID, a.UID)
	}
	// Renaming keeps it; the old path's tombstone gets a different one.
	if _, _, err := s.MoveFile(ctx, u, "Docs/a.txt", "Docs/b.txt", Precondition{}); err != nil {
		t.Fatal(err)
	}
	if b := get("Docs/b.txt"); b.UID != a.UID {
		t.Fatalf("rename changed uid: %s vs %s", b.UID, a.UID)
	}
	if old := get("Docs/a.txt"); old.Live() || old.UID == a.UID {
		t.Fatalf("tombstone kept the moved file's uid: %+v", old)
	}
	// Moving the folder keeps every child's id.
	if _, _, err := s.MoveFile(ctx, u, "Docs", "Archive/Docs", Precondition{}); err != nil {
		t.Fatal(err)
	}
	if b := get("Archive/Docs/b.txt"); b.UID != a.UID {
		t.Fatal("folder move changed a child's uid")
	}
	// Delete then restore from history: same file, same id.
	if _, _, err := s.DeleteFile(ctx, u, "Archive/Docs/b.txt", Precondition{}); err != nil {
		t.Fatal(err)
	}
	vs, err := s.ListVersions(ctx, u, "Archive/Docs/b.txt")
	if err != nil || len(vs) == 0 {
		t.Fatalf("versions: %v %d", err, len(vs))
	}
	r, err := s.RestoreVersion(ctx, u, "Archive/Docs/b.txt", vs[0].Version, Precondition{}, 0)
	if err != nil || r.UID != a.UID {
		t.Fatalf("restore: %v uid %s vs %s", err, r.UID, a.UID)
	}
	// A different file uploaded where a deleted one was gets a new id.
	if _, _, err := s.DeleteFile(ctx, u, "Archive/Docs/b.txt", Precondition{}); err != nil {
		t.Fatal(err)
	}
	if n := put("Archive/Docs/b.txt", hC); n.UID == a.UID {
		t.Fatal("new file reused a deleted file's uid")
	}
	// Ids are unique across users.
	bob := otherUser(t, s)
	g, _, err := s.PutFile(ctx, bob, "Docs/a.txt", hA, 10, Precondition{})
	if err != nil || g.UID == a.UID {
		t.Fatalf("other user: %v", err)
	}
}

func TestCreateUser(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	u, err := s.CreateUser(ctx, "carol", "hash")
	if err != nil || u.IsAdmin || u.Username != "carol" {
		t.Fatalf("%+v %v", u, err)
	}
	if _, err := s.CreateUser(ctx, "CAROL", "hash"); !errors.Is(err, ErrUserExists) {
		t.Fatalf("duplicate: %v", err)
	}
}
