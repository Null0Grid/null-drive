package api

import (
	"errors"
	"net/http"

	"github.com/Null0Grid/null-drive/server/internal/buildinfo"
	"github.com/Null0Grid/null-drive/server/internal/meta"
	"github.com/Null0Grid/null-drive/server/internal/sysinfo"
	"github.com/Null0Grid/null-drive/shared/api"
)

// handleSystemInfo: GET /api/v1/system/info.
//
// Before setup is complete it is public, because the setup wizard shows the
// storage location before anyone can log in. After setup it requires
// authentication: the data dir path and disk size are host details an
// anonymous visitor should not learn.
func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	done, err := s.store.SetupComplete(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if done {
		s.requireAuth(s.writeSystemInfo)(w, r)
		return
	}
	s.writeSystemInfo(w, r)
}

func (s *Server) writeSystemInfo(w http.ResponseWriter, _ *http.Request) {
	info := api.SystemInfo{Version: buildinfo.Version, DataDir: s.dataDir}
	if d, err := sysinfo.DiskUsage(s.dataDir); err != nil {
		info.DiskError = err.Error()
	} else {
		info.DiskTotalBytes, info.DiskFreeBytes = d.TotalBytes, d.FreeBytes
	}
	writeJSON(w, http.StatusOK, info)
}

// handleChangePassword: POST /api/v1/auth/password. Requires the current
// password even though the caller is logged in, so an unattended unlocked
// browser cannot be used to lock the owner out.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req api.ChangePasswordRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p := principalFrom(r.Context())
	user, err := s.store.UserByID(r.Context(), p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if len(req.CurrentPassword) > maxPasswordLen {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect")
		return
	}
	ok, err := s.hasher.Verify(req.CurrentPassword, user.PasswordHash)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect")
		return
	}
	if msg := validateCredentials(api.Credentials{Username: user.Username, Password: req.NewPassword}); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_credentials", msg)
		return
	}
	hash, err := s.hasher.Hash(req.NewPassword)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.store.UpdatePassword(r.Context(), user.ID, hash, p.SessionID); err == nil {
		// Other sessions were just deleted; close their live sockets too.
		s.hub.DisconnectSessions(user.ID, 0, p.SessionID)
		s.audit(r, meta.AuditPasswordChanged, &user, nil)
	} else {
		if errors.Is(err, meta.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "user not found")
			return
		}
		s.internalError(w, r, err)
		return
	}
	s.log.Info("password changed", "user", user.Username)
	w.WriteHeader(http.StatusNoContent)
}
