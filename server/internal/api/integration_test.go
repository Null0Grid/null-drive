package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	srvapi "github.com/Null0Grid/null-drive/server/internal/api"
	"github.com/Null0Grid/null-drive/server/internal/auth"
	"github.com/Null0Grid/null-drive/server/internal/db"
	"github.com/Null0Grid/null-drive/server/internal/meta"
	"github.com/Null0Grid/null-drive/server/storage"
	"github.com/Null0Grid/null-drive/shared/api"
)

// newTestServer wires the real stack (SQLite file, disk blobs, handlers)
// against a fresh temp data dir, exactly like main does.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return newTestServerWith(t, nil, nil)
}

// newTestServerWith lets tests control the clock and other options.
func newTestServerWith(t *testing.T, now func() time.Time, opt func(*srvapi.Options)) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "nulldrive.db"))
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
	o := srvapi.Options{
		Store:   meta.New(d, now),
		Blobs:   blobs,
		Hasher:  auth.Hasher{P: auth.TestParams},
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		DataDir: dir,
		Now:     now,
	}
	if opt != nil {
		opt(&o)
	}
	s, err := srvapi.New(o)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	t.Cleanup(func() { ts.Close(); _ = s.Close() })
	return ts
}

type client struct {
	t     *testing.T
	base  string
	token string
}

type resp struct {
	status int
	header http.Header
	body   []byte
}

func (r resp) decode(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
}

func (r resp) errCode(t *testing.T) string {
	t.Helper()
	var e api.ErrorBody
	r.decode(t, &e)
	return e.Error.Code
}

func (c *client) do(method, path string, body io.Reader, headers map[string]string) resp {
	c.t.Helper()
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		c.t.Fatal(err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{status: res.StatusCode, header: res.Header, body: b}
}

func (c *client) json(method, path string, v any) resp {
	c.t.Helper()
	b, _ := json.Marshal(v)
	return c.do(method, path, bytes.NewReader(b), map[string]string{"Content-Type": "application/json"})
}

func (c *client) put(path, content string, headers map[string]string) resp {
	c.t.Helper()
	return c.do(http.MethodPut, "/api/v1/files/"+path, strings.NewReader(content), headers)
}

func want(t *testing.T, r resp, status int) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status = %d, want %d; body: %s", r.status, status, r.body)
	}
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// setupAdmin runs first-run setup and returns a session client.
func setupAdmin(t *testing.T, ts *httptest.Server) *client {
	t.Helper()
	c := &client{t: t, base: ts.URL}
	r := c.json(http.MethodPost, "/api/v1/setup", api.Credentials{Username: "admin", Password: "hunter2hunter2"})
	want(t, r, http.StatusCreated)
	var sr api.SessionResponse
	r.decode(t, &sr)
	return &client{t: t, base: ts.URL, token: sr.Token}
}

// TestEndToEnd is the Phase 1 acceptance flow: setup, register a device,
// upload, list, download, conflict, delete.
func TestEndToEnd(t *testing.T) {
	ts := newTestServer(t)
	anon := &client{t: t, base: ts.URL}

	// Health and setup status before setup.
	r := anon.do(http.MethodGet, "/api/v1/health", nil, nil)
	want(t, r, http.StatusOK)
	var st api.SetupStatusResponse
	anon.do(http.MethodGet, "/api/v1/setup/status", nil, nil).decode(t, &st)
	if st.SetupComplete {
		t.Fatal("fresh server reports setup complete")
	}

	// Setup creates the admin and returns a session.
	web := setupAdmin(t, ts)
	anon.do(http.MethodGet, "/api/v1/setup/status", nil, nil).decode(t, &st)
	if !st.SetupComplete {
		t.Fatal("setup not complete")
	}
	// Setup cannot be repeated.
	r = anon.json(http.MethodPost, "/api/v1/setup", api.Credentials{Username: "evil", Password: "longenough"})
	want(t, r, http.StatusConflict)

	// Login works with the right password only.
	want(t, anon.json(http.MethodPost, "/api/v1/auth/login", api.Credentials{Username: "admin", Password: "wrong-password"}), http.StatusUnauthorized)
	want(t, anon.json(http.MethodPost, "/api/v1/auth/login", api.Credentials{Username: "nobody", Password: "hunter2hunter2"}), http.StatusUnauthorized)
	r = anon.json(http.MethodPost, "/api/v1/auth/login", api.Credentials{Username: "admin", Password: "hunter2hunter2"})
	want(t, r, http.StatusOK)

	// Register a device; the token is returned once.
	r = web.json(http.MethodPost, "/api/v1/devices", api.CreateDeviceRequest{Name: "Desktop PC"})
	want(t, r, http.StatusCreated)
	var dr api.CreateDeviceResponse
	r.decode(t, &dr)
	if !strings.HasPrefix(dr.Token, api.DeviceTokenPrefix) {
		t.Fatalf("device token %q", dr.Token)
	}
	dev := &client{t: t, base: ts.URL, token: dr.Token}

	var me api.MeResponse
	dev.do(http.MethodGet, "/api/v1/me", nil, nil).decode(t, &me)
	if me.Device == nil || me.Device.Name != "Desktop PC" {
		t.Fatalf("me = %+v", me)
	}

	// Remember the feed position before any file exists.
	var list api.FileList
	dev.do(http.MethodGet, "/api/v1/files", nil, nil).decode(t, &list)
	start := list.Version

	// Upload (create) with integrity check.
	content := "hello from null drive"
	r = dev.put("notes/hello.txt", content, map[string]string{api.HeaderContentSHA256: sha(content)})
	want(t, r, http.StatusCreated)
	var f api.File
	r.decode(t, &f)
	if f.SHA256 != sha(content) || f.Size != int64(len(content)) || r.header.Get("ETag") == "" {
		t.Fatalf("uploaded file = %+v etag=%q", f, r.header.Get("ETag"))
	}

	// List shows it, plus the parent folder created on the fly.
	dev.do(http.MethodGet, "/api/v1/files", nil, nil).decode(t, &list)
	if len(list.Files) != 2 || !list.Files[0].IsDir || list.Files[0].Path != "notes" ||
		list.Files[1].Path != "notes/hello.txt" || list.Files[1].IsDir {
		t.Fatalf("list = %+v", list)
	}

	// Download returns the bytes and safe headers.
	r = dev.do(http.MethodGet, "/api/v1/files/notes/hello.txt", nil, nil)
	want(t, r, http.StatusOK)
	if string(r.body) != content {
		t.Fatalf("downloaded %q", r.body)
	}
	if !strings.HasPrefix(r.header.Get("Content-Disposition"), "attachment") ||
		!strings.Contains(r.header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("missing download hardening headers: %v", r.header)
	}

	// Two devices edit from the same base version: the second one conflicts.
	base := r.header.Get("ETag")
	want(t, web.put("notes/hello.txt", "edit from web", map[string]string{"If-Match": base}), http.StatusOK)
	r = dev.put("notes/hello.txt", "edit from desktop", map[string]string{"If-Match": base})
	want(t, r, http.StatusConflict)
	var ce api.ErrorBody
	r.decode(t, &ce)
	if ce.Error.Code != "conflict" || ce.Error.Current == nil || ce.Error.Current.SHA256 != sha("edit from web") {
		t.Fatalf("conflict body = %s", r.body)
	}

	// Overwriting without If-Match is refused rather than silently winning.
	want(t, dev.put("notes/hello.txt", "blind write", nil), http.StatusPreconditionRequired)

	// Delete creates a tombstone that shows up in the change feed.
	r = dev.do(http.MethodDelete, "/api/v1/files/notes/hello.txt", nil, nil)
	want(t, r, http.StatusOK)
	want(t, dev.do(http.MethodGet, "/api/v1/files/notes/hello.txt", nil, nil), http.StatusNotFound)

	dev.do(http.MethodGet, "/api/v1/files?since="+itoa(start), nil, nil).decode(t, &list)
	last := list.Files[len(list.Files)-1]
	if last.Path != "notes/hello.txt" || !last.Deleted {
		t.Fatalf("feed tail = %+v", last)
	}
	var live api.FileList
	dev.do(http.MethodGet, "/api/v1/files", nil, nil).decode(t, &live)
	if len(live.Files) != 1 || live.Files[0].Path != "notes" {
		t.Fatalf("live listing after delete = %+v (folder should remain)", live)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestPathTraversalRejected(t *testing.T) {
	ts := newTestServer(t)
	c := setupAdmin(t, ts)
	for _, p := range []string{
		"../etc/passwd",
		"a/../../b",
		"%2e%2e/secret",
		"a/%2E%2E/%2e%2e/b",
		"..%2fsecret",
		"..%5csecret",
		"%2Fetc%2Fpasswd",
		"C:%5CWindows",
		"a%00b",
		"",
	} {
		t.Run(p, func(t *testing.T) {
			r := c.put(p, "x", nil)
			if r.status != http.StatusBadRequest {
				t.Fatalf("PUT %q status = %d, want 400 (body %s)", p, r.status, r.body)
			}
			if code := r.errCode(t); code != "invalid_path" {
				t.Fatalf("code = %q", code)
			}
		})
	}
	// Move validates both paths too.
	want(t, c.json(http.MethodPost, "/api/v1/files/move", api.MoveRequest{From: "a", To: "../b"}), http.StatusBadRequest)
}

func TestAuthRequired(t *testing.T) {
	ts := newTestServer(t)
	setupAdmin(t, ts)
	for _, tok := range []string{"", "garbage", "nds_notarealtoken", "ndd_notarealtoken"} {
		c := &client{t: t, base: ts.URL, token: tok}
		r := c.do(http.MethodGet, "/api/v1/files", nil, nil)
		want(t, r, http.StatusUnauthorized)
		if r.header.Get("WWW-Authenticate") == "" {
			t.Fatal("missing WWW-Authenticate")
		}
	}
}

func TestCookieSession(t *testing.T) {
	ts := newTestServer(t)
	setupAdmin(t, ts)
	anon := &client{t: t, base: ts.URL}
	r := anon.json(http.MethodPost, "/api/v1/auth/login", api.Credentials{Username: "admin", Password: "hunter2hunter2"})
	want(t, r, http.StatusOK)
	var cookie *http.Cookie
	for _, ck := range (&http.Response{Header: r.header}).Cookies() {
		if ck.Name == api.SessionCookieName {
			cookie = ck
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie = %+v", cookie)
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/me", nil)
	req.AddCookie(cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("cookie auth status = %d", res.StatusCode)
	}
}

func TestLogoutAndRevocation(t *testing.T) {
	ts := newTestServer(t)
	web := setupAdmin(t, ts)

	var dr api.CreateDeviceResponse
	web.json(http.MethodPost, "/api/v1/devices", api.CreateDeviceRequest{Name: "laptop"}).decode(t, &dr)
	dev := &client{t: t, base: ts.URL, token: dr.Token}

	// Device tokens cannot mint more devices or revoke others.
	want(t, dev.json(http.MethodPost, "/api/v1/devices", api.CreateDeviceRequest{Name: "sneaky"}), http.StatusForbidden)
	want(t, dev.do(http.MethodDelete, "/api/v1/devices/1", nil, nil), http.StatusForbidden)

	// Revoking from the web session kills the device token immediately.
	want(t, web.do(http.MethodDelete, "/api/v1/devices/"+itoa(dr.Device.ID), nil, nil), http.StatusOK)
	want(t, dev.do(http.MethodGet, "/api/v1/files", nil, nil), http.StatusUnauthorized)
	want(t, web.do(http.MethodDelete, "/api/v1/devices/999", nil, nil), http.StatusNotFound)

	var dl api.DeviceList
	web.do(http.MethodGet, "/api/v1/devices", nil, nil).decode(t, &dl)
	if len(dl.Devices) != 1 || dl.Devices[0].RevokedAt == nil {
		t.Fatalf("devices = %+v", dl)
	}

	// Logout ends the session.
	want(t, web.do(http.MethodPost, "/api/v1/auth/logout", nil, nil), http.StatusNoContent)
	want(t, web.do(http.MethodGet, "/api/v1/me", nil, nil), http.StatusUnauthorized)
}

func TestUploadEdgeCases(t *testing.T) {
	ts := newTestServer(t)
	c := setupAdmin(t, ts)

	// Integrity check failure stores nothing.
	r := c.put("a.txt", "actual", map[string]string{api.HeaderContentSHA256: sha("expected")})
	want(t, r, http.StatusBadRequest)
	if r.errCode(t) != "hash_mismatch" {
		t.Fatal(string(r.body))
	}
	want(t, c.do(http.MethodGet, "/api/v1/files/a.txt", nil, nil), http.StatusNotFound)

	want(t, c.put("a.txt", "x", map[string]string{api.HeaderContentSHA256: "zzz"}), http.StatusBadRequest)
	want(t, c.put("a.txt", "x", map[string]string{"If-Match": "abc"}), http.StatusBadRequest)
	want(t, c.put("a.txt", "x", map[string]string{"If-Match": `W/"1"`}), http.StatusBadRequest)

	// Create-only (If-Match: 0) succeeds once, then conflicts.
	want(t, c.put("a.txt", "v1", map[string]string{"If-Match": "0"}), http.StatusCreated)
	want(t, c.put("a.txt", "v2", map[string]string{"If-Match": "0"}), http.StatusConflict)

	// Re-uploading identical content is an idempotent 200 that does not
	// bump the version (safe retry after a lost response). The client
	// declares the hash up front so the server can recognise the retry
	// before the body is read; without it, a stale If-Match is a 409.
	var before, after api.FileList
	c.do(http.MethodGet, "/api/v1/files", nil, nil).decode(t, &before)
	want(t, c.put("a.txt", "v1", map[string]string{"If-Match": "12345"}), http.StatusConflict)
	r = c.put("a.txt", "v1", map[string]string{"If-Match": "12345", api.HeaderContentSHA256: sha("v1")})
	want(t, r, http.StatusOK)
	c.do(http.MethodGet, "/api/v1/files", nil, nil).decode(t, &after)
	if before.Version != after.Version {
		t.Fatalf("idempotent re-upload bumped version %d -> %d", before.Version, after.Version)
	}

	// File/folder collisions.
	want(t, c.put("a.txt/inner", "x", nil), http.StatusConflict)

	// Empty files are fine.
	want(t, c.put("empty", "", nil), http.StatusCreated)
	r = c.do(http.MethodGet, "/api/v1/files/empty", nil, nil)
	want(t, r, http.StatusOK)
	if len(r.body) != 0 {
		t.Fatal("empty file not empty")
	}

	// Range requests work (resumable downloads).
	want(t, c.put("big.bin", "0123456789", nil), http.StatusCreated)
	r = c.do(http.MethodGet, "/api/v1/files/big.bin", nil, map[string]string{"Range": "bytes=2-5"})
	want(t, r, http.StatusPartialContent)
	if string(r.body) != "2345" {
		t.Fatalf("range body %q", r.body)
	}

	// Conditional GET.
	etag := r.header.Get("ETag")
	want(t, c.do(http.MethodGet, "/api/v1/files/big.bin", nil, map[string]string{"If-None-Match": etag}), http.StatusNotModified)

	// Unicode and spaces in paths round-trip.
	want(t, c.put("Fotos%202026/%C3%BCber.txt", "u", nil), http.StatusCreated)
	r = c.do(http.MethodGet, "/api/v1/files/Fotos%202026/%C3%BCber.txt", nil, nil)
	want(t, r, http.StatusOK)
}

func TestMoveAndFolderDelete(t *testing.T) {
	ts := newTestServer(t)
	c := setupAdmin(t, ts)
	for _, p := range []string{"docs/a.txt", "docs/sub/b.txt", "other.txt"} {
		want(t, c.put(p, p, nil), http.StatusCreated)
	}

	r := c.json(http.MethodPost, "/api/v1/files/move", api.MoveRequest{From: "docs", To: "archive/docs"})
	want(t, r, http.StatusOK)
	var mr api.MoveResponse
	r.decode(t, &mr)
	if len(mr.Moved) != 4 { // docs, docs/a.txt, docs/sub, docs/sub/b.txt
		t.Fatalf("moved = %+v", mr)
	}
	r = c.do(http.MethodGet, "/api/v1/files/archive/docs/sub/b.txt", nil, nil)
	want(t, r, http.StatusOK)
	if string(r.body) != "docs/sub/b.txt" {
		t.Fatal("content did not follow the move")
	}

	// Moves never overwrite.
	r = c.json(http.MethodPost, "/api/v1/files/move", api.MoveRequest{From: "other.txt", To: "archive/docs/a.txt"})
	want(t, r, http.StatusConflict)
	if r.errCode(t) != "target_exists" {
		t.Fatal(string(r.body))
	}
	// Strict JSON.
	want(t, c.do(http.MethodPost, "/api/v1/files/move", strings.NewReader(`{"from":"a","to":"b","oops":1}`), nil), http.StatusBadRequest)

	// Folder delete tombstones everything inside.
	r = c.do(http.MethodDelete, "/api/v1/files/archive", nil, nil)
	want(t, r, http.StatusOK)
	var del api.DeleteResponse
	r.decode(t, &del)
	if len(del.Deleted) != 5 { // archive, archive/docs(+a.txt), archive/docs/sub(+b.txt)
		t.Fatalf("deleted = %+v", del)
	}
	var list api.FileList
	c.do(http.MethodGet, "/api/v1/files", nil, nil).decode(t, &list)
	if len(list.Files) != 1 || list.Files[0].Path != "other.txt" {
		t.Fatalf("remaining = %+v", list.Files)
	}
	want(t, c.do(http.MethodDelete, "/api/v1/files/archive", nil, nil), http.StatusNotFound)
}

func TestValidation(t *testing.T) {
	ts := newTestServer(t)
	anon := &client{t: t, base: ts.URL}
	want(t, anon.json(http.MethodPost, "/api/v1/setup", api.Credentials{Username: "admin", Password: "short"}), http.StatusBadRequest)
	want(t, anon.json(http.MethodPost, "/api/v1/setup", api.Credentials{Username: "bad name!", Password: "longenough"}), http.StatusBadRequest)
	want(t, anon.do(http.MethodPost, "/api/v1/setup", strings.NewReader("not json"), nil), http.StatusBadRequest)

	c := setupAdmin(t, ts)
	want(t, c.json(http.MethodPost, "/api/v1/devices", api.CreateDeviceRequest{Name: "   "}), http.StatusBadRequest)
	want(t, c.do(http.MethodGet, "/api/v1/files?since=-1", nil, nil), http.StatusBadRequest)
	want(t, c.do(http.MethodGet, "/api/v1/files?since=abc", nil, nil), http.StatusBadRequest)
	want(t, c.do(http.MethodPost, "/api/v1/files/whatever", nil, nil), http.StatusMethodNotAllowed)
	want(t, c.do(http.MethodGet, "/api/v1/nope", nil, nil), http.StatusNotFound)

	r := c.do(http.MethodGet, "/api/v1/health", nil, map[string]string{api.HeaderRequestID: "trace-me"})
	if r.header.Get(api.HeaderRequestID) != "trace-me" || r.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers = %v", r.header)
	}
}

func TestFoldersAPI(t *testing.T) {
	ts := newTestServer(t)
	c := setupAdmin(t, ts)

	r := c.json(http.MethodPost, "/api/v1/folders", api.CreateFolderRequest{Path: "Photos/2026"})
	want(t, r, http.StatusCreated)
	var f api.File
	r.decode(t, &f)
	if !f.IsDir || f.Path != "Photos/2026" {
		t.Fatalf("folder = %+v", f)
	}
	want(t, c.json(http.MethodPost, "/api/v1/folders", api.CreateFolderRequest{Path: "Photos/2026"}), http.StatusOK)
	want(t, c.json(http.MethodPost, "/api/v1/folders", api.CreateFolderRequest{Path: "../x"}), http.StatusBadRequest)

	var list api.FileList
	c.do(http.MethodGet, "/api/v1/files", nil, nil).decode(t, &list)
	if len(list.Files) != 2 || !list.Files[0].IsDir || !list.Files[1].IsDir {
		t.Fatalf("list = %+v", list.Files)
	}

	// Folders and files cannot share a path; folders cannot be downloaded.
	r = c.put("Photos/2026", "x", nil)
	want(t, r, http.StatusConflict)
	if r.errCode(t) != "path_collision" {
		t.Fatal(string(r.body))
	}
	want(t, c.put("Photos/2026/pic.jpg", "jpg", nil), http.StatusCreated)
	r = c.json(http.MethodPost, "/api/v1/folders", api.CreateFolderRequest{Path: "Photos/2026/pic.jpg"})
	want(t, r, http.StatusConflict)
	r = c.do(http.MethodGet, "/api/v1/files/Photos", nil, nil)
	want(t, r, http.StatusBadRequest)
	if r.errCode(t) != "is_folder" {
		t.Fatal(string(r.body))
	}

	// Rename an (empty) folder.
	want(t, c.json(http.MethodPost, "/api/v1/folders", api.CreateFolderRequest{Path: "Empty"}), http.StatusCreated)
	want(t, c.json(http.MethodPost, "/api/v1/files/move", api.MoveRequest{From: "Empty", To: "Still Empty"}), http.StatusOK)
	want(t, c.do(http.MethodDelete, "/api/v1/files/Still%20Empty", nil, nil), http.StatusOK)
}

func TestSystemInfo(t *testing.T) {
	ts := newTestServer(t)
	anon := &client{t: t, base: ts.URL}

	// Public during setup (the wizard shows it before an account exists).
	r := anon.do(http.MethodGet, "/api/v1/system/info", nil, nil)
	want(t, r, http.StatusOK)
	var info api.SystemInfo
	r.decode(t, &info)
	if info.DataDir == "" || info.DiskTotalBytes == 0 {
		t.Fatalf("info = %+v", info)
	}

	// Private afterwards.
	c := setupAdmin(t, ts)
	want(t, anon.do(http.MethodGet, "/api/v1/system/info", nil, nil), http.StatusUnauthorized)
	want(t, c.do(http.MethodGet, "/api/v1/system/info", nil, nil), http.StatusOK)
}

func TestChangePassword(t *testing.T) {
	ts := newTestServer(t)
	web := setupAdmin(t, ts)
	anon := &client{t: t, base: ts.URL}

	// A second session that should be signed out by the change.
	var other api.SessionResponse
	anon.json(http.MethodPost, "/api/v1/auth/login", api.Credentials{Username: "admin", Password: "hunter2hunter2"}).decode(t, &other)
	otherC := &client{t: t, base: ts.URL, token: other.Token}

	var dr api.CreateDeviceResponse
	web.json(http.MethodPost, "/api/v1/devices", api.CreateDeviceRequest{Name: "pc"}).decode(t, &dr)
	dev := &client{t: t, base: ts.URL, token: dr.Token}

	want(t, web.json(http.MethodPost, "/api/v1/auth/password",
		api.ChangePasswordRequest{CurrentPassword: "wrong-password", NewPassword: "new-password-1"}), http.StatusUnauthorized)
	want(t, web.json(http.MethodPost, "/api/v1/auth/password",
		api.ChangePasswordRequest{CurrentPassword: "hunter2hunter2", NewPassword: "short"}), http.StatusBadRequest)
	want(t, dev.json(http.MethodPost, "/api/v1/auth/password",
		api.ChangePasswordRequest{CurrentPassword: "hunter2hunter2", NewPassword: "new-password-1"}), http.StatusForbidden)

	want(t, web.json(http.MethodPost, "/api/v1/auth/password",
		api.ChangePasswordRequest{CurrentPassword: "hunter2hunter2", NewPassword: "new-password-1"}), http.StatusNoContent)

	want(t, web.do(http.MethodGet, "/api/v1/me", nil, nil), http.StatusOK)              // current session kept
	want(t, otherC.do(http.MethodGet, "/api/v1/me", nil, nil), http.StatusUnauthorized) // others ended
	want(t, dev.do(http.MethodGet, "/api/v1/me", nil, nil), http.StatusOK)              // devices unaffected
	want(t, anon.json(http.MethodPost, "/api/v1/auth/login", api.Credentials{Username: "admin", Password: "hunter2hunter2"}), http.StatusUnauthorized)
	want(t, anon.json(http.MethodPost, "/api/v1/auth/login", api.Credentials{Username: "admin", Password: "new-password-1"}), http.StatusOK)
}

func TestUIRouting(t *testing.T) {
	dir := t.TempDir()
	d, _ := db.Open(filepath.Join(dir, "nulldrive.db"))
	t.Cleanup(func() { _ = d.Close() })
	if _, err := db.Migrate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	blobs, _ := storage.NewDisk(dir)
	ui := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("SPA")) })
	s, err := srvapi.New(srvapi.Options{
		Store: meta.New(d, nil), Blobs: blobs, Hasher: auth.Hasher{P: auth.TestParams},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), UI: ui,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	defer ts.Close()
	c := &client{t: t, base: ts.URL}

	for _, p := range []string{"/", "/files/Photos", "/settings", "/login"} {
		r := c.do(http.MethodGet, p, nil, nil)
		if r.status != 200 || string(r.body) != "SPA" {
			t.Errorf("GET %s = %d %q, want the UI", p, r.status, r.body)
		}
	}
	for _, p := range []string{"/api", "/api/", "/api/v2/files", "/api/v1/nope"} {
		r := c.do(http.MethodGet, p, nil, nil)
		if r.status != 404 || r.errCode(t) != "not_found" {
			t.Errorf("GET %s = %d %q, want JSON 404", p, r.status, r.body)
		}
	}
}
