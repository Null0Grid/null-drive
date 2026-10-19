package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Null0Grid/null-drive/server/internal/meta"
	"github.com/Null0Grid/null-drive/shared/api"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, api.ErrorBody{Error: api.ErrorDetail{Code: code, Message: msg}})
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	id, _ := r.Context().Value(requestIDKey).(string)
	s.log.Error("internal error", "err", err, "request_id", id, "path", r.URL.Path)
	writeError(w, http.StatusInternalServerError, "internal", "internal server error (request "+id+")")
}

// writeStoreError maps repository errors to HTTP responses.
func (s *Server) writeStoreError(w http.ResponseWriter, r *http.Request, err error) {
	var ce *meta.ConflictError
	switch {
	case errors.As(err, &ce):
		body := api.ErrorBody{Error: api.ErrorDetail{Code: "conflict", Message: ce.Error()}}
		if ce.Current != nil {
			f := toAPIFile(*ce.Current)
			body.Error.Current = &f
			w.Header().Set("ETag", etag(ce.Current.Version))
		}
		writeJSON(w, http.StatusConflict, body)
	case errors.Is(err, meta.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such file or folder")
	case errors.Is(err, meta.ErrPreconditionRequired):
		writeError(w, http.StatusPreconditionRequired, "precondition_required", err.Error())
	case errors.Is(err, meta.ErrTargetExists):
		writeError(w, http.StatusConflict, "target_exists", err.Error())
	case errors.Is(err, meta.ErrPathCollision):
		writeError(w, http.StatusConflict, "path_collision", err.Error())
	case errors.Is(err, meta.ErrInvalidMove):
		writeError(w, http.StatusBadRequest, "invalid_move", err.Error())
	case errors.Is(err, meta.ErrE2EEAlreadyOn):
		writeError(w, http.StatusConflict, "e2ee_already_on", err.Error())
	case errors.Is(err, meta.ErrE2EEOff):
		writeError(w, http.StatusConflict, "e2ee_off", err.Error())
	case errors.Is(err, meta.ErrE2EEAuth):
		writeError(w, http.StatusForbidden, "e2ee_auth_failed", err.Error())
	default:
		s.internalError(w, r, err)
	}
}

// decodeJSON reads a small JSON body strictly: unknown fields are errors so
// client typos surface immediately instead of being silently ignored.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	return decodeJSONLimit(w, r, dst, maxJSONBody)
}

// decodeJSONLimit is decodeJSON with a custom body limit (manifests of
// very large files exceed the 1 MiB default).
func decodeJSONLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", fmt.Sprintf("invalid request body: %v", err))
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be a single JSON object")
		return false
	}
	return true
}

// etag renders a file version as a strong entity tag.
func etag(version int64) string { return `"` + strconv.FormatInt(version, 10) + `"` }

// parseIfMatch accepts `If-Match: "12"` (an ETag as returned by the server)
// or the bare `If-Match: 12` that is friendlier to type in curl.
func parseIfMatch(h string) (meta.Precondition, error) {
	h = strings.TrimSpace(h)
	if h == "" {
		return meta.Precondition{}, nil
	}
	if h == "*" {
		// "Whatever is there": create or overwrite (backups, mirroring).
		return meta.Precondition{Set: true, Any: true}, nil
	}
	if strings.HasPrefix(h, "W/") {
		return meta.Precondition{}, errors.New("weak ETags are not supported")
	}
	v, err := strconv.ParseInt(strings.Trim(h, `"`), 10, 64)
	if err != nil || v < 0 {
		return meta.Precondition{}, fmt.Errorf("If-Match must be a file version, got %q", h)
	}
	return meta.Precondition{Set: true, Version: v}, nil
}

func toAPIFile(f meta.File) api.File {
	return api.File{
		ID: f.UID, Path: f.Path, IsDir: f.IsDir, Size: f.Size, SHA256: f.SHA256, Version: f.Version,
		ModifiedAt: f.ModifiedAt, Deleted: !f.Live(), DeletedAt: f.DeletedAt,
		Mtime: f.Mtime, SyncedID: f.SyncedID, MovedTo: f.MovedTo,
	}
}

// parseWritePrecondition reads the precondition of an upload: If-Match
// (a version, or * for "whatever is there") or If-None-Match: * ("only if
// nothing is there", the standard create-only; same as If-Match: 0).
func parseWritePrecondition(r *http.Request) (meta.Precondition, error) {
	inm := strings.TrimSpace(r.Header.Get("If-None-Match"))
	im := r.Header.Get("If-Match")
	switch {
	case inm == "":
		return parseIfMatch(im)
	case inm != "*":
		return meta.Precondition{}, errors.New(`If-None-Match on an upload must be "*" (create only)`)
	case strings.TrimSpace(im) != "":
		return meta.Precondition{}, errors.New("send If-Match or If-None-Match, not both")
	}
	return meta.Precondition{Set: true, Version: 0}, nil
}

// Sane bounds for a client-reported modification time.
var (
	minMtime = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	maxMtime = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
)

// checkMtime validates a client mtime and truncates it to the stored
// precision (milliseconds), so an unchanged mtime compares equal later.
func checkMtime(t *time.Time) (*time.Time, error) {
	if t == nil {
		return nil, nil
	}
	if t.Before(minMtime) || t.After(maxMtime) {
		return nil, errors.New("mtime must be between 1970 and 9999")
	}
	v := t.UTC().Truncate(time.Millisecond)
	return &v, nil
}

// parseMtimeHeader reads X-NullDrive-Mtime (RFC 3339, optional).
func parseMtimeHeader(h string) (*time.Time, error) {
	h = strings.TrimSpace(h)
	if h == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, h)
	if err != nil {
		return nil, fmt.Errorf("%s must be an RFC 3339 time, e.g. 2026-10-18T09:30:00.123Z", api.HeaderMtime)
	}
	return checkMtime(&t)
}

func toAPIUser(u meta.User) api.User {
	e := u.E2EE
	if e == "" {
		e = meta.E2EEOff
	}
	return api.User{ID: u.ID, Username: u.Username, IsAdmin: u.IsAdmin, CreatedAt: u.CreatedAt, TOTPEnabled: u.TOTPEnabled, E2EE: e}
}

func toAPIDevice(d meta.Device) api.Device {
	return api.Device{
		ID: d.ID, Name: d.Name, CreatedAt: d.CreatedAt, LastSeenAt: d.LastSeenAt, RevokedAt: d.RevokedAt,
		LastIP: d.LastIP, ClientVersion: d.ClientVersion, AddedVia: d.AddedVia, Scope: d.Scope,
	}
}
