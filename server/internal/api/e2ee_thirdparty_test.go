package api_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	srvapi "github.com/Null0Grid/null-drive/server/internal/api"
	"github.com/Null0Grid/null-drive/shared/api"
	"github.com/Null0Grid/null-drive/shared/e2ee"
)

// TestNotesE2EEFlow is docs/NOTES-E2EE.md as a test: a third-party client
// (Null Notes) syncing into an end-to-end encrypted account over raw HTTP,
// using only the e2ee package's primitives (keys, names, chunks), never
// the desktop client's transfer code. scripts/e2ee-reference runs the same
// flow from Python, written from the doc alone.

// e2eeClient is what a third-party client needs: a token, the unlocked
// keys, and the X-NullDrive-E2EE header on every request.
type e2eeClient struct {
	*client
	k *e2ee.Keys
}

func (c *e2eeClient) do(method, path string, body []byte, h map[string]string) resp {
	c.t.Helper()
	hh := map[string]string{api.HeaderE2EE: "1"}
	for k, v := range h {
		hh[k] = v
	}
	var r *bytes.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	if r == nil {
		return c.client.do(method, path, nil, hh)
	}
	return c.client.do(method, path, r, hh)
}

func (c *e2eeClient) enc(p string) string {
	c.t.Helper()
	e, err := c.k.EncryptPath(p)
	if err != nil {
		c.t.Fatal(err)
	}
	return e
}

func escSegs(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// upload: seal, check, upload missing chunks, commit. Any chunking is
// valid; 1 KiB here so notes span several chunks.
func (c *e2eeClient) upload(p string, content []byte, mtime string, h map[string]string) resp {
	c.t.Helper()
	const size = 1024
	var refs []api.ChunkRef
	blobs := map[string][]byte{}
	for i := 0; i < len(content); i += size {
		sealed := c.k.SealChunk(content[i:min(i+size, len(content))])
		sum := sha256.Sum256(sealed)
		hs := hex.EncodeToString(sum[:])
		refs = append(refs, api.ChunkRef{Hash: hs, Size: int64(len(sealed))})
		blobs[hs] = sealed
	}
	if len(refs) > 0 {
		hashes := make([]string, 0, len(refs))
		for _, r := range refs {
			hashes = append(hashes, r.Hash)
		}
		b, _ := json.Marshal(api.ChunkCheckRequest{Hashes: hashes})
		r := c.do(http.MethodPost, "/api/v2/chunks/check", b, map[string]string{"Content-Type": "application/json"})
		want(c.t, r, http.StatusOK)
		var miss api.ChunkCheckResponse
		r.decode(c.t, &miss)
		for _, hs := range miss.Missing {
			want(c.t, c.do(http.MethodPut, "/api/v2/chunks/"+hs, blobs[hs], nil), http.StatusCreated)
		}
	}
	sum := sha256.Sum256(content)
	body := map[string]any{"size": len(content), "sha256": c.k.FileHash(sum[:]), "chunks": refs}
	if refs == nil {
		body["chunks"] = []api.ChunkRef{}
	}
	if mtime != "" {
		body["mtime"] = mtime
	}
	b, _ := json.Marshal(body)
	return c.do(http.MethodPut, "/api/v2/files/"+escSegs(c.enc(p)), b, h)
}

func (c *e2eeClient) download(p string) ([]byte, api.Manifest) {
	c.t.Helper()
	r := c.do(http.MethodGet, "/api/v2/files/"+escSegs(c.enc(p))+"/manifest", nil, nil)
	want(c.t, r, http.StatusOK)
	var m api.Manifest
	r.decode(c.t, &m)
	var out []byte
	for _, ch := range m.Chunks {
		r := c.do(http.MethodGet, "/api/v2/chunks/"+ch.Hash, nil, nil)
		want(c.t, r, http.StatusOK)
		pt, err := c.k.OpenChunk(r.body)
		if err != nil {
			c.t.Fatal(err)
		}
		out = append(out, pt...)
	}
	sum := sha256.Sum256(out)
	if int64(len(out)) != m.Size || c.k.FileHash(sum[:]) != m.SHA256 {
		c.t.Fatalf("downloaded content doesn't match its manifest")
	}
	return out, m
}

// list decrypts every row; rows that don't decrypt (migration leftovers)
// come back under "?plain:<path>".
func (c *e2eeClient) list(prefix string, since *int64) (int64, map[string]api.File) {
	c.t.Helper()
	q := url.Values{}
	if prefix != "" {
		q.Set("prefix", c.enc(prefix))
	}
	if since != nil {
		q.Set("since", strconv.FormatInt(*since, 10))
	}
	r := c.do(http.MethodGet, "/api/v1/files?"+q.Encode(), nil, nil)
	want(c.t, r, http.StatusOK)
	var fl api.FileList
	r.decode(c.t, &fl)
	out := map[string]api.File{}
	for _, f := range fl.Files {
		p, err := c.k.DecryptPath(f.Path)
		if err != nil {
			out["?plain:"+f.Path] = f
			continue
		}
		out[p] = f
	}
	return fl.Version, out
}

func TestNotesE2EEFlow(t *testing.T) {
	ts := newTestServerWith(t, nil, func(o *srvapi.Options) {
		o.SearchDir = filepath.Join(o.DataDir, "indexes")
		o.SearchSyncWait = testSearchSyncWait
	})
	admin := setupAdmin(t, ts)
	// A plaintext file from before encryption: the account is "migrating"
	// until the desktop app re-uploads or purges it.
	want(t, admin.put("old.txt", "from before", nil), http.StatusCreated)

	// The desktop app turns encryption on (fast KDF parameters for tests).
	bundle, _, recovery, err := e2ee.Enable("correct horse battery staple", e2ee.KDF{Alg: "argon2id", Time: 1, MemoryKiB: 8192, Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	bj, _ := json.Marshal(bundle)
	want(t, admin.json(http.MethodPost, "/api/v2/e2ee", api.E2EEEnableRequest{Bundle: bj, Fingerprint: bundle.Fingerprint, AuthKey: mustUnlock(t, bj, "correct horse battery staple").AuthKey()}), http.StatusCreated)

	// Null Notes gets a device token, like any other client.
	r := admin.json(http.MethodPost, "/api/v1/devices", api.CreateDeviceRequest{Name: "Null Notes"})
	want(t, r, http.StatusCreated)
	var dev api.CreateDeviceResponse
	r.decode(t, &dev)
	raw := &client{t: t, base: admin.base, token: dev.Token}

	// 1. Account key: GET /api/v2/e2ee, unlock the bundle locally.
	r = raw.do(http.MethodGet, "/api/v2/e2ee", nil, nil)
	want(t, r, http.StatusOK)
	var st api.E2EEStatus
	r.decode(t, &st)
	if st.State != api.E2EEMigrating || st.PlaintextFiles != 1 || st.Fingerprint != bundle.Fingerprint || st.EnabledAt == nil {
		t.Fatalf("status %+v", st)
	}
	k := mustUnlock(t, st.Bundle, "correct horse battery staple")
	var b e2ee.Bundle
	_ = json.Unmarshal(st.Bundle, &b)
	if kr, err := b.UnlockRecovery(recovery); err != nil || !bytes.Equal(kr.Master(), k.Master()) {
		t.Fatal("recovery key unlock")
	}
	if _, err := b.UnlockPassphrase("wrong passphrase"); err == nil {
		t.Fatal("wrong passphrase unlocked")
	}
	c := &e2eeClient{client: raw, k: k}

	// 2. What the server refuses.
	for _, tc := range []struct {
		r    resp
		code string
	}{
		{raw.do(http.MethodGet, "/api/v1/files", nil, nil), "e2ee_client_required"},
		{raw.do(http.MethodGet, "/api/v2/files/"+escSegs(c.enc("a.md"))+"/manifest", nil, nil), "e2ee_client_required"},
		{c.do(http.MethodPut, "/api/v1/files/a.md", []byte("x"), map[string]string{"If-None-Match": "*"}), "e2ee_required"},
		{c.do(http.MethodGet, "/api/v1/files/old.txt", nil, nil), "e2ee_required"},
	} {
		if tc.r.status != http.StatusConflict || tc.r.errCode(t) != tc.code {
			t.Fatalf("want 409 %s, got %d %s", tc.code, tc.r.status, tc.r.body)
		}
	}
	plainCommit, _ := json.Marshal(api.CommitRequest{Size: 0, SHA256: strings.Repeat("0", 64), Chunks: []api.ChunkRef{}})
	if r := c.do(http.MethodPut, "/api/v2/files/Null%20Notes/plain.md", plainCommit, nil); r.status != http.StatusConflict || r.errCode(t) != "e2ee_path_required" {
		t.Fatalf("plaintext name: %d %s", r.status, r.body)
	}
	mv, _ := json.Marshal(api.MoveRequest{From: "old.txt", To: c.enc("old.txt")})
	if r := c.do(http.MethodPost, "/api/v1/files/move", mv, nil); r.status != http.StatusConflict || r.errCode(t) != "e2ee_path_required" {
		t.Fatalf("plaintext move source: %d %s", r.status, r.body)
	}
	fold, _ := json.Marshal(api.CreateFolderRequest{Path: "Null Notes"})
	if r := c.do(http.MethodPost, "/api/v1/folders", fold, nil); r.status != http.StatusConflict || r.errCode(t) != "e2ee_path_required" {
		t.Fatalf("plaintext folder: %d %s", r.status, r.body)
	}
	fold, _ = json.Marshal(api.CreateFolderRequest{Path: c.enc("Null Notes/Work")})
	want(t, c.do(http.MethodPost, "/api/v1/folders", fold, nil), http.StatusCreated)

	// 3. Create-only upload of a multi-chunk note, with mtime.
	root := "Null Notes/Work"
	note := []byte("---\nid: " + noteUUID + "\n---\n# Plan\n" + strings.Repeat("Ship the encrypted sync. ", 100))
	r = c.upload(root+"/Plan.md", note, "2026-10-18T08:00:00Z", map[string]string{"If-None-Match": "*"})
	want(t, r, http.StatusCreated)
	var f1 api.File
	r.decode(t, &f1)
	if f1.Path != c.enc(root+"/Plan.md") || f1.Size != int64(len(note)) || f1.SyncedID != "" || f1.Mtime == nil || len(f1.ID) != 32 {
		t.Fatalf("created: %+v", f1)
	}
	sum := sha256.Sum256(note)
	if f1.SHA256 != k.FileHash(sum[:]) {
		t.Fatal("sha256 is stored as sent (the keyed file hash)")
	}
	if r.header.Get("ETag") != `"`+itoa(f1.Version)+`"` {
		t.Fatalf("etag %q", r.header.Get("ETag"))
	}
	// Create-only again: 409 with the current file. Same content: 200 no-op
	// (sealing is deterministic, so a retry is recognised).
	r = c.upload(root+"/Plan.md", []byte("other"), "", map[string]string{"If-None-Match": "*"})
	if r.status != http.StatusConflict || r.errCode(t) != "conflict" || !strings.Contains(string(r.body), f1.ID) {
		t.Fatalf("create-only on existing: %d %s", r.status, r.body)
	}
	r = c.upload(root+"/Plan.md", note, "", map[string]string{"If-None-Match": "*"})
	want(t, r, http.StatusOK)
	// No precondition on an existing file: 428, as on plain accounts.
	want(t, c.upload(root+"/Plan.md", []byte("x"), "", nil), http.StatusPreconditionRequired)
	// Sizes must add up to the plaintext size (chunk sizes minus 41 each).
	bad, _ := json.Marshal(api.CommitRequest{Size: f1.Size + 1, SHA256: f1.SHA256, Chunks: mustManifest(t, c, root+"/Plan.md").Chunks})
	if r := c.do(http.MethodPut, "/api/v2/files/"+escSegs(c.enc(root+"/X.md")), bad, nil); r.status != http.StatusBadRequest || r.errCode(t) != "invalid_manifest" {
		t.Fatalf("size check: %d %s", r.status, r.body)
	}
	// Empty note: no chunks at all.
	r = c.upload(root+"/Empty.md", nil, "", map[string]string{"If-None-Match": "*"})
	want(t, r, http.StatusCreated)
	if got, _ := c.download(root + "/Empty.md"); len(got) != 0 {
		t.Fatal("empty")
	}

	// 4. Listing by (encrypted) prefix, decrypted locally. The plaintext
	// leftover is outside the prefix; without one it shows up, undecryptable.
	v0, rows := c.list(root, nil)
	if len(rows) != 3 || rows[root].ID == "" || !rows[root].IsDir || rows[root+"/Plan.md"].ID != f1.ID || rows[root+"/Plan.md"].SyncedID != "" {
		t.Fatalf("prefix listing: %v", keys(rows))
	}
	if _, all := c.list("", nil); all["?plain:old.txt"].ID == "" {
		t.Fatalf("full listing during migration should include the plaintext leftover: %v", keys(all))
	}

	// 5. Download: manifest (id, mtime) + chunks.
	got, m := c.download(root + "/Plan.md")
	if !bytes.Equal(got, note) || m.ID != f1.ID || m.Mtime == nil || !m.Mtime.Equal(time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)) ||
		m.Version != f1.Version || m.SyncedID != "" || len(m.Chunks) < 2 {
		t.Fatalf("manifest %+v", m)
	}
	// The same through the stable id (nulldrive://open?id=…).
	r = c.do(http.MethodGet, "/api/v2/ids/"+f1.ID, nil, nil)
	want(t, r, http.StatusOK)

	// 6. Replace with If-Match; a stale version is 409 with current.
	edited := append(append([]byte{}, note...), "\nEdited.\n"...)
	r = c.upload(root+"/Plan.md", edited, "2026-10-18T09:00:00Z", map[string]string{"If-Match": itoa(f1.Version)})
	want(t, r, http.StatusOK)
	var f2 api.File
	r.decode(t, &f2)
	if f2.ID != f1.ID || f2.Version <= f1.Version {
		t.Fatalf("replace %+v", f2)
	}
	r = c.upload(root+"/Plan.md", []byte("stale"), "", map[string]string{"If-Match": itoa(f1.Version)})
	if r.status != http.StatusConflict || r.errCode(t) != "conflict" || r.header.Get("ETag") != `"`+itoa(f2.Version)+`"` {
		t.Fatalf("stale replace: %d %s", r.status, r.body)
	}

	// 7. Move (If-Match header): keeps id; feed shows a tombstone whose
	// moved_to is the encrypted new path.
	mv, _ = json.Marshal(api.MoveRequest{From: c.enc(root + "/Plan.md"), To: c.enc(root + "/Archive/Plan.md")})
	want(t, c.do(http.MethodPost, "/api/v1/files/move", mv, map[string]string{"If-Match": itoa(f1.Version)}), http.StatusConflict)
	r = c.do(http.MethodPost, "/api/v1/files/move", mv, map[string]string{"If-Match": itoa(f2.Version)})
	want(t, r, http.StatusOK)
	var mr api.MoveResponse
	r.decode(t, &mr)
	if len(mr.Moved) != 1 || mr.Moved[0].File.ID != f1.ID || mr.Moved[0].File.Path != c.enc(root+"/Archive/Plan.md") {
		t.Fatalf("move %+v", mr)
	}
	v1, ch := c.list(root, &v0)
	tomb := ch[root+"/Plan.md"]
	if !tomb.Deleted || tomb.MovedTo != c.enc(root+"/Archive/Plan.md") || tomb.ID == f1.ID {
		t.Fatalf("move tombstone %+v", tomb)
	}
	if live := ch[root+"/Archive/Plan.md"]; live.ID != f1.ID || live.Deleted {
		t.Fatalf("moved row %+v", live)
	}

	// 8. Delete: tombstone keeps the id; trash lists it (encrypted).
	want(t, c.do(http.MethodDelete, "/api/v1/files/"+escSegs(c.enc(root+"/Archive/Plan.md")), nil, map[string]string{"If-Match": itoa(f2.Version)}), http.StatusConflict)
	want(t, c.do(http.MethodDelete, "/api/v1/files/"+escSegs(c.enc(root+"/Archive/Plan.md")), nil, map[string]string{"If-Match": itoa(mr.Version)}), http.StatusOK)
	_, ch = c.list(root, &v1)
	if d := ch[root+"/Archive/Plan.md"]; !d.Deleted || d.ID != f1.ID || d.MovedTo != "" {
		t.Fatalf("delete tombstone %+v", d)
	}
	r = c.do(http.MethodGet, "/api/v2/trash", nil, nil)
	want(t, r, http.StatusOK)
	if !strings.Contains(string(r.body), c.enc(root+"/Archive/Plan.md")) {
		t.Fatalf("trash: %s", r.body)
	}

	// 9. Search is off for encrypted accounts.
	r = raw.do(http.MethodGet, "/search?q=Plan", nil, nil)
	want(t, r, http.StatusOK)
	if strings.TrimSpace(string(r.body)) != "[]" || r.header.Get("X-NullDrive-Search") != "encrypted" {
		t.Fatalf("search: %s %v", r.body, r.header)
	}

	// 10. The server never saw a plaintext name.
	for _, part := range strings.Split(root+"/Plan.md", "/") {
		if strings.Contains(f1.Path, part) {
			t.Fatal("plaintext name on the server")
		}
	}
}

func mustUnlock(t *testing.T, bundle json.RawMessage, pass string) *e2ee.Keys {
	t.Helper()
	var b e2ee.Bundle
	if err := json.Unmarshal(bundle, &b); err != nil {
		t.Fatal(err)
	}
	k, err := b.UnlockPassphrase(pass)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func mustManifest(t *testing.T, c *e2eeClient, p string) api.Manifest {
	t.Helper()
	r := c.do(http.MethodGet, "/api/v2/files/"+escSegs(c.enc(p))+"/manifest", nil, nil)
	want(t, r, http.StatusOK)
	var m api.Manifest
	r.decode(t, &m)
	return m
}
