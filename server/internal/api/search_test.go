package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	srvapi "github.com/Null0Grid/null-drive/server/internal/api"
	"github.com/Null0Grid/null-drive/server/internal/auth"
	"github.com/Null0Grid/null-drive/server/internal/db"
	"github.com/Null0Grid/null-drive/server/internal/meta"
	"github.com/Null0Grid/null-drive/server/internal/search/searchtest"
	"github.com/Null0Grid/null-drive/server/storage"
	"github.com/Null0Grid/null-drive/shared/api"
)

const testSearchOrigin = "https://search.nullgrid.dev"

type searchEnv struct {
	t      *testing.T
	base   string
	idx    string // index directory
	store  *meta.Store
	alice  *client // the admin
	bob    *client // a second account
	ids    map[*client]int64
	hasher auth.Hasher
}

func newSearchEnv(t *testing.T) *searchEnv {
	t.Helper()
	e := &searchEnv{t: t, ids: map[*client]int64{}}
	ts := newTestServerWith(t, nil, func(o *srvapi.Options) {
		e.idx = filepath.Join(o.DataDir, "indexes")
		o.SearchDir = e.idx
		o.SearchOrigins = []string{testSearchOrigin}
		e.store, e.hasher = o.Store, o.Hasher
	})
	e.base = ts.URL
	e.alice = setupAdmin(t, ts)
	e.bob = e.addUser("bob", "bob-password-123")
	for _, c := range []*client{e.alice, e.bob} {
		var me api.MeResponse
		c.do(http.MethodGet, "/api/v1/me", nil, nil).decode(t, &me)
		e.ids[c] = me.User.ID
	}
	return e
}

// addUser creates an account (as `nulldrive add-user` does) and logs in.
func (e *searchEnv) addUser(name, pw string) *client {
	e.t.Helper()
	h, err := e.hasher.Hash(pw)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.store.CreateUser(context.Background(), name, h); err != nil {
		e.t.Fatal(err)
	}
	anon := &client{t: e.t, base: e.base}
	r := anon.json(http.MethodPost, "/api/v1/auth/login", api.Credentials{Username: name, Password: pw})
	want(e.t, r, http.StatusOK)
	var sr api.SessionResponse
	r.decode(e.t, &sr)
	return &client{t: e.t, base: e.base, token: sr.Token}
}

func (e *searchEnv) upload(c *client, path string, content []byte, ifMatch ...int64) int64 {
	e.t.Helper()
	hdr := map[string]string{}
	if len(ifMatch) > 0 {
		hdr["If-Match"] = strconv.FormatInt(ifMatch[0], 10)
	}
	r := c.do(http.MethodPut, "/api/v1/files/"+path, bytes.NewReader(content), hdr)
	if r.status != http.StatusCreated && r.status != http.StatusOK {
		e.t.Fatalf("upload %s: %d %s", path, r.status, r.body)
	}
	var f api.File
	r.decode(e.t, &f)
	return f.Version
}

func searchAs(t *testing.T, c *client, q string, extra ...string) []api.SearchResult {
	t.Helper()
	u := "/search?q=" + url.QueryEscape(q) + strings.Join(extra, "")
	r := c.do(http.MethodGet, u, nil, nil)
	want(t, r, http.StatusOK)
	if !bytes.HasPrefix(bytes.TrimSpace(r.body), []byte("[")) {
		t.Fatalf("search %q: not a JSON array: %s", q, r.body)
	}
	var out []api.SearchResult
	r.decode(t, &out)
	return out
}

func paths(rs []api.SearchResult) string {
	var p []string
	for _, r := range rs {
		p = append(p, r.Path)
	}
	return strings.Join(p, ",")
}

// TestSearchIsPerUser is the phase's acceptance test: each user finds their
// own files by name and by content (.md, .txt, .pdf) and never the other's,
// and each user's index is a separate file.
func TestSearchIsPerUser(t *testing.T) {
	e := newSearchEnv(t)
	alice, bob := e.alice, e.bob

	e.upload(alice, "notes/Libre AI plan.md", []byte("---\nid: 11111111-2222-3333-4444-555555555555\n---\nRoadmap: the zebra migration ships in March.\n"))
	e.upload(alice, "todo.txt", []byte("buy oat milk\ncall the zebra vet"))
	e.upload(alice, "invoices/acme.pdf", searchtest.PDF("Invoice 2026-114", "Acme Rockets, payable in 30 days"))
	e.upload(alice, "photos/LibreAI-logo.png", []byte("\x89PNG not really"))

	e.upload(bob, "LibreAI notes.md", []byte("Bob's private zebra diary"))
	e.upload(bob, "taxes.txt", []byte("bob secret: invoice for the boat"))
	e.upload(bob, "scan.pdf", searchtest.PDF("Bob's mortgage statement"))

	cases := []struct {
		who   *client
		q     string
		paths string // exact, in rank order
	}{
		// Content of each type.
		{alice, "zebra", "notes/Libre AI plan.md,todo.txt"},
		{alice, "Acme rockets", "invoices/acme.pdf"},
		{alice, "oat milk", "todo.txt"},
		{bob, "zebra", "LibreAI notes.md"},
		{bob, "mortgage", "scan.pdf"},
		// Filenames, case-insensitive, across separators, any extension.
		{alice, "libre ai", "photos/LibreAI-logo.png,notes/Libre AI plan.md"},
		{alice, "LIBREAI", "notes/Libre AI plan.md,photos/LibreAI-logo.png"},
		{alice, "acme", "invoices/acme.pdf"},
		{bob, "libre-ai", "LibreAI notes.md"},
		// Neither sees the other's files, by name or by content.
		{alice, "mortgage", ""},
		{alice, "diary", ""},
		{alice, "taxes", ""},
		{bob, "oat milk", ""}, // ("oat" alone matches Bob's "boat": substring matching)
		{bob, "Acme", ""},
		{bob, "roadmap", ""},
		{bob, "plan", ""},
		// Short terms (no trigrams) still work, ANDed with the rest.
		{alice, "oat vet", "todo.txt"},
		{alice, "30 acme", "invoices/acme.pdf"},
		{alice, "", ""},
	}
	for _, c := range cases {
		got := searchAs(t, c.who, c.q)
		// Ties in rank order (same score) aren't part of the contract:
		// compare as sets when more than one result is expected.
		g, w := strings.Split(paths(got), ","), strings.Split(c.paths, ",")
		if !sameSet(g, w) {
			t.Errorf("user %d, q=%q: got [%s], want [%s]", e.ids[c.who], c.q, paths(got), c.paths)
		}
	}

	// Result shape (contract v1).
	rs := searchAs(t, alice, "zebra migration")
	if len(rs) != 1 {
		t.Fatalf("zebra migration: %+v", rs)
	}
	r := rs[0]
	if r.Source != "drive" || r.Title != "Libre AI plan.md" || r.Path != "notes/Libre AI plan.md" ||
		len(r.ID) != 32 || r.Open != "nulldrive://open?id="+r.ID ||
		r.SyncedID == nil || *r.SyncedID != "11111111-2222-3333-4444-555555555555" ||
		!strings.Contains(r.Snippet, "zebra migration ships") || strings.Contains(r.Snippet, "id:") {
		t.Fatalf("result: %+v", r)
	}
	if _, err := time.Parse(time.RFC3339, r.Modified); err != nil {
		t.Fatalf("modified %q: %v", r.Modified, err)
	}
	raw := alice.do(http.MethodGet, "/search?q=oat+milk", nil, nil) // a .txt: no note id
	if !bytes.Contains(raw.body, []byte(`"synced_id":null`)) || raw.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("raw: %s %v", raw.body, raw.header)
	}
	// The file listing shows the same id.
	var fl api.FileList
	alice.do(http.MethodGet, "/api/v1/files", nil, nil).decode(t, &fl)
	found := false
	for _, f := range fl.Files {
		found = found || (f.Path == r.Path && f.ID == r.ID)
	}
	if !found {
		t.Fatal("search id doesn't match the file listing's id")
	}

	// One index file per user, named by user id, holding only their text.
	for c, other := range map[*client]string{alice: "mortgage", bob: "Acme Rockets"} {
		p := filepath.Join(e.idx, strconv.FormatInt(e.ids[c], 10)+".db")
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("index file: %v", err)
		}
		for _, suffix := range []string{"", "-wal"} {
			b, _ := os.ReadFile(p + suffix)
			if bytes.Contains(b, []byte(other)) {
				t.Fatalf("%s holds another user's text %q", filepath.Base(p+suffix), other)
			}
		}
	}

	// Limit.
	if got := searchAs(t, alice, "a", "&limit=1"); len(got) != 1 {
		t.Fatalf("limit=1: %d results", len(got))
	}
	want(t, alice.do(http.MethodGet, "/search?q=a&limit=0", nil, nil), http.StatusBadRequest)
	want(t, alice.do(http.MethodGet, "/search?q=a&limit=x", nil, nil), http.StatusBadRequest)
	// The API-prefixed alias.
	want(t, alice.do(http.MethodGet, "/api/v1/search?q=zebra", nil, nil), http.StatusOK)
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		if m[s] == 0 {
			return false
		}
		m[s]--
	}
	return true
}

func TestSearchFollowsChanges(t *testing.T) {
	e := newSearchEnv(t)
	c := e.alice
	v := e.upload(c, "draft.md", []byte("first version about penguins"))
	rs := searchAs(t, c, "penguins")
	if len(rs) != 1 {
		t.Fatalf("%+v", rs)
	}
	id := rs[0].ID

	// Overwrite: new text found, old text gone, same id.
	e.upload(c, "draft.md", []byte("second version about walruses"), v)
	if got := searchAs(t, c, "penguins"); len(got) != 0 {
		t.Fatalf("stale content: %+v", got)
	}
	if got := searchAs(t, c, "walruses"); len(got) != 1 || got[0].ID != id {
		t.Fatalf("after overwrite: %+v (id %s)", got, id)
	}
	// Rename/move: new path, same id, content still searchable.
	want(t, c.json(http.MethodPost, "/api/v1/files/move", api.MoveRequest{From: "draft.md", To: "final/report.md"}), http.StatusOK)
	got := searchAs(t, c, "walruses")
	if len(got) != 1 || got[0].ID != id || got[0].Path != "final/report.md" || got[0].Title != "report.md" {
		t.Fatalf("after move: %+v", got)
	}
	if got := searchAs(t, c, "draft"); len(got) != 0 {
		t.Fatalf("old name still found: %+v", got)
	}
	// Delete: gone.
	want(t, c.do(http.MethodDelete, "/api/v1/files/final/report.md", nil, nil), http.StatusOK)
	if got := searchAs(t, c, "walruses"); len(got) != 0 {
		t.Fatalf("deleted file found: %+v", got)
	}

	// The index file is private to the server's user.
	idx := filepath.Join(e.idx, strconv.FormatInt(e.ids[c], 10)+".db")
	st, err := os.Stat(idx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		t.Fatalf("index file mode %v", st.Mode().Perm())
	}
}

func TestSearchNullNotesVault(t *testing.T) {
	e := newSearchEnv(t)
	c := e.alice
	noteID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	txtID := "12345678-1234-1234-1234-123456789abc"
	e.upload(c, "Vault/Ideas.md", []byte("---\nid: "+noteID+"\n---\nquokka idea"))
	e.upload(c, "Vault/sub/list.txt", []byte("quokka list"))
	ids, _ := json.Marshal(map[string]string{"sub/list.txt": txtID})
	e.upload(c, "Vault/.nullnotes/ids.json", ids)
	e.upload(c, "Vault/Secret.md.lock", []byte("NNLOCK1\n\x01\x02quokka"))
	e.upload(c, "Vault/Ideas.md.nntmp", []byte("quokka temp"))
	// A Markdown note keeps its id outside a vault too (a backup can put
	// notes anywhere); a .txt there has no ids.json to look in.
	otherID := "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"
	e.upload(c, "Elsewhere/other.md", []byte("---\nid: "+otherID+"\n---\nquokka elsewhere"))
	e.upload(c, "Elsewhere/plain.txt", []byte("quokka plain"))

	got := searchAs(t, c, "quokka")
	byPath := map[string]api.SearchResult{}
	for _, r := range got {
		byPath[r.Path] = r
	}
	if len(got) != 4 {
		t.Fatalf("want Ideas.md, list.txt, other.md, plain.txt; got [%s]", paths(got))
	}
	if s := byPath["Vault/Ideas.md"].SyncedID; s == nil || *s != noteID {
		t.Fatalf("markdown synced_id: %v", s)
	}
	if s := byPath["Vault/sub/list.txt"].SyncedID; s == nil || *s != txtID {
		t.Fatalf("txt synced_id: %v", s)
	}
	if s := byPath["Elsewhere/other.md"].SyncedID; s == nil || *s != otherID {
		t.Fatalf("markdown outside a vault: synced_id %v", s)
	}
	if s := byPath["Elsewhere/plain.txt"].SyncedID; s != nil {
		t.Fatalf("txt outside a vault: synced_id %v", *s)
	}
	if got := searchAs(t, c, "ids"); len(got) != 0 {
		t.Fatalf(".nullnotes data is not a result: %+v", got)
	}
	if got := searchAs(t, c, "secret"); len(got) != 0 {
		t.Fatalf("locked note found: %+v", got)
	}
}

func TestSearchAuthScopeAndCORS(t *testing.T) {
	e := newSearchEnv(t)
	e.upload(e.alice, "a.txt", []byte("aardvark"))

	// No or bad token.
	anon := &client{t: t, base: e.base}
	want(t, anon.do(http.MethodGet, "/search?q=aardvark", nil, nil), http.StatusUnauthorized)
	want(t, (&client{t: t, base: e.base, token: "ndd_nope"}).do(http.MethodGet, "/search?q=a", nil, nil), http.StatusUnauthorized)

	// A search-only token (what Null Search stores) can search and say
	// whose it is, and nothing else.
	r := e.alice.json(http.MethodPost, "/api/v1/devices", api.CreateDeviceRequest{Name: "Null Search", Scope: "search"})
	want(t, r, http.StatusCreated)
	var dr api.CreateDeviceResponse
	r.decode(t, &dr)
	if dr.Device.Scope != "search" {
		t.Fatalf("scope %q", dr.Device.Scope)
	}
	tok := &client{t: t, base: e.base, token: dr.Token}
	if got := searchAs(t, tok, "aardvark"); len(got) != 1 {
		t.Fatalf("search token: %+v", got)
	}
	want(t, tok.do(http.MethodGet, "/api/v1/me", nil, nil), http.StatusOK)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/files"},
		{http.MethodGet, "/api/v1/files/a.txt"},
		{http.MethodPut, "/api/v1/files/b.txt"},
		{http.MethodDelete, "/api/v1/files/a.txt"},
		{http.MethodGet, "/api/v2/files/a.txt"},
		{http.MethodGet, "/api/v1/devices"},
		{http.MethodGet, "/api/v1/ws"},
		{http.MethodPost, "/api/v1/auth/logout"},
	} {
		r := tok.do(c.method, c.path, strings.NewReader("x"), nil)
		if r.status != http.StatusForbidden || r.errCode(t) != "insufficient_scope" {
			t.Fatalf("%s %s with a search token: %d %s", c.method, c.path, r.status, r.body)
		}
	}
	want(t, e.alice.json(http.MethodPost, "/api/v1/devices", api.CreateDeviceRequest{Name: "x", Scope: "admin"}), http.StatusBadRequest)
	// A normal device token can search too.
	if got := searchAs(t, registerDevice(t, e.alice, "laptop"), "aardvark"); len(got) != 1 {
		t.Fatal("device token search")
	}

	// CORS preflight from Null Search, with Private Network Access.
	pre := anon.do(http.MethodOptions, "/search?q=x", nil, map[string]string{
		"Origin": testSearchOrigin, "Access-Control-Request-Method": "GET",
		"Access-Control-Request-Headers": "authorization", "Access-Control-Request-Private-Network": "true",
	})
	want(t, pre, http.StatusNoContent)
	h := pre.header
	if h.Get("Access-Control-Allow-Origin") != testSearchOrigin || !strings.Contains(h.Get("Access-Control-Allow-Headers"), "Authorization") ||
		h.Get("Access-Control-Allow-Private-Network") != "true" || h.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("preflight headers: %v", h)
	}
	// The real request.
	got := tok.do(http.MethodGet, "/search?q=aardvark", nil, map[string]string{"Origin": testSearchOrigin})
	want(t, got, http.StatusOK)
	if got.header.Get("Access-Control-Allow-Origin") != testSearchOrigin || !strings.Contains(strings.Join(got.header.Values("Vary"), ","), "Origin") {
		t.Fatalf("CORS on GET: %v", got.header)
	}
	// Other origins: no.
	evil := "https://evil.example"
	want(t, anon.do(http.MethodOptions, "/search", nil, map[string]string{"Origin": evil, "Access-Control-Request-Method": "GET"}), http.StatusForbidden)
	if got := tok.do(http.MethodGet, "/search?q=a", nil, map[string]string{"Origin": evil}); got.header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("ACAO for an unlisted origin")
	}
	// CORS applies to search only.
	if got := e.alice.do(http.MethodGet, "/api/v1/files", nil, map[string]string{"Origin": testSearchOrigin}); got.header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("CORS leaked to the files API")
	}
}

func TestSearchE2EEDropsIndex(t *testing.T) {
	e := newSearchEnv(t)
	e.upload(e.alice, "diary.md", []byte("platypus confessions"))
	if got := searchAs(t, e.alice, "platypus"); len(got) != 1 {
		t.Fatal("before")
	}
	idx := filepath.Join(e.idx, strconv.FormatInt(e.ids[e.alice], 10)+".db")
	if _, err := os.Stat(idx); err != nil {
		t.Fatal(err)
	}
	good := api.E2EEEnableRequest{Bundle: []byte(`{"version":1,"fingerprint":"0123456789abcdef"}`), Fingerprint: "0123456789abcdef", AuthKey: strings.Repeat("a", 64)}
	want(t, e.alice.json(http.MethodPost, "/api/v2/e2ee", good), http.StatusCreated)
	for _, s := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(idx + s); !os.IsNotExist(err) {
			t.Fatalf("index%s still there after enabling encryption: %v", s, err)
		}
	}
	r := e.alice.do(http.MethodGet, "/search?q=platypus", nil, nil)
	want(t, r, http.StatusOK)
	if strings.TrimSpace(string(r.body)) != "[]" || r.header.Get("X-NullDrive-Search") != "encrypted" {
		t.Fatalf("encrypted account: %s %v", r.body, r.header)
	}
	if _, err := os.Stat(idx); !os.IsNotExist(err) {
		t.Fatal("search recreated the index of an encrypted account")
	}
	// Bob is unaffected.
	e.upload(e.bob, "b.txt", []byte("platypus"))
	if got := searchAs(t, e.bob, "platypus"); len(got) != 1 {
		t.Fatal("other user")
	}
}

func TestSearchDisabled(t *testing.T) {
	ts := newTestServer(t)
	c := setupAdmin(t, ts)
	r := c.do(http.MethodGet, "/search?q=x", nil, nil)
	want(t, r, http.StatusServiceUnavailable)
	if r.errCode(t) != "search_disabled" {
		t.Fatal(string(r.body))
	}
}

// startOn runs a server on an existing data directory (a restart). stop
// shuts it down completely (also run at cleanup; safe to call twice).
func startOn(t *testing.T, dir string) (url string, stop func()) {
	t.Helper()
	d, err := db.Open(filepath.Join(dir, "nulldrive.db"))
	if err != nil {
		t.Fatal(err)
	}
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
		SearchDir: filepath.Join(dir, "indexes"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	var once sync.Once
	stop = func() { once.Do(func() { ts.Close(); _ = s.Close(); _ = d.Close() }) }
	t.Cleanup(stop)
	return ts.URL, stop
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSearchIndexSurvivesRestartAndRebuilds(t *testing.T) {
	dir := t.TempDir()
	base, stop := startOn(t, dir)
	c := setupAdmin(t, &httptest.Server{URL: base})
	up := func(p, body string) {
		r := c.do(http.MethodPut, "/api/v1/files/"+p, strings.NewReader(body), nil)
		if r.status != http.StatusCreated {
			t.Fatalf("upload: %d %s", r.status, r.body)
		}
	}
	restart := func() {
		stop()
		c.base, stop = startOn(t, dir)
	}
	up("one.txt", "lemur")
	if got := searchAs(t, c, "lemur"); len(got) != 1 {
		t.Fatal("first run")
	}
	stop()
	backup := filepath.Join(t.TempDir(), "nulldrive.db")
	copyFile(t, filepath.Join(dir, "nulldrive.db"), backup) // closed: the WAL is checkpointed

	// Restart: changes made while the index was closed are picked up.
	c.base, stop = startOn(t, dir)
	up("two.txt", "lemur again")
	if got := searchAs(t, c, "lemur"); len(got) != 2 {
		t.Fatalf("after restart: [%s]", paths(got))
	}

	// A deleted index is rebuilt from scratch...
	stop()
	idx := filepath.Join(dir, "indexes", "1.db")
	for _, sfx := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(idx + sfx)
	}
	c.base, stop = startOn(t, dir)
	if got := searchAs(t, c, "lemur"); len(got) != 2 {
		t.Fatalf("after deleting the index: [%s]", paths(got))
	}

	// ...and so is one from another schema version...
	stop()
	d, err := db.Open(idx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE meta SET value = '0' WHERE key = 'schema'`); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	c.base, stop = startOn(t, dir)
	if got := searchAs(t, c, "lemur"); len(got) != 2 {
		t.Fatalf("after a schema change: [%s]", paths(got))
	}

	// ...and one newer than the database (the database was restored from a
	// backup, the index wasn't): two.txt doesn't exist any more.
	stop()
	for _, sfx := range []string{"-wal", "-shm"} {
		_ = os.Remove(filepath.Join(dir, "nulldrive.db"+sfx))
	}
	copyFile(t, backup, filepath.Join(dir, "nulldrive.db"))
	restart()
	if got := searchAs(t, c, "lemur"); len(got) != 1 || got[0].Path != "one.txt" {
		t.Fatalf("after restoring an older database: [%s]", paths(got))
	}
}

func TestFileByID(t *testing.T) {
	e := newSearchEnv(t)
	e.upload(e.alice, "docs/plan.md", []byte("x"))
	rs := searchAs(t, e.alice, "plan")
	if len(rs) != 1 {
		t.Fatal(rs)
	}
	id := rs[0].ID
	get := func(c *client, id string) (api.File, resp) {
		r := c.do(http.MethodGet, "/api/v2/ids/"+id, nil, nil)
		var f api.File
		if r.status == http.StatusOK {
			r.decode(t, &f)
		}
		return f, r
	}
	if f, r := get(e.alice, id); r.status != http.StatusOK || f.Path != "docs/plan.md" || f.ID != id || f.Deleted {
		t.Fatalf("lookup: %d %+v", r.status, f)
	}
	// Upper case is accepted (links typed by hand).
	if _, r := get(e.alice, strings.ToUpper(id)); r.status != http.StatusOK {
		t.Fatalf("upper case: %d", r.status)
	}
	want(t, e.alice.json(http.MethodPost, "/api/v1/files/move", api.MoveRequest{From: "docs", To: "archive/docs"}), http.StatusOK)
	if f, _ := get(e.alice, id); f.Path != "archive/docs/plan.md" {
		t.Fatalf("after moving the folder: %+v", f)
	}
	// Another user's id is "not found", same as a missing one.
	if _, r := get(e.bob, id); r.status != http.StatusNotFound {
		t.Fatalf("other user: %d", r.status)
	}
	if _, r := get(e.alice, strings.Repeat("0", 32)); r.status != http.StatusNotFound {
		t.Fatalf("missing: %d", r.status)
	}
	for _, bad := range []string{"x", strings.Repeat("g", 32), "row-1", strings.Repeat("a", 33)} {
		if _, r := get(e.alice, bad); r.status != http.StatusBadRequest {
			t.Fatalf("%q: %d", bad, r.status)
		}
	}
	// Deleted: still resolvable, marked deleted.
	want(t, e.alice.do(http.MethodDelete, "/api/v1/files/archive/docs/plan.md", nil, nil), http.StatusOK)
	if f, r := get(e.alice, id); r.status != http.StatusOK || !f.Deleted {
		t.Fatalf("deleted: %d %+v", r.status, f)
	}
	// A search-only token can't use it (it reveals paths without a query).
	r := e.alice.json(http.MethodPost, "/api/v1/devices", api.CreateDeviceRequest{Name: "s", Scope: "search"})
	var dr api.CreateDeviceResponse
	r.decode(t, &dr)
	if _, r := get(&client{t: t, base: e.base, token: dr.Token}, id); r.status != http.StatusForbidden {
		t.Fatalf("search token: %d", r.status)
	}
}
