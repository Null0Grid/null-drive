package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/nulldrive/nulldrive/shared/api"
)

// The Drive side of Null Notes two-way sync (Phase 7): every write
// conditional, nothing overwritten silently, moves distinguishable from
// deletes in the change feed.

func vstr(v int64) string { return strconv.FormatInt(v, 10) }

func conflictCurrent(t *testing.T, r resp) *api.File {
	t.Helper()
	if r.status != http.StatusConflict {
		t.Fatalf("want 409, got %d %s", r.status, r.body)
	}
	var e api.ErrorBody
	r.decode(t, &e)
	if e.Error.Code != "conflict" || e.Error.Current == nil {
		t.Fatalf("want code conflict with current, got %s", r.body)
	}
	if r.header.Get("ETag") != `"`+vstr(e.Error.Current.Version)+`"` {
		t.Fatalf("conflict ETag %q, current %d", r.header.Get("ETag"), e.Error.Current.Version)
	}
	return e.Error.Current
}

func TestNotesSyncFlow(t *testing.T) {
	e := newV2(t)
	c := e.admin
	root := "Null Notes/Work"
	path := root + "/Plan.md"
	note := "---\nid: " + noteUUID + "\n---\n# Plan\n"

	// 4. Create-only: If-None-Match: * (or If-Match: 0).
	put := func(p, body string, h map[string]string) resp {
		return c.do(http.MethodPut, "/api/v1/files/"+p, strings.NewReader(body), h)
	}
	r := put(path, note, map[string]string{"If-None-Match": "*", "X-NullDrive-Mtime": "2026-10-18T08:00:00Z"})
	want(t, r, http.StatusCreated)
	var f1 api.File
	r.decode(t, &f1)
	if r.header.Get("ETag") != `"`+vstr(f1.Version)+`"` {
		t.Fatalf("PUT ETag %q", r.header.Get("ETag"))
	}
	// Retrying the same create (lost response) succeeds when the client
	// sends the content hash: Drive sees the identical file before reading
	// the body. Without the hash it can't know, so it's a conflict.
	want(t, put(path, note, map[string]string{"If-None-Match": "*", "X-Content-SHA256": sha(note)}), http.StatusOK)
	conflictCurrent(t, put(path, note, map[string]string{"If-None-Match": "*"}))
	// Something else there: 409 conflict, current = what's there.
	if cur := conflictCurrent(t, put(path, "different", map[string]string{"If-None-Match": "*"})); cur.Version != f1.Version {
		t.Fatalf("current %+v", cur)
	}
	conflictCurrent(t, put(path, "different", map[string]string{"If-Match": "0"}))
	for _, h := range []map[string]string{
		{"If-None-Match": "*", "If-Match": "3"},
		{"If-None-Match": `"3"`},
	} {
		if r := put(path, "x", h); r.status != http.StatusBadRequest || r.errCode(t) != "invalid_if_match" {
			t.Fatalf("%v: %d %s", h, r.status, r.body)
		}
	}
	// A deleted path counts as empty for create-only.
	want(t, put(root+"/tmp.md", "x", map[string]string{"If-None-Match": "*"}), http.StatusCreated)
	want(t, c.do(http.MethodDelete, "/api/v1/files/"+root+"/tmp.md", nil, nil), http.StatusOK)
	want(t, put(root+"/tmp.md", "y", map[string]string{"If-None-Match": "*"}), http.StatusCreated)

	// 1. Download: bytes plus version, hash, mtime, id, note id in headers.
	r = c.do(http.MethodGet, "/api/v1/files/"+path, nil, nil)
	want(t, r, http.StatusOK)
	h := r.header
	if string(r.body) != note || h.Get("ETag") != `"`+vstr(f1.Version)+`"` || h.Get("X-Content-SHA256") != sha(note) ||
		h.Get("X-NullDrive-Mtime") != "2026-10-18T08:00:00.000Z" || h.Get("X-NullDrive-File-Id") != f1.ID ||
		h.Get("X-NullDrive-Synced-Id") != noteUUID {
		t.Fatalf("download headers %v", h)
	}
	// Conditional and partial GETs (standard HTTP).
	want(t, c.do(http.MethodGet, "/api/v1/files/"+path, nil, map[string]string{"If-None-Match": `"` + vstr(f1.Version) + `"`}), http.StatusNotModified)
	r = c.do(http.MethodGet, "/api/v1/files/"+path, nil, map[string]string{"Range": "bytes=0-2"})
	if r.status != http.StatusPartialContent || string(r.body) != "---" {
		t.Fatalf("range: %d %q", r.status, r.body)
	}
	if r := c.do(http.MethodHead, "/api/v1/files/"+path, nil, nil); r.status != 200 || r.header.Get("X-NullDrive-File-Id") != f1.ID {
		t.Fatalf("HEAD %d %v", r.status, r.header)
	}

	// 2. Listing fields.
	fl := listUnder(t, c, root, nil)
	lf := byPathOf(fl)[path]
	if lf.ID != f1.ID || len(lf.ID) != 32 || lf.Version != f1.Version || lf.SyncedID != noteUUID || lf.SHA256 != sha(note) {
		t.Fatalf("listing %+v", lf)
	}
	since := fl.Version

	// 3. Conditional replace: stale → 409 conflict (+ current), fresh → 200.
	edited := note + "More.\n"
	if cur := conflictCurrent(t, put(path, edited, map[string]string{"If-Match": vstr(f1.Version - 1)})); cur.Version != f1.Version || cur.ID != f1.ID {
		t.Fatalf("current %+v", cur)
	}
	want(t, put(path, edited, nil), http.StatusPreconditionRequired) // never a silent overwrite
	r = put(path, edited, map[string]string{"If-Match": `"` + vstr(f1.Version) + `"`})
	want(t, r, http.StatusOK)
	var f2 api.File
	r.decode(t, &f2)
	if f2.Version <= f1.Version || f2.ID != f1.ID {
		t.Fatalf("replace %+v", f2)
	}

	// 5. Moves: stale version → 409 (body field or header); destination
	// taken → 409 target_exists; ok → same id and note id, new version.
	move := func(from, to string, ifMatch *int64, h map[string]string) resp {
		b, _ := json.Marshal(api.MoveRequest{From: from, To: to, IfMatch: ifMatch})
		hh := map[string]string{"Content-Type": "application/json"}
		for k, v := range h {
			hh[k] = v
		}
		return c.do(http.MethodPost, "/api/v1/files/move", bytes.NewReader(b), hh)
	}
	stale := f1.Version
	conflictCurrent(t, move(path, root+"/Archive/Plan.md", &stale, nil))
	conflictCurrent(t, move(path, root+"/Archive/Plan.md", nil, map[string]string{"If-Match": vstr(stale)}))
	v2 := f2.Version
	if r := move(path, root+"/Archive/Plan.md", &v2, map[string]string{"If-Match": vstr(stale)}); r.status != http.StatusBadRequest {
		t.Fatalf("disagreeing if_match: %d", r.status)
	}
	want(t, put(root+"/Taken.md", "x", map[string]string{"If-None-Match": "*"}), http.StatusCreated)
	if r := move(path, root+"/Taken.md", &v2, nil); r.status != http.StatusConflict || r.errCode(t) != "target_exists" {
		t.Fatalf("destination exists: %d %s", r.status, r.body)
	}
	if r := move(root+"/Nope.md", root+"/Other.md", nil, nil); r.status != http.StatusNotFound || r.errCode(t) != "not_found" {
		t.Fatalf("missing source: %d %s", r.status, r.body)
	}
	r = move(path, root+"/Archive/Plan.md", nil, map[string]string{"If-Match": `"` + vstr(v2) + `"`})
	want(t, r, http.StatusOK)
	var mr api.MoveResponse
	r.decode(t, &mr)
	moved := mr.Moved[0].File
	if moved.ID != f1.ID || moved.SyncedID != noteUUID || moved.Version != mr.Version || moved.Version <= v2 || moved.Path != root+"/Archive/Plan.md" {
		t.Fatalf("moved %+v", moved)
	}

	// Change feed: the move is a tombstone with moved_to (another id, no
	// note id) plus the live row with the file's id.
	feed := listUnder(t, c, root, &since)
	var tomb, live *api.File
	for i, f := range feed.Files {
		switch f.Path {
		case path:
			tomb = &feed.Files[i]
		case root + "/Archive/Plan.md":
			live = &feed.Files[i]
		}
	}
	if tomb == nil || live == nil {
		t.Fatalf("feed %+v", feed.Files)
	}
	if !tomb.Deleted || tomb.MovedTo != root+"/Archive/Plan.md" || tomb.ID == f1.ID || tomb.SyncedID != "" || tomb.Version != mr.Version {
		t.Fatalf("move tombstone %+v", tomb)
	}
	if live.Deleted || live.ID != f1.ID || live.SyncedID != noteUUID || live.MovedTo != "" {
		t.Fatalf("moved row %+v", live)
	}
	// Writing to the old path again starts a new file there (no moved_to).
	want(t, put(path, "new", map[string]string{"If-None-Match": "*"}), http.StatusCreated)
	if nf := byPathOf(listUnder(t, c, root, nil))[path]; nf.ID == f1.ID || nf.MovedTo != "" {
		t.Fatalf("new file at old path %+v", nf)
	}

	// 3. Conditional delete: stale → 409, fresh → 200 (to the trash), again → 404.
	np := root + "/Archive/Plan.md"
	conflictCurrent(t, c.do(http.MethodDelete, "/api/v1/files/"+np, nil, map[string]string{"If-Match": vstr(v2)}))
	since2 := listUnder(t, c, root, nil).Version
	r = c.do(http.MethodDelete, "/api/v1/files/"+np, nil, map[string]string{"If-Match": vstr(moved.Version)})
	want(t, r, http.StatusOK)
	var dr api.DeleteResponse
	r.decode(t, &dr)
	if len(dr.Deleted) != 1 || !dr.Deleted[0].Deleted || dr.Deleted[0].ID != f1.ID {
		t.Fatalf("delete %+v", dr)
	}
	if r := c.do(http.MethodDelete, "/api/v1/files/"+np, nil, map[string]string{"If-Match": vstr(moved.Version)}); r.status != http.StatusNotFound {
		t.Fatalf("delete again: %d", r.status)
	}
	// Replacing a file that has been deleted since: conflict, current =
	// the tombstone (nothing is resurrected).
	if cur := conflictCurrent(t, put(np, "late edit", map[string]string{"If-Match": vstr(moved.Version)})); !cur.Deleted || cur.ID != f1.ID {
		t.Fatalf("replace after delete: current %+v", cur)
	}
	// Delete tombstone: same id and note id, deleted, no moved_to.
	feed = listUnder(t, c, root, &since2)
	if len(feed.Files) != 1 {
		t.Fatalf("feed after delete %+v", feed.Files)
	}
	dt := feed.Files[0]
	if !dt.Deleted || dt.DeletedAt == nil || dt.ID != f1.ID || dt.SyncedID != noteUUID || dt.MovedTo != "" || dt.Path != np || dt.Version != dr.Version {
		t.Fatalf("delete tombstone %+v", dt)
	}

	// Conflict copy, as Notes makes it: create-only next to the original.
	want(t, put(root+"/Plan (conflict 2026-10-18).md", "---\nid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee\n---\n", map[string]string{"If-None-Match": "*"}), http.StatusCreated)

	// v2 commits take the same preconditions.
	req, blobs := manifestOf(t, []byte("chunked"))
	for hh, b := range blobs {
		c.do(http.MethodPut, "/api/v2/chunks/"+hh, bytes.NewReader(b), nil)
	}
	body, _ := json.Marshal(req)
	want(t, c.do(http.MethodPut, "/api/v2/files/"+root+"/Taken.md", bytes.NewReader(body), map[string]string{"If-None-Match": "*"}), http.StatusConflict)
	want(t, c.do(http.MethodPut, "/api/v2/files/"+root+"/v2.md", bytes.NewReader(body), map[string]string{"If-None-Match": "*"}), http.StatusCreated)
}
