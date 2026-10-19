package api

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nulldrive/nulldrive/server/internal/chunkstore"
	"github.com/nulldrive/nulldrive/server/internal/meta"
	"github.com/nulldrive/nulldrive/server/internal/noteid"
	"github.com/nulldrive/nulldrive/server/storage"
	"github.com/nulldrive/nulldrive/shared/api"
	"github.com/nulldrive/nulldrive/shared/e2ee"
	"github.com/nulldrive/nulldrive/shared/pathutil"
)

// The v2 API (Phase 6) moves file content as content-defined chunks:
//
//	POST /api/v2/chunks/check      which of these hashes must I upload?
//	PUT  /api/v2/chunks/{hash}     upload one chunk (verified against its hash)
//	PUT  /api/v2/files/{path}      commit a manifest as the new version (If-Match)
//	GET  /api/v2/files/{path}/manifest
//	GET  /api/v2/chunks/{hash}
//
// plus version history, the recently-deleted bin and storage stats. v1
// keeps working unchanged (its uploads are chunked server-side), so old
// desktop clients and scripts don't break.

const (
	v2FilesPrefix  = "/api/v2/files/"
	maxCheckHashes = 20000 // a 40 GB file at the 2 MiB average chunk size
	maxManifest    = 8 << 20
)

func (s *Server) registerV2(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v2/chunks/check", s.requireAuth(s.handleChunkCheck))
	mux.HandleFunc("PUT /api/v2/chunks/{hash}", s.requireAuth(s.handleChunkPut))
	mux.HandleFunc("GET /api/v2/chunks/{hash}", s.requireAuth(s.handleChunkGet))
	mux.HandleFunc("HEAD /api/v2/chunks/{hash}", s.requireAuth(s.handleChunkGet))
	mux.HandleFunc("GET /api/v2/ids/{id}", s.requireAuth(s.handleFileByID))
	mux.HandleFunc("GET /api/v2/trash", s.requireAuth(s.handleTrash))
	mux.HandleFunc("GET /api/v2/stats", s.requireAuth(s.handleStats))
	mux.HandleFunc("POST /api/v2/admin/gc", s.requireSession(s.handleGC))
}

func (s *Server) handleChunkCheck(w http.ResponseWriter, r *http.Request) {
	var req api.ChunkCheckRequest
	if !decodeJSONLimit(w, r, &req, maxManifest) {
		return
	}
	if len(req.Hashes) > maxCheckHashes {
		writeError(w, http.StatusBadRequest, "too_many_hashes", "at most 20000 hashes per request")
		return
	}
	for _, h := range req.Hashes {
		if !storage.ValidHash(h) {
			writeError(w, http.StatusBadRequest, "invalid_sha256", "hashes must be 64 lowercase hex characters")
			return
		}
	}
	missing, err := s.store.MissingChunks(r.Context(), principalFrom(r.Context()).User.ID, req.Hashes)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, api.ChunkCheckResponse{Missing: missing})
}

func (s *Server) handleChunkPut(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if !storage.ValidHash(hash) {
		writeError(w, http.StatusBadRequest, "invalid_sha256", "chunk hash must be 64 lowercase hex characters")
		return
	}
	body := http.MaxBytesReader(w, r.Body, chunkstore.MaxChunkSize)
	size, err := s.chunks.PutChunk(r.Context(), principalFrom(r.Context()).User.ID, hash, body)
	var mbe *http.MaxBytesError
	switch {
	case errors.Is(err, storage.ErrHashMismatch):
		writeError(w, http.StatusBadRequest, "hash_mismatch", "chunk content does not match its hash")
		return
	case errors.As(err, &mbe):
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "a chunk may be at most 16 MiB")
		return
	case err != nil:
		if r.Context().Err() != nil {
			return
		}
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, api.ChunkRef{Hash: hash, Size: size})
}

func (s *Server) handleChunkGet(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if !storage.ValidHash(hash) {
		writeError(w, http.StatusBadRequest, "invalid_sha256", "chunk hash must be 64 lowercase hex characters")
		return
	}
	// Only chunks of the caller's own files: chunk hashes aren't secrets
	// (a manifest shared elsewhere, a known public file), so knowing one
	// must not be enough to download it.
	_, ok, err := s.store.CanReadChunk(r.Context(), principalFrom(r.Context()).User.ID, hash)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such chunk")
		return
	}
	rc, _, err := s.blobs.Open(hash)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer rc.Close()
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("ETag", `"`+hash+`"`)
	// Content-addressed: the bytes behind a hash never change.
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeContent(w, r, "", time.Time{}, rc)
}

// v2FileOp is a parsed /api/v2/files/... URL. Operations are suffixes
// read from the right, because a path may itself contain "versions" or
// "manifest" segments: GET .../a/manifest is always the manifest of "a";
// the manifest of a file literally named "manifest" is .../manifest/manifest.
type v2FileOp struct {
	path    string
	op      string // commit | manifest | versions | version-content | version-manifest | restore
	version int64
}

func parseV2FilePath(r *http.Request) (v2FileOp, error) {
	raw, err := url.PathUnescape(strings.TrimPrefix(r.URL.EscapedPath(), v2FilesPrefix))
	if err != nil {
		return v2FileOp{}, pathutil.ErrInvalid
	}
	segs := strings.Split(raw, "/")
	n := len(segs)
	take := func(k int, op string, ver string) (v2FileOp, error) {
		p, err := pathutil.Clean(strings.Join(segs[:n-k], "/"))
		if err != nil {
			return v2FileOp{}, err
		}
		o := v2FileOp{path: p, op: op}
		if ver != "" {
			if o.version, err = strconv.ParseInt(ver, 10, 64); err != nil || o.version <= 0 {
				return v2FileOp{}, errors.New("version must be a positive integer")
			}
		}
		return o, nil
	}
	switch {
	case r.Method == http.MethodPut:
		return take(0, "commit", "")
	case r.Method == http.MethodPost && n >= 4 && segs[n-3] == "versions" && segs[n-1] == "restore":
		return take(3, "restore", segs[n-2])
	case r.Method != http.MethodGet && r.Method != http.MethodHead:
		return v2FileOp{}, errMethod
	case n >= 2 && segs[n-1] == "manifest" && (n < 4 || segs[n-3] != "versions"):
		return take(1, "manifest", "")
	case n >= 2 && segs[n-1] == "versions":
		return take(1, "versions", "")
	case n >= 4 && segs[n-3] == "versions" && segs[n-1] == "content":
		return take(3, "version-content", segs[n-2])
	case n >= 4 && segs[n-3] == "versions" && segs[n-1] == "manifest":
		return take(3, "version-manifest", segs[n-2])
	}
	return v2FileOp{}, errNoSuchOp
}

var (
	errMethod   = errors.New("method not allowed")
	errNoSuchOp = errors.New("unknown file operation; see docs/API.md (v2)")
)

func (s *Server) handleV2File(w http.ResponseWriter, r *http.Request) {
	op, err := parseV2FilePath(r)
	switch {
	case errors.Is(err, errMethod):
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	case errors.Is(err, errNoSuchOp):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, "invalid_path", err.Error())
		return
	}
	if !requireE2EEClient(w, r) {
		return
	}
	switch op.op {
	case "commit":
		s.v2Commit(w, r, op.path)
	case "manifest":
		s.v2Manifest(w, r, op.path, 0)
	case "version-manifest":
		s.v2Manifest(w, r, op.path, op.version)
	case "versions":
		s.v2Versions(w, r, op.path)
	case "version-content":
		s.v2VersionContent(w, r, op.path, op.version)
	case "restore":
		s.v2Restore(w, r, op.path, op.version)
	}
}

func (s *Server) v2Commit(w http.ResponseWriter, r *http.Request, p string) {
	if !requireEncryptedPaths(w, r, p) {
		return
	}
	pre, err := parseWritePrecondition(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_if_match", err.Error())
		return
	}
	var req api.CommitRequest
	if !decodeJSONLimit(w, r, &req, maxManifest) {
		return
	}
	req.SHA256 = strings.ToLower(req.SHA256)
	if !storage.ValidHash(req.SHA256) || req.Size < 0 {
		writeError(w, http.StatusBadRequest, "invalid_manifest", "sha256 must be 64 hex characters and size >= 0")
		return
	}
	chunks := make([]meta.ChunkRef, len(req.Chunks))
	for i, c := range req.Chunks {
		if !storage.ValidHash(c.Hash) {
			writeError(w, http.StatusBadRequest, "invalid_manifest", "chunk hashes must be 64 lowercase hex characters")
			return
		}
		chunks[i] = meta.ChunkRef{Hash: c.Hash, Size: c.Size}
	}
	mtime, err := checkMtime(req.Mtime)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_mtime", err.Error())
		return
	}
	var overhead int64
	if encrypted(r) {
		overhead = e2ee.ChunkOverhead
	}
	uid := principalFrom(r.Context()).User.ID
	f, created, err := s.store.CommitFile(r.Context(), uid, meta.Commit{
		Path: p, SHA256: req.SHA256, Size: req.Size, Chunks: chunks, DeviceID: deviceIDOf(r), Overhead: overhead,
		Mtime: mtime,
	}, pre)
	var mce *meta.MissingChunksError
	switch {
	case errors.As(err, &mce):
		// 422: the request is well-formed but refers to data the server
		// doesn't have for this user. The client uploads these and retries.
		writeJSON(w, http.StatusUnprocessableEntity, struct {
			api.ErrorBody
			Missing []string `json:"missing"`
		}{api.ErrorBody{Error: api.ErrorDetail{Code: "missing_chunks", Message: mce.Error()}}, mce.Hashes})
		return
	case errors.Is(err, meta.ErrManifestMismatch):
		writeError(w, http.StatusBadRequest, "invalid_manifest", err.Error())
		return
	case err != nil:
		s.writeStoreError(w, r, err)
		return
	}
	// Markdown on a plain account: read the note id from the frontmatter,
	// now that the commit proved the user owns these chunks.
	if noteid.IsMarkdown(p) && !encrypted(r) && f.VersionID != 0 {
		rc := s.chunks.Open(chunks)
		head, rerr := io.ReadAll(io.LimitReader(rc, noteid.MaxRead))
		_ = rc.Close()
		if rerr != nil {
			s.log.Warn("read note id", "path", p, "err", rerr)
		} else if id := noteid.ID(p, head); id != f.SyncedID {
			if nf, err := s.store.SetSyncedID(r.Context(), f.ID, f.VersionID, id); err != nil {
				s.log.Warn("store note id", "path", p, "err", err)
			} else {
				f = nf
			}
		}
	}
	w.Header().Set("ETag", etag(f.Version))
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	s.changedIfNew(r, f)
	writeJSON(w, status, toAPIFile(f))
}

// changedIfNew notifies other devices unless the commit was a no-op
// retry (whose version is older than the current counter).
func (s *Server) changedIfNew(r *http.Request, f meta.File) {
	if cur, err := s.store.CurrentVersion(r.Context()); err == nil && cur == f.Version {
		s.changed(r, f.Version)
	}
}

func (s *Server) v2Manifest(w http.ResponseWriter, r *http.Request, p string, version int64) {
	uid := principalFrom(r.Context()).User.ID
	var out api.Manifest
	var vid int64
	if version == 0 {
		f, err := s.store.GetFile(r.Context(), uid, p)
		if err == nil && (!f.Live() || f.IsDir) {
			err = meta.ErrNotFound
		}
		if err != nil {
			s.writeStoreError(w, r, err)
			return
		}
		out = api.Manifest{Path: f.Path, Version: f.Version, Size: f.Size, SHA256: f.SHA256,
			ID: f.UID, Mtime: f.Mtime, SyncedID: f.SyncedID}
		vid = f.VersionID
		w.Header().Set("ETag", etag(f.Version))
	} else {
		v, err := s.store.GetVersion(r.Context(), uid, p, version)
		if err != nil {
			s.writeStoreError(w, r, err)
			return
		}
		out = api.Manifest{Path: v.Path, Version: v.Version, Size: v.Size, SHA256: v.SHA256,
			Mtime: v.Mtime, SyncedID: v.SyncedID}
		vid = v.ID
	}
	chunks, err := s.store.Manifest(r.Context(), vid)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out.Chunks = make([]api.ChunkRef, len(chunks))
	for i, c := range chunks {
		out.Chunks[i] = api.ChunkRef{Hash: c.Hash, Size: c.Size}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) v2Versions(w http.ResponseWriter, r *http.Request, p string) {
	uid := principalFrom(r.Context()).User.ID
	vs, err := s.store.ListVersions(r.Context(), uid, p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if len(vs) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "no history for this path")
		return
	}
	cur, err := s.store.GetFile(r.Context(), uid, p)
	if err != nil && !errors.Is(err, meta.ErrNotFound) {
		s.internalError(w, r, err)
		return
	}
	out := api.VersionList{Path: p, Deleted: err != nil || !cur.Live(), Versions: make([]api.FileVersion, 0, len(vs))}
	for _, v := range vs {
		out.Versions = append(out.Versions, api.FileVersion{
			Version: v.Version, Size: v.Size, SHA256: v.SHA256, CreatedAt: v.CreatedAt,
			Device: v.DeviceName, DeviceID: v.DeviceID, Origin: v.Origin, Chunks: v.Chunks,
			Current: err == nil && cur.Live() && cur.VersionID == v.ID,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) v2VersionContent(w http.ResponseWriter, r *http.Request, p string, version int64) {
	if !refuseWholeFile(w, r) {
		return
	}
	v, err := s.store.GetVersion(r.Context(), principalFrom(r.Context()).User.ID, p, version)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.serveVersion(w, r, v.Path, v.Version, v.ID, v.SHA256, v.CreatedAt)
}

func (s *Server) v2Restore(w http.ResponseWriter, r *http.Request, p string, version int64) {
	if !requireEncryptedPaths(w, r, p) {
		return
	}
	pre, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_if_match", err.Error())
		return
	}
	f, err := s.store.RestoreVersion(r.Context(), principalFrom(r.Context()).User.ID, p, version, pre, deviceIDOf(r))
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.changedIfNew(r, f)
	w.Header().Set("ETag", etag(f.Version))
	writeJSON(w, http.StatusOK, toAPIFile(f))
}

func (s *Server) handleTrash(w http.ResponseWriter, r *http.Request) {
	if !requireE2EEClient(w, r) {
		return
	}
	items, err := s.store.Trash(r.Context(), principalFrom(r.Context()).User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := api.TrashList{Items: make([]api.TrashItem, 0, len(items))}
	for _, it := range items {
		out.Items = append(out.Items, api.TrashItem{
			Path: it.Path, Size: it.Size, DeletedAt: it.DeletedAt, Version: it.Version, Versions: it.Versions,
			PurgeAt: it.PurgeAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	st, err := s.store.Stats(r.Context(), p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	pol, err := s.store.Retention(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := api.Stats{
		Files: st.Files, LogicalBytes: st.LogicalBytes, HistoryBytes: st.HistoryBytes, Versions: st.Versions,
		StoredBytes: st.UniqueBytes, TrashFiles: st.TrashFiles,
		RetentionFullDays: pol.FullDays, RetentionDailyDays: pol.DailyDays, TrashDays: pol.TrashDays,
	}
	if p.User.IsAdmin {
		out.ServerStoredBytes, out.ServerLogicalBytes = st.ServerStored, st.ServerLogical
		out.ServerChunks, out.ServerReclaimable = st.ServerChunks, st.UnreferencedB
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGC: POST /api/v2/admin/gc runs retention + garbage collection now
// (it also runs hourly in the background).
func (s *Server) handleGC(w http.ResponseWriter, r *http.Request) {
	if !principalFrom(r.Context()).User.IsAdmin {
		writeError(w, http.StatusForbidden, "admin_required", "only an administrator can run garbage collection")
		return
	}
	rep, err := s.chunks.Collect(r.Context(), s.gcGrace)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	u := principalFrom(r.Context()).User
	s.audit(r, meta.AuditGC, &u, map[string]string{"chunks_deleted": itoa64(int64(rep.ChunksDeleted))})
	writeJSON(w, http.StatusOK, api.GCReport{
		VersionsPruned: rep.VersionsPruned, UploadsExpired: rep.UploadsExpired, ChunksDeleted: rep.ChunksDeleted,
		OrphansDeleted: rep.OrphansDeleted, BytesFreed: rep.BytesFreed, DurationMS: rep.Duration.Milliseconds(),
	})
}

// Chunks exposes the chunk store (main runs its background collector).
func (s *Server) Chunks() *chunkstore.Store { return s.chunks }

// fileIDRE: a file's stable id (meta.NewUID: 16 random bytes, hex).
var fileIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// handleFileByID resolves a stable file id (search results, nulldrive://
// links) to the file's current path. Deleted files are returned too, with
// "deleted": true, so a caller can say "it's in Recently deleted" rather
// than "not found".
func (s *Server) handleFileByID(w http.ResponseWriter, r *http.Request) {
	if !requireE2EEClient(w, r) {
		return
	}
	id := strings.ToLower(r.PathValue("id"))
	if !fileIDRE.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid_id", "not a file id")
		return
	}
	f, err := s.store.FileByUID(r.Context(), principalFrom(r.Context()).User.ID, id)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIFile(f))
}
