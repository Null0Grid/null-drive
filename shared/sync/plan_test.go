package sync

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func file(p, sha string) *Local          { return &Local{Path: p, SHA256: sha, Size: 1} }
func base(p, sha string) *Entry          { return &Entry{Path: p, SHA256: sha, Version: 1} }
func rem(p, sha string, v int64) *Remote { return &Remote{Path: p, SHA256: sha, Version: v} }

func kinds(ops []Op) []string {
	out := make([]string, len(ops))
	for i, op := range ops {
		s := op.Kind.String() + " " + op.Path
		if op.From != "" {
			s += " <- " + op.From
		}
		out[i] = s
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d ops %v, want %v", len(got), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("op %d = %q, want %q\nall: %v", i, got[i], want[i], got)
		}
	}
}

func TestPlanLocalRenameBecomesServerMove(t *testing.T) {
	locals := map[string]*Local{"new/name.txt": file("new/name.txt", "A")}
	bases := map[string]*Entry{"old.txt": base("old.txt", "A")}
	remotes := map[string]*Remote{"old.txt": rem("old.txt", "A", 1)}
	eq(t, kinds(Plan(locals, bases, remotes, nil)), []string{"move-remote new/name.txt <- old.txt"})
}

func TestPlanRemoteRenameBecomesLocalMove(t *testing.T) {
	locals := map[string]*Local{"old.txt": file("old.txt", "A")}
	bases := map[string]*Entry{"old.txt": base("old.txt", "A")}
	remotes := map[string]*Remote{"Archive/old.txt": rem("Archive/old.txt", "A", 2)}
	eq(t, kinds(Plan(locals, bases, remotes, nil)), []string{"move-local Archive/old.txt <- old.txt"})
}

func TestPlanRenamePrefersSameName(t *testing.T) {
	// Two deleted files with identical content; the moved one keeps its name.
	locals := map[string]*Local{"dir/b.txt": file("dir/b.txt", "X")}
	bases := map[string]*Entry{"a.txt": base("a.txt", "X"), "b.txt": base("b.txt", "X")}
	remotes := map[string]*Remote{"a.txt": rem("a.txt", "X", 1), "b.txt": rem("b.txt", "X", 1)}
	eq(t, kinds(Plan(locals, bases, remotes, nil)), []string{
		"move-remote dir/b.txt <- b.txt",
		"delete-remote a.txt",
	})
}

func TestPlanNoRenameWhenContentDiffers(t *testing.T) {
	locals := map[string]*Local{"new.txt": file("new.txt", "B")}
	bases := map[string]*Entry{"old.txt": base("old.txt", "A")}
	remotes := map[string]*Remote{"old.txt": rem("old.txt", "A", 1)}
	eq(t, kinds(Plan(locals, bases, remotes, nil)), []string{"upload new.txt", "delete-remote old.txt"})
}

func TestPlanOrdering(t *testing.T) {
	locals := map[string]*Local{
		"z.txt":        file("z.txt", "Z"),
		"newdir":       {Path: "newdir", IsDir: true},
		"newdir/a.txt": file("newdir/a.txt", "N"),
	}
	bases := map[string]*Entry{
		"gone":          {Path: "gone", IsDir: true, Version: 1},
		"gone/deep.txt": base("gone/deep.txt", "G"),
	}
	remotes := map[string]*Remote{
		"gone":          {Path: "gone", IsDir: true, Version: 1},
		"gone/deep.txt": rem("gone/deep.txt", "G", 1),
		"srv":           {Path: "srv", IsDir: true, Version: 3},
	}
	eq(t, kinds(Plan(locals, bases, remotes, nil)), []string{
		"create-remote-dir newdir", // folders first, parents before children
		"create-local-dir srv",
		"upload newdir/a.txt", // then transfers, in path order
		"upload z.txt",
		"delete-remote gone/deep.txt", // deletions last, children first
		"delete-remote gone",
	})
}

func TestPlanSkipProtectsUnreadableAndIgnored(t *testing.T) {
	// locked.docx couldn't be read this scan, so it's absent from locals.
	// Without skip it would look deleted and be deleted on the server.
	bases := map[string]*Entry{"locked.docx": base("locked.docx", "L")}
	remotes := map[string]*Remote{"locked.docx": rem("locked.docx", "L", 1)}
	if ops := Plan(map[string]*Local{}, bases, remotes, func(p string) bool { return p == "locked.docx" }); len(ops) != 0 {
		t.Fatalf("skipped path produced ops: %v", kinds(ops))
	}
}

func TestScan(t *testing.T) {
	root := t.TempDir()
	write := func(p, content string) {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "hello")
	write("dir/b.txt", "world")
	write("dir/Thumbs.db", "junk")
	write("node_modules/x/y.js", "ignored by .nullignore")
	write(".nullignore", "node_modules/\n")
	write("half.txt"+partSuffix, "interrupted download")
	if runtime.GOOS != "windows" {
		write("bad:name.txt", "colon is illegal on Windows")
	}

	res, err := Scan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"a.txt", "dir", "dir/b.txt", ".nullignore"} {
		if res.Items[want] == nil {
			t.Errorf("missing %s", want)
		}
	}
	for _, not := range []string{"dir/Thumbs.db", "node_modules", "node_modules/x/y.js", "half.txt" + partSuffix} {
		if res.Items[not] != nil {
			t.Errorf("should not include %s", not)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "half.txt"+partSuffix)); !errors.Is(err, os.ErrNotExist) {
		t.Error("leftover partial download not cleaned up")
	}
	if res.Items["a.txt"].SHA256 != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Errorf("hash = %s", res.Items["a.txt"].SHA256)
	}
	if runtime.GOOS != "windows" {
		if len(res.Skipped) != 1 || res.Skipped[0].Path != "bad:name.txt" {
			t.Errorf("skipped = %+v", res.Skipped)
		}
	}

	// Unchanged size+mtime: the base hash is trusted (no re-read).
	a := res.Items["a.txt"]
	bases := map[string]*Entry{"a.txt": {Path: "a.txt", Size: a.Size, MtimeNs: a.MtimeNs, SHA256: "cached"}}
	res2, _ := Scan(root, bases)
	if res2.Items["a.txt"].SHA256 != "cached" {
		t.Error("hash not reused for unchanged file")
	}
	// Changed mtime: re-hashed.
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(filepath.Join(root, "a.txt"), future, future)
	res3, _ := Scan(root, bases)
	if res3.Items["a.txt"].SHA256 == "cached" {
		t.Error("changed file not re-hashed")
	}

	if _, err := Scan(filepath.Join(root, "nope"), nil); !errors.Is(err, ErrRootMissing) {
		t.Fatalf("missing root: %v", err)
	}
}
