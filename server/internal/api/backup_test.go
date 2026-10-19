package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Null0Grid/null-drive/shared/api"
)

// The Drive side of Null Notes backup (Phase 6): what a backup tool does
// against the API, end to end.

const noteUUID = "3f2504e0-4f89-11d3-9a0c-0305e82c3301"

func listUnder(t *testing.T, c *client, prefix string, since *int64) api.FileList {
	t.Helper()
	q := "?prefix=" + url.QueryEscape(prefix)
	if since != nil {
		q += "&since=" + strconv.FormatInt(*since, 10)
	}
	r := c.do(http.MethodGet, "/api/v1/files"+q, nil, nil)
	want(t, r, http.StatusOK)
	var fl api.FileList
	r.decode(t, &fl)
	return fl
}

func byPathOf(fl api.FileList) map[string]api.File {
	m := map[string]api.File{}
	for _, f := range fl.Files {
		m[f.Path] = f
	}
	return m
}

func putAny(t *testing.T, c *client, path, body, mtime string) resp {
	t.Helper()
	h := map[string]string{"If-Match": "*"}
	if mtime != "" {
		h["X-NullDrive-Mtime"] = mtime
	}
	return c.do(http.MethodPut, "/api/v1/files/"+path, strings.NewReader(body), h)
}

func TestNotesBackupFlow(t *testing.T) {
	e := newV2(t)
	c := e.admin
	root := "Backups/Null Notes/Work"

	// 1. Folders: mkdir -p, idempotent.
	want(t, c.json(http.MethodPost, "/api/v1/folders", api.CreateFolderRequest{Path: root + "/Projects"}), http.StatusCreated)
	want(t, c.json(http.MethodPost, "/api/v1/folders", api.CreateFolderRequest{Path: root + "/Projects"}), http.StatusOK)
	want(t, c.json(http.MethodPost, "/api/v1/folders", api.CreateFolderRequest{Path: root}), http.StatusOK)

	// 2. Replace by path with If-Match: * (no version needed), mtime kept.
	note := "---\nid: " + noteUUID + "\n---\n# Plan\nShip it.\n"
	r := putAny(t, c, root+"/Projects/Plan.md", note, "2026-10-01T08:00:00.123456789+02:00")
	want(t, r, http.StatusCreated)
	var f1 api.File
	r.decode(t, &f1)
	if f1.SyncedID != noteUUID {
		t.Fatalf("synced_id %q", f1.SyncedID)
	}
	if f1.Mtime == nil || !f1.Mtime.Equal(time.Date(2026, 10, 1, 6, 0, 0, 123_000_000, time.UTC)) {
		t.Fatalf("mtime %v (want UTC, millisecond precision)", f1.Mtime)
	}
	edited := note + "Now with more detail.\n"
	r = putAny(t, c, root+"/Projects/Plan.md", edited, "2026-10-02T09:00:00Z")
	want(t, r, http.StatusOK)
	var f2 api.File
	r.decode(t, &f2)
	if f2.Version <= f1.Version || f2.SyncedID != noteUUID || f2.ID != f1.ID {
		t.Fatalf("replace: %+v", f2)
	}
	// The old content is in history.
	var vl api.VersionList
	c.do(http.MethodGet, "/api/v2/files/"+root+"/Projects/Plan.md/versions", nil, nil).decode(t, &vl)
	if len(vl.Versions) != 2 {
		t.Fatalf("history: %d versions", len(vl.Versions))
	}
	// A numeric If-Match still guards as before; no If-Match on an existing
	// file is still refused.
	want(t, c.do(http.MethodPut, "/api/v1/files/"+root+"/Projects/Plan.md", strings.NewReader("x"),
		map[string]string{"If-Match": strconv.FormatInt(f1.Version, 10)}), http.StatusConflict)
	want(t, c.do(http.MethodPut, "/api/v1/files/"+root+"/Projects/Plan.md", strings.NewReader("x"), nil), http.StatusPreconditionRequired)

	// 3. Same content, newer mtime: recorded, but no new version and no
	// change-feed entry (sync clients have nothing to download).
	r = putAny(t, c, root+"/Projects/Plan.md", edited, "2026-10-05T10:00:00Z")
	want(t, r, http.StatusOK)
	var f3 api.File
	r.decode(t, &f3)
	if f3.Version != f2.Version || f3.Mtime == nil || !f3.Mtime.Equal(time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("touch: %+v", f3)
	}
	for _, bad := range []string{"yesterday", "1969-12-31T23:59:59Z", "2026-13-01T00:00:00Z"} {
		r := putAny(t, c, root+"/x.md", "x", bad)
		if r.status != http.StatusBadRequest || r.errCode(t) != "invalid_mtime" {
			t.Fatalf("mtime %q: %d %s", bad, r.status, r.body)
		}
	}

	// 4. Listing by prefix: hash, mtime, synced_id, only this subtree.
	putAny(t, c, root+"/todo.txt", "buy milk", "2026-10-03T00:00:00Z")
	putAny(t, c, "Elsewhere/notes.md", "---\nid: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee\n---\n", "")
	fl := listUnder(t, c, root, nil)
	m := byPathOf(fl)
	if len(m) != 4 { // Work, Work/Projects, Plan.md, todo.txt
		t.Fatalf("prefix listing: %v", keys(m))
	}
	if _, ok := m["Elsewhere/notes.md"]; ok {
		t.Fatal("prefix listing leaked another folder")
	}
	plan := m[root+"/Projects/Plan.md"]
	if plan.SHA256 != sha(edited) || plan.SyncedID != noteUUID || plan.Mtime == nil {
		t.Fatalf("listed plan: %+v", plan)
	}
	if todo := m[root+"/todo.txt"]; todo.SyncedID != "" || todo.Mtime == nil {
		t.Fatalf("listed todo: %+v", todo)
	}
	// "Work" must not match "Work2/…".
	putAny(t, c, root+"2/other.md", "x", "")
	if _, ok := byPathOf(listUnder(t, c, root, nil))[root+"2/other.md"]; ok {
		t.Fatal("prefix matched a sibling with the same start")
	}
	// Changes since a version, within the prefix.
	since := fl.Version
	putAny(t, c, root+"/todo.txt", "buy oat milk", "")
	putAny(t, c, "Elsewhere/notes.md", "changed", "")
	ch := listUnder(t, c, root, &since)
	if len(ch.Files) != 1 || ch.Files[0].Path != root+"/todo.txt" {
		t.Fatalf("since: %+v", ch.Files)
	}
	want(t, c.do(http.MethodGet, "/api/v1/files?prefix=../etc", nil, nil), http.StatusBadRequest)

	// 5. synced_id follows content: a version without frontmatter clears it,
	// restoring the old version brings it back.
	r = putAny(t, c, root+"/Projects/Plan.md", "no frontmatter now", "")
	var f4 api.File
	r.decode(t, &f4)
	if f4.SyncedID != "" {
		t.Fatalf("synced_id kept without frontmatter: %q", f4.SyncedID)
	}
	r = c.do(http.MethodPost, "/api/v2/files/"+root+"/Projects/Plan.md/versions/"+itoa(f2.Version)+"/restore", nil, nil)
	want(t, r, http.StatusOK)
	var f5 api.File
	r.decode(t, &f5)
	if f5.SyncedID != noteUUID || f5.Mtime == nil || !f5.Mtime.Equal(time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("restored: %+v", f5)
	}
	// Moving keeps both.
	want(t, c.json(http.MethodPost, "/api/v1/files/move", api.MoveRequest{From: root + "/Projects/Plan.md", To: root + "/Plan.md"}), http.StatusOK)
	if mv := byPathOf(listUnder(t, c, root, nil))[root+"/Plan.md"]; mv.SyncedID != noteUUID || mv.Mtime == nil {
		t.Fatalf("moved: %+v", mv)
	}

	// 6. Chunked (v2) commits too: mtime in the request, note id read from
	// the chunks.
	big := []byte("---\nid: " + noteUUID + "\n---\n" + strings.Repeat("A long note. ", 3000))
	req, blobs := manifestOf(t, big)
	for h, b := range blobs {
		c.do(http.MethodPut, "/api/v2/chunks/"+h, bytes.NewReader(b), nil)
	}
	mt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	req.Mtime = &mt
	body, _ := json.Marshal(req)
	r = c.do(http.MethodPut, "/api/v2/files/"+root+"/Long.md", bytes.NewReader(body), map[string]string{"If-Match": "*"})
	want(t, r, http.StatusCreated)
	var lf api.File
	r.decode(t, &lf)
	if lf.SyncedID != noteUUID || lf.Mtime == nil || !lf.Mtime.Equal(mt) {
		t.Fatalf("v2 commit: %+v", lf)
	}
	if len(req.Chunks) < 2 {
		t.Fatalf("test needs a multi-chunk note, got %d chunk(s)", len(req.Chunks))
	}

	// 7. Deleting goes to the 30-day trash.
	r = c.do(http.MethodDelete, "/api/v1/files/"+root+"/todo.txt", nil, map[string]string{"If-Match": "*"})
	want(t, r, http.StatusOK)
	var tl api.TrashList
	c.do(http.MethodGet, "/api/v2/trash", nil, nil).decode(t, &tl)
	var item *api.TrashItem
	for i := range tl.Items {
		if tl.Items[i].Path == root+"/todo.txt" {
			item = &tl.Items[i]
		}
	}
	if item == nil || !item.PurgeAt.Equal(item.DeletedAt.Add(30*24*time.Hour)) {
		t.Fatalf("trash: %+v", tl.Items)
	}
}

func keys(m map[string]api.File) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestTrashDaysSetting(t *testing.T) {
	ts := newTestServer(t)
	c := setupAdmin(t, ts)
	var st api.ServerSettings
	c.do(http.MethodGet, "/api/v1/system/settings", nil, nil).decode(t, &st)
	if st.TrashDays != 30 {
		t.Fatalf("default trash_days %d", st.TrashDays)
	}
	days := 14
	want(t, c.json(http.MethodPatch, "/api/v1/system/settings", api.UpdateSettingsRequest{TrashDays: &days}), http.StatusOK)
	c.do(http.MethodGet, "/api/v1/system/settings", nil, nil).decode(t, &st)
	if st.TrashDays != 14 || st.RetentionFullDays != 30 {
		t.Fatalf("after update %+v", st)
	}
	zero := 0
	r := c.json(http.MethodPatch, "/api/v1/system/settings", api.UpdateSettingsRequest{TrashDays: &zero})
	if r.status != http.StatusBadRequest {
		t.Fatalf("trash_days 0: %d", r.status)
	}
	c.put("a.txt", "x", nil)
	want(t, c.do(http.MethodDelete, "/api/v1/files/a.txt", nil, nil), http.StatusOK)
	var tl api.TrashList
	c.do(http.MethodGet, "/api/v2/trash", nil, nil).decode(t, &tl)
	if len(tl.Items) != 1 || !tl.Items[0].PurgeAt.Equal(tl.Items[0].DeletedAt.Add(14*24*time.Hour)) {
		t.Fatalf("trash %+v", tl.Items)
	}
	var stats api.Stats
	c.do(http.MethodGet, "/api/v2/stats", nil, nil).decode(t, &stats)
	if stats.TrashDays != 14 {
		t.Fatalf("stats trash_days %d", stats.TrashDays)
	}
}
