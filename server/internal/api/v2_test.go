package api_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	srvapi "github.com/Null0Grid/null-drive/server/internal/api"
	"github.com/Null0Grid/null-drive/server/internal/chunkstore"
	"github.com/Null0Grid/null-drive/shared/api"
	"github.com/Null0Grid/null-drive/shared/chunker"
)

// Small chunks so tests exercise multi-chunk files with KB of data.
var testParams = chunker.Params{Min: 1 << 10, Avg: 4 << 10, Max: 16 << 10}

type v2env struct {
	t     *testing.T
	base  string
	admin *client
	clk   *testClock
	data  string
}

func newV2(t *testing.T) *v2env {
	t.Helper()
	clk := &testClock{t: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	var dataDir string
	ts := newTestServerWith(t, clk.Now, func(o *srvapi.Options) {
		cs := chunkstore.New(o.Blobs, o.Store, nil)
		cs.Params = testParams
		o.Chunks = cs
		o.GCGrace = time.Hour
		dataDir = o.DataDir
	})
	return &v2env{t: t, base: ts.URL, admin: setupAdmin(t, ts), clk: clk, data: dataDir}
}

func randBytes(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, 1))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

func hexSum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func manifestOf(t *testing.T, b []byte) (api.CommitRequest, map[string][]byte) {
	t.Helper()
	blobs := map[string][]byte{}
	refs, whole, size, err := chunker.Manifest(bytes.NewReader(b), testParams, func(c chunker.Chunk) error {
		blobs[c.HexHash()] = append([]byte(nil), c.Data...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	req := api.CommitRequest{Size: size, SHA256: whole}
	for _, r := range refs {
		req.Chunks = append(req.Chunks, api.ChunkRef{Hash: r.Hash, Size: r.Size})
	}
	return req, blobs
}

// uploadV2 does the whole client protocol and returns bytes uploaded.
func uploadV2(t *testing.T, c *client, path string, content []byte, ifMatch string) (api.File, int) {
	t.Helper()
	req, blobs := manifestOf(t, content)
	hashes := make([]string, 0, len(req.Chunks))
	for _, ch := range req.Chunks {
		hashes = append(hashes, ch.Hash)
	}
	r := c.json(http.MethodPost, "/api/v2/chunks/check", api.ChunkCheckRequest{Hashes: hashes})
	want(t, r, http.StatusOK)
	var chk api.ChunkCheckResponse
	r.decode(t, &chk)
	sent := 0
	for _, h := range chk.Missing {
		want(t, c.do(http.MethodPut, "/api/v2/chunks/"+h, bytes.NewReader(blobs[h]), nil), http.StatusCreated)
		sent += len(blobs[h])
	}
	b, _ := json.Marshal(req)
	hdr := map[string]string{}
	if ifMatch != "" {
		hdr["If-Match"] = ifMatch
	}
	r = c.do(http.MethodPut, "/api/v2/files/"+path, bytes.NewReader(b), hdr)
	if r.status != http.StatusCreated && r.status != http.StatusOK {
		t.Fatalf("commit %s: %d %s", path, r.status, r.body)
	}
	var f api.File
	r.decode(t, &f)
	return f, sent
}

func blobCount(t *testing.T, dir string) int {
	n := 0
	_ = filepath.WalkDir(filepath.Join(dir, "blobs"), func(_ string, d os.DirEntry, _ error) error {
		if d != nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func TestV2UploadDownloadAndDelta(t *testing.T) {
	e := newV2(t)
	content := randBytes(1, 200<<10)
	f, sent := uploadV2(t, e.admin, "big.bin", content, "")
	if sent != len(content) || f.Size != int64(len(content)) || f.SHA256 != hexSum(content) {
		t.Fatalf("first upload sent %d, file %+v", sent, f)
	}

	// v1 download of a chunked file, including a Range across chunks.
	r := e.admin.do(http.MethodGet, "/api/v1/files/big.bin", nil, nil)
	want(t, r, http.StatusOK)
	if !bytes.Equal(r.body, content) {
		t.Fatal("v1 download differs")
	}
	r = e.admin.do(http.MethodGet, "/api/v1/files/big.bin", nil, map[string]string{"Range": "bytes=5000-70000"})
	want(t, r, http.StatusPartialContent)
	if !bytes.Equal(r.body, content[5000:70001]) {
		t.Fatal("range across chunks differs")
	}

	// Edit 10 bytes in the middle: only a chunk or two travel.
	edited := append([]byte{}, content...)
	copy(edited[100<<10:], "0123456789")
	f2, sent := uploadV2(t, e.admin, "big.bin", edited, `"`+itoa(f.Version)+`"`)
	t.Logf("edit of 10 bytes in 200 KiB re-sent %d bytes", sent)
	if sent == 0 || sent > 3*testParams.Max {
		t.Fatalf("delta upload sent %d bytes", sent)
	}

	// Manifest + chunk download reassemble the new version.
	var m api.Manifest
	r = e.admin.do(http.MethodGet, "/api/v2/files/big.bin/manifest", nil, nil)
	want(t, r, http.StatusOK)
	r.decode(t, &m)
	var got []byte
	for _, c := range m.Chunks {
		cr := e.admin.do(http.MethodGet, "/api/v2/chunks/"+c.Hash, nil, nil)
		want(t, cr, http.StatusOK)
		got = append(got, cr.body...)
	}
	if !bytes.Equal(got, edited) || m.Version != f2.Version || m.SHA256 != hexSum(edited) {
		t.Fatal("manifest download differs")
	}

	// Stale If-Match → 409; identical re-commit → 200 no-op.
	b, _ := json.Marshal(manifestOrDie(t, edited))
	want(t, e.admin.do(http.MethodPut, "/api/v2/files/big.bin", bytes.NewReader(b), map[string]string{"If-Match": `"` + itoa(f.Version) + `"`}), http.StatusOK)
	other, _ := json.Marshal(manifestOrDie(t, content))
	want(t, e.admin.do(http.MethodPut, "/api/v2/files/big.bin", bytes.NewReader(other), map[string]string{"If-Match": `"` + itoa(f.Version) + `"`}), http.StatusConflict)
	want(t, e.admin.do(http.MethodPut, "/api/v2/files/big.bin", bytes.NewReader(other), nil), http.StatusPreconditionRequired)
}

func manifestOrDie(t *testing.T, b []byte) api.CommitRequest { r, _ := manifestOf(t, b); return r }

func TestV2CommitValidation(t *testing.T) {
	e := newV2(t)
	content := randBytes(2, 40<<10)
	req, blobs := manifestOf(t, content)
	b, _ := json.Marshal(req)
	r := e.admin.do(http.MethodPut, "/api/v2/files/x.bin", bytes.NewReader(b), nil)
	want(t, r, http.StatusUnprocessableEntity)
	var miss struct{ Missing []string }
	r.decode(t, &miss)
	if len(miss.Missing) != len(blobs) {
		t.Fatalf("missing %d, want %d", len(miss.Missing), len(blobs))
	}
	// Wrong bytes for a hash.
	want(t, e.admin.do(http.MethodPut, "/api/v2/chunks/"+req.Chunks[0].Hash, strings.NewReader("nope"), nil), http.StatusBadRequest)
	// Sizes must add up.
	for h, d := range blobs {
		want(t, e.admin.do(http.MethodPut, "/api/v2/chunks/"+h, bytes.NewReader(d), nil), http.StatusCreated)
	}
	bad := req
	bad.Size++
	b, _ = json.Marshal(bad)
	want(t, e.admin.do(http.MethodPut, "/api/v2/files/x.bin", bytes.NewReader(b), nil), http.StatusBadRequest)
	b, _ = json.Marshal(req)
	want(t, e.admin.do(http.MethodPut, "/api/v2/files/x.bin", bytes.NewReader(b), nil), http.StatusCreated)
	// Empty file: no chunks.
	empty, _ := json.Marshal(api.CommitRequest{Size: 0, SHA256: hexSum(nil)})
	want(t, e.admin.do(http.MethodPut, "/api/v2/files/empty.txt", bytes.NewReader(empty), nil), http.StatusCreated)
	r = e.admin.do(http.MethodGet, "/api/v1/files/empty.txt", nil, nil)
	want(t, r, http.StatusOK)
	if len(r.body) != 0 {
		t.Fatal("empty file not empty")
	}
}

func TestV2DedupAcrossUploadsIsStoredOnce(t *testing.T) {
	e := newV2(t)
	content := randBytes(3, 120<<10)
	uploadV2(t, e.admin, "a.bin", content, "")
	before := blobCount(t, e.data)
	// Same bytes under another name: nothing to upload, nothing stored.
	_, sent := uploadV2(t, e.admin, "copy of a.bin", content, "")
	if sent != 0 || blobCount(t, e.data) != before {
		t.Fatalf("second copy sent %d bytes, blobs %d -> %d", sent, before, blobCount(t, e.data))
	}
	// v1 upload of the same bytes (web UI) also dedups.
	want(t, e.admin.put("third.bin", string(content), nil), http.StatusCreated)
	if blobCount(t, e.data) != before {
		t.Fatal("v1 upload stored duplicate chunks")
	}
	var st api.Stats
	e.admin.do(http.MethodGet, "/api/v2/stats", nil, nil).decode(t, &st)
	if st.LogicalBytes != 3*int64(len(content)) || st.StoredBytes != int64(len(content)) || st.ServerStoredBytes != int64(len(content)) {
		t.Fatalf("stats %+v", st)
	}
}

func TestV2ChunkAccessIsPerUser(t *testing.T) {
	// Cross-user isolation is tested in meta (TestChunkOwnershipIsPerUser):
	// there's only one user until Phase 7 adds user management. Here: a
	// hash you merely know is never downloadable.
	e := newV2(t)
	content := randBytes(4, 30<<10)
	req, _ := manifestOf(t, content)
	r := e.admin.do(http.MethodGet, "/api/v2/chunks/"+req.Chunks[0].Hash, nil, nil)
	want(t, r, http.StatusNotFound)
	uploadV2(t, e.admin, "f.bin", content, "")
	want(t, e.admin.do(http.MethodGet, "/api/v2/chunks/"+req.Chunks[0].Hash, nil, nil), http.StatusOK)
}

func TestV2HistoryRestoreAndTrash(t *testing.T) {
	e := newV2(t)
	dev := registerDevice(t, e.admin, "Laptop")
	var vers []api.File
	for i, text := range []string{"one", "two", "three"} {
		c := e.admin
		if i == 1 {
			c = dev
		}
		hdr := map[string]string{}
		if len(vers) > 0 {
			hdr["If-Match"] = `"` + itoa(vers[len(vers)-1].Version) + `"`
		}
		r := c.put("docs/note.txt", text, hdr)
		if r.status != 200 && r.status != 201 {
			t.Fatalf("put %d %s", r.status, r.body)
		}
		var f api.File
		r.decode(t, &f)
		vers = append(vers, f)
		e.clk.Advance(time.Minute)
	}
	var vl api.VersionList
	r := e.admin.do(http.MethodGet, "/api/v2/files/docs/note.txt/versions", nil, nil)
	want(t, r, http.StatusOK)
	r.decode(t, &vl)
	if len(vl.Versions) != 3 || !vl.Versions[0].Current || vl.Versions[1].Device != "Laptop" || vl.Versions[0].Device != "" {
		t.Fatalf("versions %+v", vl.Versions)
	}
	// Download an old version.
	r = e.admin.do(http.MethodGet, "/api/v2/files/docs/note.txt/versions/"+itoa(vers[0].Version)+"/content", nil, nil)
	want(t, r, http.StatusOK)
	if string(r.body) != "one" {
		t.Fatalf("old version content %q", r.body)
	}
	// Restore it: a NEW version with the old content; history grows.
	r = e.admin.do(http.MethodPost, "/api/v2/files/docs/note.txt/versions/"+itoa(vers[0].Version)+"/restore", nil, nil)
	want(t, r, http.StatusOK)
	var restored api.File
	r.decode(t, &restored)
	if restored.Version <= vers[2].Version {
		t.Fatal("restore did not create a new version")
	}
	if got := e.admin.do(http.MethodGet, "/api/v1/files/docs/note.txt", nil, nil); string(got.body) != "one" {
		t.Fatalf("after restore %q", got.body)
	}
	e.admin.do(http.MethodGet, "/api/v2/files/docs/note.txt/versions", nil, nil).decode(t, &vl)
	if len(vl.Versions) != 4 || vl.Versions[0].Origin != "restore" {
		t.Fatalf("after restore %+v", vl.Versions)
	}

	// Delete the whole folder → the file is in the bin; restore brings it
	// back together with its folder.
	want(t, e.admin.do(http.MethodDelete, "/api/v1/files/docs", nil, nil), http.StatusOK)
	var tl api.TrashList
	e.admin.do(http.MethodGet, "/api/v2/trash", nil, nil).decode(t, &tl)
	if len(tl.Items) != 1 || tl.Items[0].Path != "docs/note.txt" || tl.Items[0].Versions != 4 {
		t.Fatalf("trash %+v", tl.Items)
	}
	if want := tl.Items[0].DeletedAt.Add(30 * 24 * time.Hour); !tl.Items[0].PurgeAt.Equal(want) { // trash_days
		t.Fatalf("purge at %v", tl.Items[0].PurgeAt)
	}
	r = e.admin.do(http.MethodPost, "/api/v2/files/docs/note.txt/versions/"+itoa(tl.Items[0].Version)+"/restore", nil, nil)
	want(t, r, http.StatusOK)
	if got := e.admin.do(http.MethodGet, "/api/v1/files/docs/note.txt", nil, nil); string(got.body) != "one" {
		t.Fatalf("after undelete %q", got.body)
	}
	e.admin.do(http.MethodGet, "/api/v2/trash", nil, nil).decode(t, &tl)
	if len(tl.Items) != 0 {
		t.Fatalf("restored file still in trash: %+v", tl.Items)
	}

	// A move takes the history along and is not a deletion.
	want(t, e.admin.json(http.MethodPost, "/api/v1/files/move", api.MoveRequest{From: "docs/note.txt", To: "archive/note.txt"}), http.StatusOK)
	e.admin.do(http.MethodGet, "/api/v2/files/archive/note.txt/versions", nil, nil).decode(t, &vl)
	if len(vl.Versions) != 5 {
		t.Fatalf("history after move: %d versions", len(vl.Versions))
	}
	e.admin.do(http.MethodGet, "/api/v2/trash", nil, nil).decode(t, &tl)
	if len(tl.Items) != 0 {
		t.Fatalf("moved file appeared in trash: %+v", tl.Items)
	}
	want(t, e.admin.do(http.MethodGet, "/api/v2/files/docs/note.txt/versions", nil, nil), http.StatusNotFound)
}

func TestV2PathOperationsAreUnambiguous(t *testing.T) {
	e := newV2(t)
	// Files literally named like the operation suffixes.
	want(t, e.admin.put("manifest", "m", nil), http.StatusCreated)
	want(t, e.admin.put("versions/manifest", "vm", nil), http.StatusCreated)
	var m api.Manifest
	r := e.admin.do(http.MethodGet, "/api/v2/files/manifest/manifest", nil, nil)
	want(t, r, http.StatusOK)
	r.decode(t, &m)
	if m.Path != "manifest" {
		t.Fatalf("got manifest of %q", m.Path)
	}
	e.admin.do(http.MethodGet, "/api/v2/files/versions/manifest/manifest", nil, nil).decode(t, &m)
	if m.Path != "versions/manifest" {
		t.Fatalf("got manifest of %q", m.Path)
	}
	want(t, e.admin.do(http.MethodGet, "/api/v2/files/versions/manifest/versions", nil, nil), http.StatusOK)
	want(t, e.admin.do(http.MethodGet, "/api/v2/files/manifest", nil, nil), http.StatusNotFound) // no path left
	want(t, e.admin.do(http.MethodGet, "/api/v2/files/a/whatever", nil, nil), http.StatusNotFound)
	want(t, e.admin.do(http.MethodGet, "/api/v2/files/..%2Fx/manifest", nil, nil), http.StatusBadRequest)
}

func TestV2RetentionAndGC(t *testing.T) {
	e := newV2(t)
	// 5 versions, one per day, then 40 days pass.
	var last api.File
	for i := 0; i < 5; i++ {
		hdr := map[string]string{}
		if i > 0 {
			hdr["If-Match"] = `"` + itoa(last.Version) + `"`
		}
		r := e.admin.put("log.bin", string(randBytes(uint64(10+i), 20<<10)), hdr)
		r.decode(t, &last)
		e.clk.Advance(24 * time.Hour)
	}
	// A pending upload that never gets committed.
	orphan := randBytes(99, 3000)
	want(t, e.admin.do(http.MethodPut, "/api/v2/chunks/"+hexSum(orphan), bytes.NewReader(orphan), nil), http.StatusCreated)

	// Keep 1 day in full, 0 daily days: only the current version survives
	// (protected), the four older ones go.
	one, zero := 1, 0
	want(t, e.admin.json(http.MethodPatch, "/api/v1/system/settings", api.UpdateSettingsRequest{RetentionFullDays: &one, RetentionDailyDays: &zero}), http.StatusOK)
	before := blobCount(t, e.data)
	var rep api.GCReport
	r := e.admin.json(http.MethodPost, "/api/v2/admin/gc", struct{}{})
	want(t, r, http.StatusOK)
	r.decode(t, &rep)
	if rep.VersionsPruned != 4 {
		t.Fatalf("pruned %d versions, want 4 (%+v)", rep.VersionsPruned, rep)
	}
	// Freed chunks are past the 1h grace (they were uploaded days ago in
	// clock time) and get deleted; the pending orphan upload is within its
	// grace period and must survive.
	if rep.ChunksDeleted == 0 || blobCount(t, e.data) >= before {
		t.Fatalf("nothing collected: %+v", rep)
	}
	if got := e.admin.do(http.MethodGet, "/api/v1/files/log.bin", nil, nil); got.status != 200 || int64(len(got.body)) != last.Size {
		t.Fatal("current version damaged by GC")
	}
	var chk api.ChunkCheckResponse
	e.admin.json(http.MethodPost, "/api/v2/chunks/check", api.ChunkCheckRequest{Hashes: []string{hexSum(orphan)}}).decode(t, &chk)
	if len(chk.Missing) != 0 {
		t.Fatal("in-flight upload was collected inside its grace period")
	}
	// After the grace period it goes too.
	e.clk.Advance(2 * time.Hour)
	e.admin.json(http.MethodPost, "/api/v2/admin/gc", struct{}{}).decode(t, &rep)
	e.admin.json(http.MethodPost, "/api/v2/chunks/check", api.ChunkCheckRequest{Hashes: []string{hexSum(orphan)}}).decode(t, &chk)
	if len(chk.Missing) != 1 || rep.ChunksDeleted != 1 {
		t.Fatalf("expired upload not collected: %+v", rep)
	}
	// Devices can't run GC.
	dev := registerDevice(t, e.admin, "d")
	want(t, dev.json(http.MethodPost, "/api/v2/admin/gc", struct{}{}), http.StatusForbidden)
}
