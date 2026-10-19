// Package synctest runs the real sync engine against a real server: two
// simulated devices, each with its own folder and index, converging through
// create, edit, rename, delete and offline conflicts.
//
// It lives under server/ because it starts the server in-process (server
// internals are not importable from shared/).
package synctest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	srvapi "github.com/Null0Grid/null-drive/server/internal/api"
	"github.com/Null0Grid/null-drive/server/internal/auth"
	"github.com/Null0Grid/null-drive/server/internal/db"
	"github.com/Null0Grid/null-drive/server/internal/meta"
	"github.com/Null0Grid/null-drive/server/storage"
	"github.com/Null0Grid/null-drive/shared/api"
	"github.com/Null0Grid/null-drive/shared/client"
	nsync "github.com/Null0Grid/null-drive/shared/sync"
)

// ---------- harness ----------

type world struct {
	t      *testing.T
	ts     *httptest.Server
	admin  *client.Client
	events []string
	dir    string // server data dir
}

type device struct {
	name   string
	root   string
	engine *nsync.Engine
	w      *world
	token  string
	ix     *nsync.Index
}

func newWorld(t *testing.T) *world {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "server.db"))
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
	s, err := srvapi.New(srvapi.Options{
		Store: meta.New(d, nil), Blobs: blobs, Hasher: auth.Hasher{P: auth.TestParams},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DataDir: dir,
		SearchDir: filepath.Join(dir, "indexes"), // on, as in production
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	t.Cleanup(func() { ts.Close(); _ = s.Close() })

	anon := client.New(ts.URL, "", nil)
	ctx := context.Background()
	// Setup through the raw API (the wizard's call).
	var sess api.SessionResponse
	{
		c := client.New(ts.URL, "", nil)
		_ = c
		res, err := ts.Client().Post(ts.URL+"/api/v1/setup", "application/json",
			strings.NewReader(`{"username":"admin","password":"correct horse battery"}`))
		if err != nil || res.StatusCode != 201 {
			t.Fatalf("setup: %v %v", err, res)
		}
		res.Body.Close()
		sess, err = anon.Login(ctx, "admin", "correct horse battery")
		if err != nil {
			t.Fatal(err)
		}
	}
	return &world{t: t, ts: ts, admin: client.New(ts.URL, sess.Token, nil), dir: dir}
}

func (w *world) device(name string) *device {
	t := w.t
	t.Helper()
	reg, err := w.admin.RegisterDevice(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "NullDrive")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	ix, err := nsync.OpenIndex(filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ix.Close() })
	d := &device{name: name, root: root, w: w, token: reg.Token, ix: ix}
	d.engine = &nsync.Engine{
		Root: root, Index: ix, Remote: client.New(w.ts.URL, reg.Token, nil), Device: name,
		OnEvent: func(ev nsync.Event) {
			s := name + ": " + ev.Kind + " " + ev.Path
			if ev.From != "" {
				s += " <- " + ev.From
			}
			if ev.Err != "" {
				s += " ERR " + ev.Err
			}
			w.events = append(w.events, s)
		},
	}
	return d
}

func (d *device) full(p string) string { return filepath.Join(d.root, filepath.FromSlash(p)) }

func (d *device) write(p, content string) {
	d.w.t.Helper()
	full := d.full(p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		d.w.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		d.w.t.Fatal(err)
	}
	// Make sure the edit is visible to mtime-based change detection even
	// on filesystems with coarse timestamps.
	future := time.Now().Add(time.Duration(len(content)+1) * time.Millisecond)
	_ = os.Chtimes(full, future, future)
}

func (d *device) mkdir(p string) {
	if err := os.MkdirAll(d.full(p), 0o755); err != nil {
		d.w.t.Fatal(err)
	}
}

func (d *device) rm(p string) {
	if err := os.RemoveAll(d.full(p)); err != nil {
		d.w.t.Fatal(err)
	}
}

func (d *device) mv(from, to string) {
	if err := os.MkdirAll(filepath.Dir(d.full(to)), 0o755); err != nil {
		d.w.t.Fatal(err)
	}
	if err := os.Rename(d.full(from), d.full(to)); err != nil {
		d.w.t.Fatal(err)
	}
}

func (d *device) sync() *nsync.Report {
	d.w.t.Helper()
	rep, err := d.engine.Sync(context.Background())
	if err != nil {
		d.w.t.Fatalf("%s sync: %v", d.name, err)
	}
	if rep.Failed > 0 {
		d.w.t.Fatalf("%s sync had %d failures: %v", d.name, rep.Failed, d.w.events)
	}
	return rep
}

// tree returns path → content ("<dir>" for folders) of a device folder.
func (d *device) tree() map[string]string {
	out := map[string]string{}
	_ = filepath.WalkDir(d.root, func(p string, e os.DirEntry, err error) error {
		if err != nil || p == d.root {
			return err
		}
		rel, _ := filepath.Rel(d.root, p)
		rel = filepath.ToSlash(rel)
		if e.IsDir() {
			out[rel] = "<dir>"
			return nil
		}
		b, _ := os.ReadFile(p)
		out[rel] = string(b)
		return nil
	})
	return out
}

// serverTree is the server's live state, contents included. A running
// agent can delete or replace a file between the listing and its download
// (e.g. a conflict copy being resolved): then the snapshot is taken again,
// so callers always see one consistent state.
func (w *world) serverTree() map[string]string {
	ctx := context.Background()
	for attempt := 0; ; attempt++ {
		out, vanished := w.serverTreeOnce(ctx)
		if !vanished {
			return out
		}
		if attempt == 20 {
			w.t.Fatal("server tree kept changing between listing and download")
		}
	}
}

func (w *world) serverTreeOnce(ctx context.Context) (out map[string]string, vanished bool) {
	list, err := w.admin.ListFiles(ctx, nil)
	if err != nil {
		w.t.Fatal(err)
	}
	out = map[string]string{}
	for _, f := range list.Files {
		if f.IsDir {
			out[f.Path] = "<dir>"
			continue
		}
		rc, _, err := w.admin.Download(ctx, f.Path)
		if client.IsCode(err, "not_found") {
			return nil, true
		}
		if err != nil {
			w.t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Path] = string(b)
	}
	return out, false
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// settle syncs all devices round-robin until a full round changes nothing.
func settle(t *testing.T, devs ...*device) {
	t.Helper()
	for round := 0; round < 6; round++ {
		work := 0
		for _, d := range devs {
			for _, n := range d.sync().Ops {
				work += n
			}
		}
		if work == 0 {
			return
		}
	}
	t.Fatal("did not converge in 6 rounds")
}

// assertConverged checks every device and the server hold the same tree,
// and optionally that it equals want.
func assertConverged(t *testing.T, w *world, want map[string]string, devs ...*device) {
	t.Helper()
	srv := w.serverTree()
	for _, d := range devs {
		got := d.tree()
		if strings.Join(keys(got), "|") != strings.Join(keys(srv), "|") {
			t.Fatalf("%s tree %v\nserver   %v\nevents: %v", d.name, keys(got), keys(srv), w.events)
		}
		for k, v := range srv {
			if got[k] != v {
				t.Fatalf("%s: %s = %q, server has %q", d.name, k, got[k], v)
			}
		}
	}
	if want != nil {
		if strings.Join(keys(srv), "|") != strings.Join(keys(want), "|") {
			t.Fatalf("server tree %v, want %v\nevents: %v", keys(srv), keys(want), w.events)
		}
		for k, v := range want {
			if v != "*" && srv[k] != v {
				t.Fatalf("server %s = %q, want %q", k, srv[k], v)
			}
		}
	}
}

func conflictCopies(tree map[string]string) []string {
	var out []string
	for p := range tree {
		if nsync.IsConflictCopy(p) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func hasEvent(w *world, substr string) bool {
	for _, e := range w.events {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}

// ---------- scenarios ----------

func TestCreateEditRenameDelete(t *testing.T) {
	w := newWorld(t)
	a, b := w.device("Desktop"), w.device("Laptop")

	// Create files and folders (including an empty one) on A.
	a.write("notes.txt", "hello")
	a.write("Photos/2026/beach.jpg", "JPEGDATA")
	a.write("Docs/report.md", "# Report")
	a.mkdir("Empty")
	a.write(".nullignore", "*.bak\n")
	a.write("Docs/old.bak", "ignored")
	a.write("Docs/~$report.docx", "office lock file")
	settle(t, a, b)
	assertConverged(t, w, map[string]string{
		"notes.txt": "hello", "Photos": "<dir>", "Photos/2026": "<dir>", "Photos/2026/beach.jpg": "JPEGDATA",
		"Docs": "<dir>", "Docs/report.md": "# Report", "Empty": "<dir>", ".nullignore": "*.bak\n",
	}, b) // A keeps its ignored files locally; they never reach the server or B
	if bt := b.tree(); bt["Docs/old.bak"] != "" || bt["Docs/~$report.docx"] != "" {
		t.Fatal("ignored files reached B")
	}
	a.rm("Docs/old.bak")
	a.rm("Docs/~$report.docx")

	// Edit on B.
	b.write("notes.txt", "hello, edited on the laptop")
	settle(t, a, b)
	assertConverged(t, w, nil, a, b)
	if a.tree()["notes.txt"] != "hello, edited on the laptop" {
		t.Fatal("edit did not reach A")
	}

	// Rename a file and move it to another folder on A: a server-side move.
	w.events = nil
	a.mv("Docs/report.md", "Archive/report-2026.md")
	settle(t, a, b)
	assertConverged(t, w, nil, a, b)
	if !hasEvent(w, "Desktop: move-remote Archive/report-2026.md <- Docs/report.md") {
		t.Fatalf("rename was not a server move: %v", w.events)
	}
	if !hasEvent(w, "Laptop: move-local Archive/report-2026.md <- Docs/report.md") {
		t.Fatalf("B did not rename locally: %v", w.events)
	}

	// Rename a whole folder on B.
	b.mv("Photos", "Pictures")
	settle(t, a, b)
	assertConverged(t, w, nil, a, b)
	if _, ok := a.tree()["Pictures/2026/beach.jpg"]; !ok {
		t.Fatalf("folder rename not applied on A: %v", keys(a.tree()))
	}

	// Delete a file and a folder on A.
	a.rm("notes.txt")
	a.rm("Pictures")
	settle(t, a, b)
	assertConverged(t, w, map[string]string{
		"Docs": "<dir>", "Archive": "<dir>", "Archive/report-2026.md": "# Report", "Empty": "<dir>", ".nullignore": "*.bak\n",
	}, a, b)
}

func TestOfflineEditEditConflictKeepsBoth(t *testing.T) {
	w := newWorld(t)
	a, b := w.device("Desktop"), w.device("Laptop")
	a.write("plan.txt", "v1")
	settle(t, a, b)

	// Both edit while "offline" (no sync in between).
	a.write("plan.txt", "v2 from desktop")
	b.write("plan.txt", "v2 from the laptop!")
	a.sync() // A wins the race to the server
	b.sync() // B detects the conflict
	settle(t, a, b)
	assertConverged(t, w, nil, a, b)

	tree := a.tree()
	if tree["plan.txt"] != "v2 from desktop" {
		t.Fatalf("original should hold the version that reached the server first, got %q", tree["plan.txt"])
	}
	copies := conflictCopies(tree)
	if len(copies) != 1 || tree[copies[0]] != "v2 from the laptop!" || !strings.Contains(copies[0], "Laptop") {
		t.Fatalf("conflict copies = %v (%v)", copies, tree)
	}
	cs, _ := b.engine.Index.Conflicts(context.Background())
	if len(cs) != 1 || cs[0].Path != "plan.txt" || cs[0].ConflictPath != copies[0] {
		t.Fatalf("conflict record = %+v", cs)
	}
}

func TestNewFileSameNameOnBothSides(t *testing.T) {
	w := newWorld(t)
	a, b := w.device("A"), w.device("B")
	a.write("todo.txt", "from A")
	b.write("todo.txt", "from B, different")
	a.sync()
	b.sync()
	settle(t, a, b)
	assertConverged(t, w, nil, a, b)
	if n := len(conflictCopies(a.tree())); n != 1 {
		t.Fatalf("want 1 conflict copy, got %d: %v", n, a.tree())
	}

	// Identical content created on both sides converges without a copy.
	a.write("same.txt", "identical")
	b.write("same.txt", "identical")
	a.sync()
	b.sync()
	settle(t, a, b)
	if n := len(conflictCopies(a.tree())); n != 1 {
		t.Fatalf("identical files produced a conflict: %v", a.tree())
	}
}

func TestEditVersusDeleteEditWins(t *testing.T) {
	w := newWorld(t)
	a, b := w.device("A"), w.device("B")
	a.write("keep-me.txt", "v1")
	a.write("also.txt", "v1")
	settle(t, a, b)

	// A deletes, B edits (offline): the edit survives everywhere.
	a.rm("keep-me.txt")
	b.write("keep-me.txt", "v2 edited")
	a.sync()
	b.sync()
	settle(t, a, b)
	assertConverged(t, w, nil, a, b)
	if a.tree()["keep-me.txt"] != "v2 edited" {
		t.Fatal("edit lost to a delete (local edit vs remote delete)")
	}

	// Reverse order: B edits and syncs first, then A's stale delete.
	b.write("also.txt", "v2 edited on B")
	a.rm("also.txt")
	b.sync()
	a.sync()
	settle(t, a, b)
	assertConverged(t, w, nil, a, b)
	if a.tree()["also.txt"] != "v2 edited on B" {
		t.Fatal("edit lost to a delete (remote edit vs local delete)")
	}
}

func TestStaleClientDoesNotResurrectDeletes(t *testing.T) {
	w := newWorld(t)
	a, b := w.device("A"), w.device("B")
	a.write("old.txt", "obsolete")
	a.write("Folder/inside.txt", "obsolete too")
	settle(t, a, b)

	// B goes offline for a while; meanwhile A deletes things.
	a.rm("old.txt")
	a.rm("Folder")
	a.sync()
	// B comes back: its copies are unchanged since the last sync, so they
	// must be deleted, not re-uploaded.
	b.sync()
	settle(t, a, b)
	assertConverged(t, w, map[string]string{}, a, b)
}

func TestRenameEditConflictKeepsBoth(t *testing.T) {
	w := newWorld(t)
	a, b := w.device("A"), w.device("B")
	a.write("doc.txt", "original")
	settle(t, a, b)

	a.mv("doc.txt", "renamed.txt")    // A renames
	b.write("doc.txt", "edited on B") // B edits the old name
	a.sync()
	b.sync()
	settle(t, a, b)
	assertConverged(t, w, nil, a, b)
	tree := a.tree()
	if tree["renamed.txt"] != "original" || tree["doc.txt"] != "edited on B" {
		t.Fatalf("rename/edit lost data: %v", tree)
	}
}

func TestMassDeleteGuardAndMissingRoot(t *testing.T) {
	w := newWorld(t)
	a, b := w.device("A"), w.device("B")
	for i := 0; i < 30; i++ {
		a.write(filepath.ToSlash(filepath.Join("bulk", string(rune('a'+i%26))+string(rune('0'+i/26))+".txt")), "data")
	}
	settle(t, a, b)

	// Wiping the folder contents trips the guard; the server is untouched.
	for p := range a.tree() {
		if !strings.Contains(p, "/") {
			a.rm(p)
		}
	}
	_, err := a.engine.Sync(context.Background())
	var mass *nsync.ErrMassDelete
	if !errors.As(err, &mass) || mass.Count != 30 {
		t.Fatalf("want mass-delete guard, got %v", err)
	}
	if len(w.serverTree()) != 31 {
		t.Fatal("guard did not protect the server")
	}
	// Confirming lets it through.
	a.engine.AllowMassDelete = true
	a.sync()
	settle(t, a, b)
	assertConverged(t, w, map[string]string{}, a, b)

	// A missing sync folder (unplugged drive) is an error, never "delete all".
	b.write("x.txt", "precious")
	settle(t, a, b)
	_ = os.RemoveAll(a.root)
	if _, err := a.engine.Sync(context.Background()); !errors.Is(err, nsync.ErrRootMissing) {
		t.Fatalf("missing root: %v", err)
	}
	if w.serverTree()["x.txt"] != "precious" {
		t.Fatal("server data touched while root was missing")
	}
}

// flakyRemote simulates a crash/disconnect right after the server applied
// an upload but before the client recorded it.
type flakyRemote struct {
	nsync.RemoteAPI
	failAfterUpload bool
}

func (f *flakyRemote) Upload(ctx context.Context, p string, body io.Reader, size int64, sha string, ifm *int64) (api.File, error) {
	file, err := f.RemoteAPI.Upload(ctx, p, body, size, sha, ifm)
	if err == nil && f.failAfterUpload {
		return api.File{}, errors.New("connection reset by peer")
	}
	return file, err
}

func TestCrashAfterUploadRecovers(t *testing.T) {
	w := newWorld(t)
	a, b := w.device("A"), w.device("B")
	real := a.engine.Remote
	a.engine.Remote = &flakyRemote{RemoteAPI: real, failAfterUpload: true}
	a.write("important.txt", "do not duplicate me")
	if _, err := a.engine.Sync(context.Background()); err == nil {
		t.Fatal("expected the simulated disconnect")
	}
	// Restart with a healthy connection: the index never recorded the
	// upload, but the content matches the server, so it converges quietly.
	a.engine.Remote = real
	settle(t, a, b)
	assertConverged(t, w, map[string]string{"important.txt": "do not duplicate me"}, a, b)
	if hasEvent(w, "conflict") {
		t.Fatalf("crash recovery produced a conflict: %v", w.events)
	}
}

func TestAgentRunsAndResolvesConflicts(t *testing.T) {
	w := newWorld(t)
	a, b := w.device("A"), w.device("B")
	a.write("f.txt", "base")
	settle(t, a, b)
	a.write("f.txt", "A's version")
	b.write("f.txt", "B's version!!")
	a.sync()
	b.sync() // conflict on B

	agent := nsync.NewAgent(b.engine)
	agent.PollInterval = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { agent.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	waitFor(t, func() bool { return agent.Status().State == nsync.StateIdle })
	if agent.Status().Conflicts != 1 {
		t.Fatalf("status = %+v", agent.Status())
	}
	cs, _ := b.engine.Index.Conflicts(context.Background())
	// "Keep mine": B's copy replaces the original everywhere.
	if err := agent.ResolveConflict(context.Background(), cs[0].ID, nsync.ResolveKeepMine); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return w.serverTree()["f.txt"] == "B's version!!" })
	cancel()
	<-done
	settle(t, a, b)
	assertConverged(t, w, map[string]string{"f.txt": "B's version!!"}, a, b)

	agent.Pause()
	if agent.Status().State != nsync.StatePaused {
		t.Fatal("pause not reflected")
	}
	if len(agent.Activity()) == 0 {
		t.Fatal("no activity recorded")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not reached in time")
}

// TestRealtimePushPropagatesWithinSeconds is the Phase 5 acceptance check:
// with polling effectively disabled (1h), an edit on A reaches B's disk
// in well under two seconds, carried only by the WebSocket push.
func TestRealtimePushPropagatesWithinSeconds(t *testing.T) {
	w := newWorld(t)
	a, b := w.device("A"), w.device("B")
	settle(t, a, b)

	run := func(d *device) (*nsync.Agent, func()) {
		ag := nsync.NewAgent(d.engine)
		ag.PollInterval, ag.LivePollInterval = time.Hour, time.Hour
		n := &client.Notifier{
			Client:    d.engine.Remote.(*client.Client),
			OnVersion: func(v int64) { ag.RemoteChanged(context.Background(), v) },
			OnState:   func(s client.LiveState, _ error) { ag.SetLive(s == client.LiveConnected) },
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{}, 2)
		go func() { ag.Run(ctx); done <- struct{}{} }()
		go func() { _ = n.Run(ctx); done <- struct{}{} }()
		return ag, func() { cancel(); <-done; <-done }
	}
	agA, stopA := run(a)
	defer stopA()
	agB, stopB := run(b)
	defer stopB()
	waitFor(t, func() bool {
		return agA.Status().Live && agB.Status().Live &&
			agA.Status().State == nsync.StateIdle && agB.Status().State == nsync.StateIdle
	})

	for i, content := range []string{"first edit", "second edit"} {
		a.write("live.txt", content)
		start := time.Now()
		agA.Trigger() // what the file watcher does
		deadline := start.Add(2 * time.Second)
		for {
			if got, err := os.ReadFile(b.full("live.txt")); err == nil && string(got) == content {
				t.Logf("edit %d visible on B after %v", i+1, time.Since(start).Round(time.Millisecond))
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("edit %d did not reach B within 2s (status B: %+v)", i+1, agB.Status())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}
