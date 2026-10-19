package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Null0Grid/null-drive/server/internal/auth"
	"github.com/Null0Grid/null-drive/server/internal/meta"
	"github.com/Null0Grid/null-drive/shared/api"
)

const (
	pairingTTL       = 5 * time.Minute
	maxActivePairing = 5
)

// handleCreatePairing: POST /api/v1/pairing (session only). Issues a
// short-lived single-use code. The code is in this response and nowhere
// else: the server keeps only its hash.
func (s *Server) handleCreatePairing(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	code, hash, err := auth.NewPairingCode()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	pr, err := s.store.CreatePairing(r.Context(), p.User.ID, hash, pairingTTL, maxActivePairing)
	if errors.Is(err, meta.ErrTooManyCodes) {
		writeError(w, http.StatusTooManyRequests, "too_many_codes", "too many active pairing codes; cancel one or wait a few minutes")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.log.Info("pairing code issued", "user", p.User.Username, "pairing_id", pr.ID)
	s.audit(r, meta.AuditPairingCreated, &p.User, nil)
	resp := s.pairingResponse(r, pr)
	resp.Code = code
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) pairingResponse(r *http.Request, pr meta.Pairing) api.PairingResponse {
	out := api.PairingResponse{ID: pr.ID, Status: pr.Status(s.store.Now()), ExpiresAt: pr.ExpiresAt}
	if pr.DeviceID != nil {
		if ds, err := s.store.ListDevices(r.Context(), pr.UserID); err == nil {
			for _, d := range ds {
				if d.ID == *pr.DeviceID {
					ad := toAPIDevice(d)
					out.Device = &ad
				}
			}
		}
	}
	return out
}

func (s *Server) pairingByPath(w http.ResponseWriter, r *http.Request, cancel bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "pairing id must be a number")
		return
	}
	uid := principalFrom(r.Context()).User.ID
	var pr meta.Pairing
	if cancel {
		pr, err = s.store.CancelPairing(r.Context(), uid, id)
	} else {
		pr, err = s.store.GetPairing(r.Context(), uid, id)
	}
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.pairingResponse(r, pr))
}

// handleGetPairing: GET /api/v1/pairing/{id}. The "Add device" dialog
// polls this to flip from "waiting" to "Paired with <device>".
func (s *Server) handleGetPairing(w http.ResponseWriter, r *http.Request) {
	s.pairingByPath(w, r, false)
}

// handleCancelPairing: DELETE /api/v1/pairing/{id}. Closing the dialog
// cancels the code so it cannot be used for the rest of its lifetime.
func (s *Server) handleCancelPairing(w http.ResponseWriter, r *http.Request) {
	s.pairingByPath(w, r, true)
}

// handleClaimPairing: POST /api/v1/pairing/claim (no auth: the code is the
// credential). Exchanges a code for a device token.
//
// Rate limiting happens before anything else, including input validation,
// so malformed guesses cost the attacker a token too.
func (s *Server) handleClaimPairing(w http.ResponseWriter, r *http.Request) {
	if !s.allowClaim(w, r) {
		return
	}
	var req api.ClaimPairingRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.DeviceName)
	if name == "" || utf8.RuneCountInString(name) > maxDeviceName {
		writeError(w, http.StatusBadRequest, "invalid_name", "device name must be 1-100 characters")
		return
	}
	norm, ok := auth.NormalizePairingCode(req.Code)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_code", "that pairing code is not valid or has expired")
		return
	}
	tok, tokHash, err := auth.NewToken(api.DeviceTokenPrefix)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	d, u, err := s.store.ClaimPairing(r.Context(), auth.HashPairingCode(norm), name, tokHash)
	if errors.Is(err, meta.ErrInvalidCode) {
		s.log.Info("pairing claim failed", "ip", s.clientIP(r))
		s.audit(r, meta.AuditPairingFailed, nil, nil)
		writeError(w, http.StatusBadRequest, "invalid_code", "that pairing code is not valid or has expired")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.log.Info("device paired", "user", u.Username, "device_id", d.ID, "name", d.Name, "ip", s.clientIP(r))
	s.audit(r, meta.AuditDeviceAdded, &u, map[string]string{"device": d.Name, "via": "pairing"})
	resp := api.ClaimPairingResponse{Device: toAPIDevice(d), Token: tok, User: toAPIUser(u)}
	if u.E2EE != meta.E2EEOff && u.E2EE != "" {
		// The wrapped key bundle rides along; the new device unlocks it
		// with the passphrase or recovery key. Nothing here is a key.
		st, err := s.e2eeStatus(r.Context(), u.ID)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		resp.E2EE = &st
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) allowClaim(w http.ResponseWriter, r *http.Request) bool {
	ok, wait := s.claimPerIP.allow(s.clientIP(r))
	if ok {
		// Only spend a global token for requests the per-IP limit let
		// through, so one noisy address cannot exhaust the global budget
		// for everyone on its own.
		ok, wait = s.claimGlobal.allow("*")
	}
	if ok {
		return true
	}
	secs := int(wait.Seconds() + 0.999)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusTooManyRequests, "rate_limited", "too many pairing attempts; wait a minute and try again")
	return false
}
