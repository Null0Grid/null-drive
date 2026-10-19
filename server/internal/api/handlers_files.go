package api

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/nulldrive/nulldrive/server/internal/meta"
	"github.com/nulldrive/nulldrive/server/internal/noteid"
	"github.com/nulldrive/nulldrive/server/storage"
	"github.com/nulldrive/nulldrive/shared/api"
	"github.com/nulldrive/nulldrive/shared/pathutil"
)

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	if !requireE2EEClient(w, r) {
		return
	}
	var since *int64
	if v := r.URL.Query().Get("since"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid_since", "since must be a non-negative integer version")
			return
		}
		since = &n
	}
	// ?prefix=<folder>: only that folder's subtree (e.g. a backup's root).
	var prefix string
	if v := r.URL.Query().Get("prefix"); v != "" {
		p, err := pathutil.Clean(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_path", "prefix: "+err.Error())
			return
		}
		prefix = p
	}
	files, version, err := s.store.ListFilesUnder(r.Context(), principalFrom(r.Context()).User.ID, prefix, since)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := api.FileList{Version: version, Files: make([]api.File, 0, len(files))}
	for _, f := range files {
		out.Files = append(out.Files, toAPIFile(f))
	}
	writeJSON(w, http.StatusOK, out)
}

// filePath extracts and validates the {path} part of /api/v1/files/{path}
// from the *escaped* URL path, so "%2e%2e" and literal ".." are both seen
// and rejected by pathutil.Clean.
func filePath(r *http.Request) (string, error) {
	raw, err := url.PathUnescape(strings.TrimPrefix(r.URL.EscapedPath(), filesPrefix))
	if err != nil {
		return "", pathutil.ErrInvalid
	}
	return pathutil.Clean(raw)
}

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	p, err := filePath(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_path", err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if refuseWholeFile(w, r) {
			s.download(w, r, p)
		}
	case http.MethodPut:
		if refuseWholeFile(w, r) {
			s.upload(w, r, p)
		}
	case http.MethodDelete:
		if requireE2EEClient(w, r) {
			s.deleteFile(w, r, p)
		}
	default:
		w.Header().Set("Allow", "GET, HEAD, PUT, DELETE")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

// upload: PUT /api/v1/files/{path}
//
// Order of operations:
//  1. Validate headers and check preconditions against current metadata,
//     before reading the body (fail fast, no wasted upload).
//  2. Stream the body through the chunker into the chunk store, hashing
//     the whole file as it goes.
//  3. Commit a new version in a transaction that re-checks the precondition.
//
// If step 3 loses a race, the chunks from step 2 are left unreferenced.
// That is harmless (content-addressed, never served without metadata);
// the garbage collector reclaims them after its grace period.
func (s *Server) upload(w http.ResponseWriter, r *http.Request, p string) {
	pre, err := parseWritePrecondition(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_if_match", err.Error())
		return
	}
	mtime, err := parseMtimeHeader(r.Header.Get(api.HeaderMtime))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_mtime", err.Error())
		return
	}
	expected := strings.ToLower(strings.TrimSpace(r.Header.Get(api.HeaderContentSHA256)))
	if expected != "" && !storage.ValidHash(expected) {
		writeError(w, http.StatusBadRequest, "invalid_sha256", api.HeaderContentSHA256+" must be 64 hex characters")
		return
	}
	uid := principalFrom(r.Context()).User.ID

	cur, err := s.store.GetFile(r.Context(), uid, p)
	if err != nil && !errors.Is(err, meta.ErrNotFound) {
		s.internalError(w, r, err)
		return
	}
	var curPtr *meta.File
	if err == nil {
		curPtr = &cur
	}
	if _, err := meta.CheckPut(curPtr, expected, -1, pre); err != nil {
		s.writeStoreError(w, r, err)
		return
	}

	// Chunk the stream on the server: v1 clients and the web UI get the
	// same dedup and version history as chunked (v2) clients.
	// Markdown: keep the start of the body to read the Null Notes note id
	// from its frontmatter (synced_id).
	body := io.Reader(r.Body)
	var head *headBuffer
	if noteid.IsMarkdown(p) {
		head = &headBuffer{max: noteid.MaxRead}
		body = io.TeeReader(r.Body, head)
	}
	refs, whole, size, err := s.chunks.Ingest(r.Context(), uid, body)
	if err != nil {
		if r.Context().Err() != nil {
			return // client went away; nothing useful to send
		}
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "upload too large")
			return
		}
		s.internalError(w, r, err)
		return
	}
	if expected != "" && whole != expected {
		// The chunks stay unreferenced and are collected after the grace period.
		writeError(w, http.StatusBadRequest, "hash_mismatch", "uploaded content does not match "+api.HeaderContentSHA256)
		return
	}

	var syncedID string
	if head != nil {
		syncedID = noteid.ID(p, head.b)
	}
	f, created, err := s.store.CommitFile(r.Context(), uid, meta.Commit{
		Path: p, SHA256: whole, Size: size, Chunks: refs, DeviceID: deviceIDOf(r),
		Mtime: mtime, SyncedID: syncedID,
	}, pre)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(f.Version))
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	if f.Version > cur.Version { // false for an idempotent retry (no-op)
		s.changed(r, f.Version)
	}
	writeJSON(w, status, toAPIFile(f))
}

// download: GET/HEAD /api/v1/files/{path}. http.ServeContent provides Range
// requests (resumable downloads) and If-None-Match/If-Match handling.
func (s *Server) download(w http.ResponseWriter, r *http.Request, p string) {
	f, err := s.store.GetFile(r.Context(), principalFrom(r.Context()).User.ID, p)
	if err == nil && !f.Live() {
		err = meta.ErrNotFound
	}
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if f.IsDir {
		writeError(w, http.StatusBadRequest, "is_folder", "path is a folder; list it with GET /api/v1/files")
		return
	}
	// What a sync client needs to record next to the bytes it saved.
	h := w.Header()
	h.Set(api.HeaderFileID, f.UID)
	if f.Mtime != nil {
		h.Set(api.HeaderMtime, f.Mtime.UTC().Format("2006-01-02T15:04:05.000Z"))
	}
	if f.SyncedID != "" {
		h.Set(api.HeaderSyncedID, f.SyncedID)
	}
	s.serveVersion(w, r, f.Path, f.Version, f.VersionID, f.SHA256, f.ModifiedAt)
}

// serveVersion streams one stored version of a file with Range support.
func (s *Server) serveVersion(w http.ResponseWriter, r *http.Request, name string, version, versionID int64, sha string, mod time.Time) {
	chunks, err := s.store.Manifest(r.Context(), versionID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	rc := s.chunks.Open(chunks)
	defer rc.Close()
	f := struct {
		Path, SHA256 string
	}{name, sha}

	h := w.Header()
	h.Set("ETag", etag(version))
	h.Set(api.HeaderContentSHA256, f.SHA256)
	ctype := mime.TypeByExtension(path.Ext(f.Path))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	h.Set("Content-Type", ctype)
	// User content is served as a download, sandboxed, from the same origin
	// as the (future) web UI. Without this an uploaded .html or .svg could
	// run script with the user's session: stored XSS.
	h.Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(path.Base(f.Path)))
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	http.ServeContent(w, r, "", mod, rc)
}

func (s *Server) deleteFile(w http.ResponseWriter, r *http.Request, p string) {
	pre, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_if_match", err.Error())
		return
	}
	v, deleted, err := s.store.DeleteFile(r.Context(), principalFrom(r.Context()).User.ID, p, pre)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if len(deleted) > 0 {
		s.changed(r, v)
	}
	out := api.DeleteResponse{Version: v, Deleted: make([]api.File, 0, len(deleted))}
	for _, f := range deleted {
		out.Deleted = append(out.Deleted, toAPIFile(f))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleMove(w http.ResponseWriter, r *http.Request) {
	var req api.MoveRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	from, err := pathutil.Clean(req.From)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_path", "from: "+err.Error())
		return
	}
	to, err := pathutil.Clean(req.To)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_path", "to: "+err.Error())
		return
	}
	if !requireE2EEClient(w, r) || !requireEncryptedPaths(w, r, from, to) {
		return
	}
	pre, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_if_match", err.Error())
		return
	}
	if req.IfMatch != nil {
		if pre.Set && (pre.Any || pre.Version != *req.IfMatch) {
			writeError(w, http.StatusBadRequest, "invalid_if_match", "if_match and the If-Match header disagree")
			return
		}
		pre = meta.Precondition{Set: true, Version: *req.IfMatch}
	}
	v, moved, err := s.store.MoveFile(r.Context(), principalFrom(r.Context()).User.ID, from, to, pre)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if len(moved) > 0 {
		s.changed(r, v)
	}
	out := api.MoveResponse{Version: v, Moved: make([]api.MovedEntry, 0, len(moved))}
	for _, m := range moved {
		out.Moved = append(out.Moved, api.MovedEntry{From: m.From, File: toAPIFile(m.File)})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateFolder: POST /api/v1/folders {"path": "a/b"}. Creates the
// folder and any missing parents; 201 if created, 200 if it already existed.
func (s *Server) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	var req api.CreateFolderRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := pathutil.Clean(req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_path", err.Error())
		return
	}
	if !requireEncryptedPaths(w, r, p) {
		return
	}
	f, created, err := s.store.CreateFolder(r.Context(), principalFrom(r.Context()).User.ID, p)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		s.changed(r, f.Version)
	}
	w.Header().Set("ETag", etag(f.Version))
	writeJSON(w, status, toAPIFile(f))
}

func deviceIDOf(r *http.Request) int64 {
	if d := principalFrom(r.Context()).Device; d != nil {
		return d.ID
	}
	return 0
}

// headBuffer keeps the first max bytes written to it and discards the rest.
type headBuffer struct {
	b   []byte
	max int
}

func (h *headBuffer) Write(p []byte) (int, error) {
	if room := h.max - len(h.b); room > 0 {
		h.b = append(h.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}
