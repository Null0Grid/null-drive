package api

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/Null0Grid/null-drive/server/internal/meta"
	"github.com/Null0Grid/null-drive/shared/api"
	"github.com/Null0Grid/null-drive/shared/e2ee"
)

// End-to-end encryption (Phase 7). The client does all the cryptography;
// these endpoints store its opaque key bundle and enforce the one rule
// the server can check: once an account is encrypted, nothing new may be
// written to it in plaintext.
//
//	GET  /api/v2/e2ee          state + key bundle (any token)
//	POST /api/v2/e2ee          turn on (session)
//	PUT  /api/v2/e2ee/bundle   replace the bundle (needs the auth key)
//	POST /api/v2/e2ee/finish   delete remaining plaintext (needs the auth key)

const maxBundle = 8 << 10

var (
	fingerprintRE = regexp.MustCompile(`^[0-9a-f]{16}$`)
	authKeyRE     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func (s *Server) registerE2EE(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v2/e2ee", s.requireAuth(s.handleE2EEStatus))
	mux.HandleFunc("POST /api/v2/e2ee", s.requireSession(s.handleE2EEEnable))
	mux.HandleFunc("PUT /api/v2/e2ee/bundle", s.requireAuth(s.handleE2EEBundle))
	mux.HandleFunc("POST /api/v2/e2ee/finish", s.requireAuth(s.handleE2EEFinish))
}

func (s *Server) e2eeStatus(ctx context.Context, userID int64) (api.E2EEStatus, error) {
	k, err := s.store.E2EEKeys(ctx, userID)
	if err != nil {
		return api.E2EEStatus{}, err
	}
	st := api.E2EEStatus{State: k.State}
	if k.State == meta.E2EEOff {
		return st, nil
	}
	st.Bundle, st.Fingerprint = json.RawMessage(k.Bundle), k.Fingerprint
	enabled := k.EnabledAt
	st.EnabledAt = &enabled
	if k.State == meta.E2EEMigrating {
		if st.PlaintextFiles, err = s.store.CountPlaintext(ctx, userID); err != nil {
			return st, err
		}
	}
	return st, nil
}

func (s *Server) handleE2EEStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.e2eeStatus(r.Context(), principalFrom(r.Context()).User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// validBundle checks shape only: the bundle is client data the server
// can't verify cryptographically. It must be a JSON object whose
// fingerprint matches the one declared alongside it.
func validBundle(raw json.RawMessage, fingerprint string) bool {
	if len(raw) == 0 || len(raw) > maxBundle || !fingerprintRE.MatchString(fingerprint) {
		return false
	}
	var b struct {
		Version     int    `json:"version"`
		Fingerprint string `json:"fingerprint"`
	}
	return json.Unmarshal(raw, &b) == nil && b.Version == e2ee.FormatVersion && b.Fingerprint == fingerprint
}

func (s *Server) handleE2EEEnable(w http.ResponseWriter, r *http.Request) {
	var req api.E2EEEnableRequest
	if !decodeJSONLimit(w, r, &req, maxBundle*2) {
		return
	}
	if !validBundle(req.Bundle, req.Fingerprint) || !authKeyRE.MatchString(req.AuthKey) {
		writeError(w, http.StatusBadRequest, "invalid_bundle", "the key bundle is malformed")
		return
	}
	p := principalFrom(r.Context())
	state, err := s.store.EnableE2EE(r.Context(), p.User.ID, req.Bundle, req.Fingerprint, e2ee.AuthHash(req.AuthKey))
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.audit(r, meta.AuditE2EEEnabled, &p.User, map[string]string{"state": state, "fingerprint": req.Fingerprint})
	// The search index holds the account's names and text in plaintext.
	if s.search != nil {
		if err := s.search.Drop(p.User.ID); err != nil {
			s.log.Error("delete search index after enabling end-to-end encryption", "user", p.User.Username, "err", err)
		}
	}
	s.log.Info("end-to-end encryption turned on", "user", p.User.Username, "state", state)
	st, err := s.e2eeStatus(r.Context(), p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	// Other devices of this account must stop syncing plaintext now.
	if v, err := s.store.CurrentVersion(r.Context()); err == nil {
		s.hub.Notify(p.User.ID, v, 0)
	}
	writeJSON(w, http.StatusCreated, st)
}

func (s *Server) handleE2EEBundle(w http.ResponseWriter, r *http.Request) {
	var req api.E2EEBundleRequest
	if !decodeJSONLimit(w, r, &req, maxBundle*2) {
		return
	}
	if !validBundle(req.Bundle, req.Fingerprint) || !authKeyRE.MatchString(req.AuthKey) {
		writeError(w, http.StatusBadRequest, "invalid_bundle", "the key bundle is malformed")
		return
	}
	p := principalFrom(r.Context())
	if err := s.store.UpdateE2EEBundle(r.Context(), p.User.ID, req.AuthKey, req.Bundle, req.Fingerprint); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.audit(r, meta.AuditE2EEKeysChanged, &p.User, nil)
	st, err := s.e2eeStatus(r.Context(), p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleE2EEFinish deletes everything of the account that isn't stored
// under an encrypted path (the migration's leftovers: plaintext files,
// their history, Recently deleted), deletes the chunks only they used
// right away, and rebuilds the database file, so the data directory holds no
// plaintext of this account afterwards (see docs/SECURITY.md for what
// the filesystem itself may retain).
func (s *Server) handleE2EEFinish(w http.ResponseWriter, r *http.Request) {
	var req api.E2EEFinishRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p := principalFrom(r.Context())
	res, err := s.store.FinishE2EE(r.Context(), p.User.ID, req.AuthKey)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	deleted, freed, err := s.chunks.Purge(r.Context(), res.Chunks)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.store.Compact(r.Context()); err != nil {
		s.log.Warn("compact database after e2ee purge", "err", err)
	}
	s.audit(r, meta.AuditE2EEFinished, &p.User, map[string]string{
		"purged_paths": itoa64(res.Paths), "chunks_deleted": itoa64(int64(deleted)),
	})
	if v, err := s.store.CurrentVersion(r.Context()); err == nil {
		s.hub.Notify(p.User.ID, v, 0)
	}
	writeJSON(w, http.StatusOK, api.E2EEFinishResponse{
		State: meta.E2EEOn, PurgedPaths: res.Paths, ChunksDeleted: deleted, BytesFreed: freed,
	})
}

// ---- enforcement ----

func encrypted(r *http.Request) bool {
	st := principalFrom(r.Context()).User.E2EE
	return st == meta.E2EEMigrating || st == meta.E2EEOn
}

// requireE2EEClient: listings of an encrypted account go only to clients
// that say they can decrypt them. An old desktop client would otherwise
// create files named "~kD3x…" full of ciphertext; this way it stops with
// an explanation instead.
func requireE2EEClient(w http.ResponseWriter, r *http.Request) bool {
	if encrypted(r) && r.Header.Get(api.HeaderE2EE) != "1" {
		writeError(w, http.StatusConflict, "e2ee_client_required",
			"this account uses end-to-end encryption; update Null Drive on this device to a version that supports it")
		return false
	}
	return true
}

// refuseWholeFile: v1 uploads would send plaintext to the server (it
// chunks them itself) and v1 downloads would return ciphertext.
func refuseWholeFile(w http.ResponseWriter, r *http.Request) bool {
	if encrypted(r) {
		writeError(w, http.StatusConflict, "e2ee_required",
			"this account uses end-to-end encryption; files are encrypted on your device and transferred in chunks (API v2)")
		return false
	}
	return true
}

// requireEncryptedPaths: every name written to an encrypted account must
// be an encrypted name. Move sources too: renaming a plaintext leftover to
// an encrypted name would smuggle its plaintext content past the purge.
func requireEncryptedPaths(w http.ResponseWriter, r *http.Request, paths ...string) bool {
	if !encrypted(r) {
		return true
	}
	for _, p := range paths {
		if !e2ee.IsEncryptedPath(p) {
			writeError(w, http.StatusConflict, "e2ee_path_required",
				"this account uses end-to-end encryption; names must be encrypted on your device")
			return false
		}
	}
	return true
}

func itoa64(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
