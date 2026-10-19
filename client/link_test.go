package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Null0Grid/null-drive/shared/api"
	"github.com/Null0Grid/null-drive/shared/client"
)

const testID = "d28fc8c06bfa5b3229e9cada9b33e866"

func TestParseLink(t *testing.T) {
	good := []string{
		"nulldrive://open?id=" + testID,
		"NULLDRIVE://OPEN?id=" + testID,
		"nulldrive://open/?id=" + testID,
		"nulldrive:open?id=" + testID,
		"nulldrive://open?id=D28FC8C06BFA5B3229E9CADA9B33E866",
		"  nulldrive://open?id=" + testID + "\n",
	}
	for _, s := range good {
		l, err := parseLink(s)
		if err != nil || l.ID != testID {
			t.Errorf("%q: %+v %v", s, l, err)
		}
	}
	bad := []string{
		"", "nulldrive://", "nulldrive://open", "nulldrive://open?id=",
		"nullnotes://open?id=" + testID,
		"https://open?id=" + testID,
		"nulldrive://delete?id=" + testID,
		"nulldrive://open/../x?id=" + testID,
		"nulldrive://open/C:/Windows?id=" + testID,
		"nulldrive://user@open?id=" + testID,
		"nulldrive://open?id=" + testID + "#frag",
		"nulldrive://open?id=" + testID + "&id=" + testID,
		"nulldrive://open?id=" + testID[:31],
		"nulldrive://open?id=" + testID + "0",
		"nulldrive://open?id=../../etc/passwd",
		"nulldrive://open?id=" + testID[:30] + "zz",
		`nulldrive://open?id=` + testID + `" --forget`,
		"nulldrive://open?id=%00" + testID,
		"nulldrive://open?id=" + string(make([]byte, 3000)),
	}
	for _, s := range bad {
		if l, err := parseLink(s); !errors.Is(err, errBadLink) {
			t.Errorf("%q accepted: %+v", s, l)
		}
	}
}

func TestLinkFromArgs(t *testing.T) {
	cases := []struct {
		args []string
		want string
		ok   bool
	}{
		{[]string{"--url", "nulldrive://open?id=x"}, "nulldrive://open?id=x", true},
		{[]string{"nulldrive://open?id=x"}, "nulldrive://open?id=x", true},
		{[]string{"NullDrive:open?id=x"}, "NullDrive:open?id=x", true},
		// Whatever else is on the line, the link wins (and the caller then
		// ignores the rest).
		{[]string{"--forget", "--url", "nulldrive://open?id=x"}, "nulldrive://open?id=x", true},
		{[]string{"--url", "nulldrive://x", "--quit"}, "nulldrive://x", true},
		// What Windows actually passes for a link crafted to break out of
		// the quotes in `--url "%1"` (observed in Wine): the link (whose
		// id then fails parseLink) and stray arguments that must be ignored.
		{[]string{"--url", "nulldrive://open?id=x", "--forget", "y"}, "nulldrive://open?id=x", true},
		{[]string{"--hidden"}, "", false},
		{[]string{"--quit"}, "", false},
		{[]string{"--url"}, "", false},
		{[]string{"nulldrivex://open"}, "", false},
		{nil, "", false},
	}
	for _, c := range cases {
		got, ok := linkFromArgs(c.args)
		if got != c.want || ok != c.ok {
			t.Errorf("%q: %q %v", c.args, got, ok)
		}
	}
}

func TestCraftedCommandLineOpensNothing(t *testing.T) {
	raw, ok := linkFromArgs([]string{"--url", "nulldrive://open?id=x", "--forget", "y"})
	if !ok {
		t.Fatal("link not found")
	}
	if _, err := parseLink(raw); !errors.Is(err, errBadLink) {
		t.Fatal("crafted link accepted")
	}
}

func TestDangerous(t *testing.T) {
	for _, n := range []string{"setup.exe", "RUN.BAT", "x.ps1", "a.lnk", "evil.js", "s.vbs", "i.msi", "p.hta",
		"page.url", "c.reg", "j.jar", "m.app", "s.sh", "a.desktop", "x.AppImage", "trailing.exe.", "space.exe ", "y.appinstaller"} {
		if !isDangerous(n) {
			t.Errorf("%q should not be opened", n)
		}
	}
	for _, n := range []string{"notes.md", "report.pdf", "photo.JPG", "budget.xlsx", "README", "archive.tar.gz", "exe", ".bashrc.txt"} {
		if isDangerous(n) {
			t.Errorf("%q should open", n)
		}
	}
}

func TestURLCommand(t *testing.T) {
	exe := `C:\Users\Ann Lee\AppData\Local\Programs\Null Drive\NullDrive.exe`
	cmd := urlCommand(exe)
	if cmd != `"C:\Users\Ann Lee\AppData\Local\Programs\Null Drive\NullDrive.exe" --url "%1"` {
		t.Fatal(cmd)
	}
	if got, err := commandExe(cmd); err != nil || got != exe {
		t.Fatalf("%q %v", got, err)
	}
	if got, _ := commandExe(`C:\nd.exe "%1"`); got != `C:\nd.exe` {
		t.Fatal(got)
	}
	if _, err := commandExe(`"C:\broken`); err == nil {
		t.Fatal("unterminated quote")
	}
}

func TestWebOpenURL(t *testing.T) {
	if u := webOpenURL("https://nas.lan:8443/", testID); u != "https://nas.lan:8443/open?id="+testID {
		t.Fatal(u)
	}
}

// ---- resolveLink ----

type fakeRemote struct {
	files map[string]api.File
	err   error
}

func (f fakeRemote) FileByID(_ context.Context, id string) (api.File, error) {
	if f.err != nil {
		return api.File{}, f.err
	}
	if v, ok := f.files[id]; ok {
		return v, nil
	}
	return api.File{}, &client.Error{Status: 404, Code: "not_found"}
}

type calls struct{ opened, revealed []string }

func testEnv(t *testing.T, r fakeRemote) (linkEnv, *calls, string) {
	dir := t.TempDir()
	c := &calls{}
	return linkEnv{
		folder: dir, configured: true, remote: r, wait: 0,
		open:   func(p string) error { c.opened = append(c.opened, p); return nil },
		reveal: func(p string) error { c.revealed = append(c.revealed, p); return nil },
	}, c, dir
}

func write(t *testing.T, dir, rel string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), mode); err != nil {
		t.Fatal(err)
	}
}

func TestResolveLink(t *testing.T) {
	ctx := context.Background()
	files := map[string]api.File{
		"a": {Path: "Docs/report.pdf"},
		"b": {Path: "Tools/setup.exe"},
		"c": {Path: "Docs", IsDir: true},
		"d": {Path: "Old/gone.txt", Deleted: true},
		"e": {Path: "Later/new.md"},
		"f": {Path: "../escape.txt"},
		"g": {Path: "bin/tool"},
	}
	env, c, dir := testEnv(t, fakeRemote{files: files})
	write(t, dir, "Docs/report.pdf", 0o644)
	write(t, dir, "Tools/setup.exe", 0o644)
	write(t, dir, "bin/tool", 0o755)

	check := func(id string, want linkOutcome) linkResult {
		t.Helper()
		r := resolveLink(ctx, env, id)
		if r.outcome != want {
			t.Fatalf("%s: %s (%v), want %s", id, r.outcome, r.err, want)
		}
		return r
	}
	check("a", linkOpened)
	if len(c.opened) != 1 || c.opened[0] != filepath.Join(dir, "Docs", "report.pdf") {
		t.Fatalf("opened %v", c.opened)
	}
	check("b", linkRevealed) // a program: shown, not run
	if len(c.revealed) != 1 || c.revealed[0] != filepath.Join(dir, "Tools", "setup.exe") {
		t.Fatalf("revealed %v", c.revealed)
	}
	check("c", linkOpened) // folder
	check("d", linkDeleted)
	check("e", linkNotSynced)
	check("f", linkFailed) // a path that leaves the folder is refused
	if runtime.GOOS != "windows" {
		check("g", linkRevealed) // executable bit, no extension
	}
	check("zz", linkNotFound)
	if len(c.opened) != 2 {
		t.Fatalf("opened %v", c.opened)
	}

	// Symlinks are never followed into an open.
	if runtime.GOOS != "windows" {
		files["h"] = api.File{Path: "link.pdf"}
		if err := os.Symlink("/bin/sh", filepath.Join(dir, "link.pdf")); err != nil {
			t.Fatal(err)
		}
		check("h", linkRevealed)
	}

	// Server unreachable.
	env2, _, _ := testEnv(t, fakeRemote{err: errors.New("dial tcp: connection refused")})
	if r := resolveLink(ctx, env2, "a"); r.outcome != linkFailed {
		t.Fatal(r)
	}
	// Not set up / locked.
	env3, _, _ := testEnv(t, fakeRemote{})
	env3.configured = false
	if r := resolveLink(ctx, env3, "a"); r.outcome != linkNotSetUp {
		t.Fatal(r)
	}
	env3.configured, env3.remote = true, nil
	if r := resolveLink(ctx, env3, "a"); r.outcome != linkLocked {
		t.Fatal(r)
	}
}

func TestResolveLinkWaitsForSync(t *testing.T) {
	env, c, dir := testEnv(t, fakeRemote{files: map[string]api.File{"x": {Path: "new/arrived.txt"}}})
	env.wait = 5 * time.Second
	synced := make(chan struct{})
	env.syncNow = func() {
		go func() { // the sync engine downloads it a moment later
			time.Sleep(400 * time.Millisecond)
			write(t, dir, "new/arrived.txt", 0o644)
			close(synced)
		}()
	}
	r := resolveLink(context.Background(), env, "x")
	<-synced
	if r.outcome != linkOpened || len(c.opened) != 1 {
		t.Fatalf("%+v %v", r, c.opened)
	}
}

func TestResolveLinkDecrypts(t *testing.T) {
	env, c, dir := testEnv(t, fakeRemote{files: map[string]api.File{"x": {Path: "~enc/~name"}}})
	env.decrypt = func(p string) (string, error) {
		if p != "~enc/~name" {
			return "", errors.New("unexpected")
		}
		return "Private/diary.md", nil
	}
	write(t, dir, "Private/diary.md", 0o644)
	if r := resolveLink(context.Background(), env, "x"); r.outcome != linkOpened || filepath.Base(c.opened[0]) != "diary.md" {
		t.Fatalf("%+v", r)
	}
}
