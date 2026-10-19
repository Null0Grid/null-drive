package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/nulldrive/nulldrive/server/internal/auth"
	"github.com/nulldrive/nulldrive/server/internal/meta"
	"github.com/nulldrive/nulldrive/shared/api"
)

type ctxKey int

const (
	principalKey ctxKey = iota
	requestIDKey
)

// principal is the authenticated caller. Exactly one of SessionID or
// Device is set, depending on which kind of token was presented.
type principal struct {
	User      meta.User
	SessionID int64
	Device    *meta.Device
	// TokenHash is the session token's hash (for the CSRF token).
	TokenHash string
}

func principalFrom(ctx context.Context) principal {
	p, _ := ctx.Value(principalKey).(principal)
	return p
}

// chain applies middleware so the first one listed is the outermost.
func chain(h http.Handler, mw ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				s.log.Error("panic", "err", v, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requestID tags each request with an ID (honouring a sane incoming one,
// e.g. from a reverse proxy) for correlating logs with bug reports.
func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(api.HeaderRequestID)
		if id == "" || len(id) > 64 {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}
		w.Header().Set(api.HeaderRequestID, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		level := slog.LevelInfo
		if rec.status >= 500 {
			level = slog.LevelError
		}
		id, _ := r.Context().Value(requestIDKey).(string)
		s.log.Log(r.Context(), level, "request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", id,
		)
	})
}

// bearerToken extracts a token from "Authorization: Bearer ..." (clients,
// curl) or, failing that, from the session cookie (browser). fromCookie
// tells the CSRF check which kind it got.
func bearerToken(r *http.Request) (tok string, fromCookie bool) {
	if h := r.Header.Get("Authorization"); h != "" {
		if scheme, tok, ok := strings.Cut(h, " "); ok && strings.EqualFold(scheme, "Bearer") {
			return strings.TrimSpace(tok), false
		}
		return "", false
	}
	if c, err := r.Cookie(api.SessionCookieName); err == nil {
		return c.Value, true
	}
	return "", false
}

// requireAuth accepts either a session token or a device token.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, fromCookie := bearerToken(r)
		var p principal
		var err error
		switch {
		case strings.HasPrefix(tok, api.SessionTokenPrefix):
			p.TokenHash = auth.HashToken(tok)
			p.User, p.SessionID, err = s.store.SessionUser(r.Context(), p.TokenHash)
		case strings.HasPrefix(tok, api.DeviceTokenPrefix):
			var d meta.Device
			d, p.User, err = s.store.DeviceByToken(r.Context(), auth.HashToken(tok))
			if err == nil {
				p.Device = &d
				if terr := s.store.TouchDevice(r.Context(), d.ID, s.clientIP(r), clientVersion(r)); terr != nil {
					s.log.Warn("touch device", "err", terr)
				}
			}
		default:
			err = meta.ErrNotFound
		}
		if errors.Is(err, meta.ErrNotFound) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="nulldrive"`)
			writeError(w, http.StatusUnauthorized, "unauthenticated", "missing, invalid or expired token")
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if p.Device != nil && !scopeAllows(p.Device.Scope, r.Method, r.URL.Path) {
			writeError(w, http.StatusForbidden, "insufficient_scope", "this token can only be used for search")
			return
		}
		if fromCookie && unsafeMethod(r.Method) && !s.checkCSRF(r, p.TokenHash) {
			writeError(w, http.StatusForbidden, "csrf_failed", "missing or invalid "+api.HeaderCSRF+" header")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), principalKey, p)))
	}
}

// requireSession is for account-level actions (registering or revoking
// devices) that a synced device should not be able to perform by itself.
func (s *Server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		if principalFrom(r.Context()).Device != nil {
			writeError(w, http.StatusForbidden, "session_required", "this action requires a logged-in user session, not a device token")
			return
		}
		next(w, r)
	})
}

// clientIP is the address of the caller: the TCP peer, or with TrustProxy
// the last X-Forwarded-For hop (the one our proxy appended; earlier
// entries are supplied by the client and can be forged).
func (s *Server) clientIP(r *http.Request) string {
	if s.trustProxy {
		if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
			parts := strings.Split(xff[len(xff)-1], ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// clientVersion extracts the desktop client's product token from its
// User-Agent ("NullDrive/0.5.0 (windows/amd64)"). Other agents are not
// recorded: a browser UA says nothing useful about a sync device.
func clientVersion(r *http.Request) string {
	ua := r.Header.Get("User-Agent")
	if !strings.HasPrefix(ua, "NullDrive/") {
		return ""
	}
	if len(ua) > 100 {
		ua = ua[:100]
	}
	return ua
}
