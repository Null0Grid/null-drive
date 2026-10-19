package api

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nulldrive/nulldrive/server/internal/auth"
	"github.com/nulldrive/nulldrive/server/internal/buildinfo"
	"github.com/nulldrive/nulldrive/server/internal/meta"
	"github.com/nulldrive/nulldrive/shared/api"
)

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

const (
	minPasswordLen = 8
	maxPasswordLen = 1024 // bound request work; real passphrases are far shorter
	maxDeviceName  = 100
)

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, api.HealthResponse{Status: "ok", Version: buildinfo.Version, Commit: buildinfo.Commit})
}

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	done, err := s.store.SetupComplete(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, api.SetupStatusResponse{SetupComplete: done})
}

func validateCredentials(c api.Credentials) string {
	if !usernameRE.MatchString(c.Username) {
		return "username must be 1-64 characters: letters, digits, '.', '_' or '-'"
	}
	if n := utf8.RuneCountInString(c.Password); n < minPasswordLen || len(c.Password) > maxPasswordLen {
		return "password must be at least 8 characters"
	}
	return ""
}

// handleSetup creates the first admin account. It works exactly once; after
// that it returns 409, so an exposed fresh server cannot be re-claimed.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var req api.Credentials
	if !decodeJSON(w, r, &req) {
		return
	}
	// Cheap check first so a finished server does not burn an argon2 hash
	// per request. The authoritative check is inside CompleteSetup.
	if done, err := s.store.SetupComplete(r.Context()); err != nil {
		s.internalError(w, r, err)
		return
	} else if done {
		writeError(w, http.StatusConflict, "setup_complete", "setup has already been completed")
		return
	}
	if msg := validateCredentials(req); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_credentials", msg)
		return
	}
	hash, err := s.hasher.Hash(req.Password)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	u, err := s.store.CompleteSetup(r.Context(), req.Username, hash)
	if errors.Is(err, meta.ErrSetupComplete) {
		writeError(w, http.StatusConflict, "setup_complete", "setup has already been completed")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.log.Info("setup complete", "admin", u.Username)
	s.audit(r, meta.AuditSetup, &u, nil)
	s.RefreshDiscovery(r.Context()) // TXT record: setup=done
	s.startSession(w, r, u, http.StatusCreated)
}

// handleLogin checks, in order: the per-address rate limit, the
// account's lockout, the password (same work for unknown users), and the
// second factor if the account has one. Wrong passwords and wrong codes
// both count towards the lockout.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if ok, wait := s.loginPerIP.allow(s.clientIP(r)); !ok {
		retryAfter(w, wait)
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many sign-in attempts from this address; wait a minute and try again")
		return
	}
	var req api.Credentials
	if !decodeJSON(w, r, &req) {
		return
	}
	key := lockKey(req.Username)
	if wait := s.lock.remaining(key); wait > 0 {
		retryAfter(w, wait)
		writeError(w, http.StatusTooManyRequests, "locked_out",
			"too many failed sign-ins for this account; try again in "+humanDuration(wait))
		return
	}
	fail := func(u *meta.User, reason, code, msg string) {
		detail := map[string]string{"reason": reason}
		if u == nil {
			detail["username"] = truncate(req.Username, 64)
		}
		s.audit(r, meta.AuditLoginFailed, u, detail)
		if d := s.lock.fail(key); d > 0 {
			s.audit(r, meta.AuditLoginLocked, u, map[string]string{"username": truncate(req.Username, 64), "for": humanDuration(d)})
		}
		writeError(w, http.StatusUnauthorized, code, msg)
	}
	if len(req.Password) > maxPasswordLen {
		fail(nil, "password", "invalid_credentials", "invalid username or password")
		return
	}
	u, err := s.store.UserByUsername(r.Context(), req.Username)
	if err != nil && !errors.Is(err, meta.ErrNotFound) {
		s.internalError(w, r, err)
		return
	}
	stored := u.PasswordHash
	if err != nil {
		stored = s.dummyHash
	}
	ok, verr := s.hasher.Verify(req.Password, stored)
	if verr != nil {
		s.internalError(w, r, verr)
		return
	}
	if err != nil || !ok {
		// Same message for unknown user and wrong password.
		var who *meta.User
		if err == nil {
			who = &u
		}
		fail(who, "password", "invalid_credentials", "invalid username or password")
		return
	}
	if u.TOTPEnabled {
		if strings.TrimSpace(req.TOTP) == "" {
			// Not a failure: the client now asks for the code.
			writeError(w, http.StatusUnauthorized, "totp_required", "enter the code from your authenticator app")
			return
		}
		valid, err := s.checkSecondFactor(r, u, req.TOTP)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if !valid {
			fail(&u, "totp", "invalid_totp", "that code is not valid; check your authenticator app's clock")
			return
		}
	}
	s.lock.success(key)
	s.audit(r, meta.AuditLogin, &u, nil)
	s.startSession(w, r, u, http.StatusOK)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return strconv.Itoa(int(d.Seconds()+0.999)) + " seconds"
	}
	m := int(d.Minutes() + 0.999)
	if m == 1 {
		return "1 minute"
	}
	return strconv.Itoa(m) + " minutes"
}

// startSession issues a session token, returns it in the body (for API
// clients) and also as an HttpOnly cookie (for the browser UI, so page
// JavaScript never has to hold the token).
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u meta.User, status int) {
	tok, hash, err := auth.NewToken(api.SessionTokenPrefix)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	expires, err := s.store.CreateSession(r.Context(), u.ID, hash, sessionTTL)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	setSessionCookie(w, r, tok, expires)
	writeJSON(w, status, api.SessionResponse{Token: tok, ExpiresAt: expires, User: toAPIUser(u), CSRFToken: s.csrfToken(hash)})
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, tok string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     api.SessionCookieName,
		Value:    tok,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		// Strict: the browser never attaches the cookie to cross-site
		// requests. CSRF tokens (security.go) cover same-site origins.
		SameSite: http.SameSiteStrictMode,
	})
}

// handleLogout invalidates the credential used for this request: a session
// is deleted, a device token is revoked ("sign out this device").
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	var err error
	if p.Device != nil {
		_, err = s.store.RevokeDevice(r.Context(), p.User.ID, p.Device.ID)
		s.hub.DisconnectDevice(p.User.ID, p.Device.ID)
		s.audit(r, meta.AuditDeviceRevoked, &p.User, map[string]string{"device": p.Device.Name, "by": "device sign-out"})
	} else {
		err = s.store.DeleteSession(r.Context(), p.SessionID)
		s.hub.DisconnectSessions(p.User.ID, p.SessionID, 0)
		s.audit(r, meta.AuditLogout, &p.User, nil)
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: api.SessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	resp := api.MeResponse{User: toAPIUser(p.User)}
	if p.Device != nil {
		d := toAPIDevice(*p.Device)
		resp.Device = &d
	} else {
		resp.CSRFToken = s.csrfToken(p.TokenHash)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleCreateDevice(w http.ResponseWriter, r *http.Request) {
	var req api.CreateDeviceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || utf8.RuneCountInString(name) > maxDeviceName {
		writeError(w, http.StatusBadRequest, "invalid_name", "device name must be 1-100 characters")
		return
	}
	tok, hash, err := auth.NewToken(api.DeviceTokenPrefix)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	scope := req.Scope
	switch scope {
	case "", meta.ScopeFull:
		scope = meta.ScopeFull
	case meta.ScopeSearch:
	default:
		writeError(w, http.StatusBadRequest, "invalid_scope", `scope must be "full" or "search"`)
		return
	}
	p := principalFrom(r.Context())
	d, err := s.store.CreateScopedDevice(r.Context(), p.User.ID, name, hash, scope)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.log.Info("device registered", "user", p.User.Username, "device_id", d.ID, "name", d.Name, "scope", d.Scope)
	s.audit(r, meta.AuditDeviceAdded, &p.User, map[string]string{"device": d.Name, "via": "password", "scope": d.Scope})
	writeJSON(w, http.StatusCreated, api.CreateDeviceResponse{Device: toAPIDevice(d), Token: tok})
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	ds, err := s.store.ListDevices(r.Context(), principalFrom(r.Context()).User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	online := s.hub.Online(principalFrom(r.Context()).User.ID)
	out := api.DeviceList{Devices: make([]api.Device, 0, len(ds))}
	for _, d := range ds {
		ad := toAPIDevice(d)
		ad.Online = online[d.ID] && d.RevokedAt == nil
		out.Devices = append(out.Devices, ad)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRevokeDevice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "device id must be a number")
		return
	}
	p := principalFrom(r.Context())
	uid := p.User.ID
	d, err := s.store.RevokeDevice(r.Context(), uid, id)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.hub.DisconnectDevice(uid, d.ID)
	s.audit(r, meta.AuditDeviceRevoked, &p.User, map[string]string{"device": d.Name})
	writeJSON(w, http.StatusOK, toAPIDevice(d))
}
