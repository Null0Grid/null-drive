package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/nulldrive/nulldrive/server/internal/auth"
	"github.com/nulldrive/nulldrive/server/internal/meta"
	"github.com/nulldrive/nulldrive/shared/api"
)

// Two-factor authentication: enrol (setup → confirm with a code → get
// recovery codes), use at login (handleLogin), and turn off. Device
// tokens are not affected: devices are added from an already signed-in
// session (pairing) or with password + code (sign-in on the device).

const recoveryCodeCount = 10

// checkSecondFactor verifies a TOTP code (consuming its time step) or a
// recovery code (consuming the code).
func (s *Server) checkSecondFactor(r *http.Request, u meta.User, code string) (bool, error) {
	st, err := s.store.TOTP(r.Context(), u.ID)
	if err != nil || st.Secret == "" {
		return false, err
	}
	if digits := strings.ReplaceAll(strings.TrimSpace(code), " ", ""); len(digits) == auth.TOTPDigits {
		step, ok := auth.VerifyTOTP(st.Secret, digits, s.store.Now(), st.LastStep)
		if !ok {
			return false, nil
		}
		return s.store.UseTOTPStep(r.Context(), u.ID, step)
	}
	ok, err := s.store.UseRecoveryCode(r.Context(), u.ID, auth.HashRecoveryCode(code))
	if ok {
		left, _ := s.store.RecoveryCodesLeft(context.WithoutCancel(r.Context()), u.ID)
		s.audit(r, meta.AuditTOTPRecoveryUsed, &u, map[string]string{"remaining": strconv.Itoa(left)})
	}
	return ok, err
}

func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	u := principalFrom(r.Context()).User
	if u.TOTPEnabled {
		writeError(w, http.StatusConflict, "totp_enabled", "two-factor authentication is already on; turn it off first to set up a new app")
		return
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.store.SetTOTPPending(r.Context(), u.ID, secret); err != nil {
		s.internalError(w, r, err)
		return
	}
	issuer := "Null Drive"
	if set, err := s.loadSettings(r.Context()); err == nil && set.ServerName != "" {
		issuer = "Null Drive (" + set.ServerName + ")"
	}
	writeJSON(w, http.StatusOK, api.TOTPSetupResponse{Secret: secret, URI: auth.TOTPURI(issuer, u.Username, secret)})
}

func (s *Server) handleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	var req api.TOTPCodeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	u := principalFrom(r.Context()).User
	st, err := s.store.TOTP(r.Context(), u.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if st.Pending == "" {
		writeError(w, http.StatusConflict, "totp_not_pending", "start two-factor setup first")
		return
	}
	step, ok := auth.VerifyTOTP(st.Pending, req.Code, s.store.Now(), 0)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_totp", "that code doesn't match; check that your phone's clock is set automatically")
		return
	}
	codes, err := auth.NewRecoveryCodes(recoveryCodeCount)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = auth.HashRecoveryCode(c)
	}
	if err := s.store.EnableTOTP(r.Context(), u.ID, step, hashes); err != nil {
		if errors.Is(err, meta.ErrNotFound) {
			writeError(w, http.StatusConflict, "totp_not_pending", "start two-factor setup first")
			return
		}
		s.internalError(w, r, err)
		return
	}
	s.audit(r, meta.AuditTOTPEnabled, &u, nil)
	writeJSON(w, http.StatusOK, api.TOTPEnableResponse{RecoveryCodes: codes})
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	var req api.TOTPDisableRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p := principalFrom(r.Context())
	u, err := s.store.UserByID(r.Context(), p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !u.TOTPEnabled {
		writeError(w, http.StatusConflict, "totp_disabled", "two-factor authentication is not on")
		return
	}
	key := lockKey(u.Username)
	if wait := s.lock.remaining(key); wait > 0 {
		retryAfter(w, wait)
		writeError(w, http.StatusTooManyRequests, "locked_out", "too many failed attempts; try again in "+humanDuration(wait))
		return
	}
	ok := len(req.Password) <= maxPasswordLen
	if ok {
		if ok, err = s.hasher.Verify(req.Password, u.PasswordHash); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	if ok {
		if ok, err = s.checkSecondFactor(r, u, req.Code); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	if !ok {
		// A stolen session must not be able to brute-force its way to
		// removing the second factor: these failures count too.
		s.lock.fail(key)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "password or code is incorrect")
		return
	}
	if err := s.store.DisableTOTP(r.Context(), u.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r, meta.AuditTOTPDisabled, &u, nil)
	w.WriteHeader(http.StatusNoContent)
}
