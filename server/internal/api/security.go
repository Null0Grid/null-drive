package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nulldrive/nulldrive/server/internal/meta"
	"github.com/nulldrive/nulldrive/shared/api"
)

// ---- account lockout ----

// Login failures are counted per username (whether or not it exists, so
// the lockout itself doesn't reveal which accounts are real). After
// lockoutThreshold consecutive failures the account is locked for
// lockoutBase, doubling with each further failure up to lockoutMax. A
// success resets the count.
//
// Exponential backoff rather than a hard lock: a hard lock lets anyone
// who knows the admin's username lock them out indefinitely; with backoff
// an attacker gets ~4 guesses per 15 minutes after the first few, while
// the owner can still get in by waiting. Per-IP rate limiting (loginPerIP)
// is the first layer; this one stops a guesser that rotates addresses.
const (
	lockoutThreshold = 5
	lockoutBase      = 30 * time.Second
	lockoutMax       = 15 * time.Minute
	lockoutForget    = 24 * time.Hour
)

type lockout struct {
	now func() time.Time
	mu  sync.Mutex
	m   map[string]*failState
}

type failState struct {
	fails int
	until time.Time
	last  time.Time
}

func newLockout(now func() time.Time) *lockout {
	if now == nil {
		now = time.Now
	}
	return &lockout{now: now, m: map[string]*failState{}}
}

func lockKey(username string) string { return strings.ToLower(strings.TrimSpace(username)) }

// remaining returns how long key is still locked.
func (l *lockout) remaining(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.m[key]
	if f == nil {
		return 0
	}
	if d := f.until.Sub(l.now()); d > 0 {
		return d
	}
	return 0
}

// fail records a failure and returns the new lock duration (0 = not
// locked yet).
func (l *lockout) fail(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.m) > 10000 {
		for k, f := range l.m {
			if now.Sub(f.last) > lockoutForget {
				delete(l.m, k)
			}
		}
	}
	f := l.m[key]
	if f == nil || now.Sub(f.last) > lockoutForget {
		f = &failState{}
		l.m[key] = f
	}
	f.fails++
	f.last = now
	if f.fails < lockoutThreshold {
		return 0
	}
	d := lockoutBase << min(f.fails-lockoutThreshold, 10)
	if d > lockoutMax {
		d = lockoutMax
	}
	f.until = now.Add(d)
	return d
}

func (l *lockout) success(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, key)
}

func retryAfter(w http.ResponseWriter, d time.Duration) {
	secs := int(d.Seconds() + 0.999)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
}

// ---- CSRF ----

// The session cookie is HttpOnly and SameSite=Strict. SameSite already
// stops cross-*site* forgery, but "site" means registrable domain: on a
// homelab, drive.home.example and some-other-app.home.example are the
// same site, so a compromised or malicious sibling app could still send
// the cookie. Hence a token too: state-changing requests authenticated by
// the cookie must carry X-NullDrive-CSRF.
//
// The token is HMAC(server key, session token hash): nothing to store,
// bound to the session, and useless to anyone who can't already read the
// page (the cross-origin attacker can't). Bearer-token requests (desktop
// app, curl) are exempt: a browser never attaches those by itself.

func (s *Server) loadCSRFKey(ctx context.Context) error {
	k, err := s.store.Setting(ctx, "csrf_key")
	if err == nil && len(k) == 64 {
		s.csrfKey, _ = hex.DecodeString(k)
		return nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	if err := s.store.SetSetting(ctx, "csrf_key", hex.EncodeToString(b)); err != nil {
		return err
	}
	s.csrfKey = b
	return nil
}

func (s *Server) csrfToken(sessionTokenHash string) string {
	m := hmac.New(sha256.New, s.csrfKey)
	m.Write([]byte("csrf:" + sessionTokenHash))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func unsafeMethod(m string) bool {
	return m != http.MethodGet && m != http.MethodHead && m != http.MethodOptions
}

func (s *Server) checkCSRF(r *http.Request, sessionTokenHash string) bool {
	got := r.Header.Get(api.HeaderCSRF)
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.csrfToken(sessionTokenHash))) == 1
}

// originGuard rejects state-changing requests that a browser marks as
// coming from another origin. It covers what tokens can't: login and setup
// (no session yet: "login CSRF" would sign the victim into the attacker's
// account) and pairing claims. Non-browser clients send no Origin and no
// Sec-Fetch-Site and pass.
func (s *Server) originGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unsafeMethod(r.Method) && strings.HasPrefix(r.URL.Path, "/api/") {
			if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "same-site" {
				writeError(w, http.StatusForbidden, "bad_origin", "cross-origin requests are not allowed")
				return
			}
			if o := r.Header.Get("Origin"); o != "" {
				u, err := url.Parse(o)
				if err != nil || o == "null" || !strings.EqualFold(u.Host, r.Host) {
					writeError(w, http.StatusForbidden, "bad_origin", "cross-origin requests are not allowed")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ---- headers ----

// securityHeaders sets defaults for every response. The web UI handler
// replaces the CSP with the app's own (webui.csp); API responses get one
// that allows nothing, so a JSON or file response opened directly can't
// run script even if a browser tried to sniff it as HTML.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		if strings.HasPrefix(r.URL.Path, "/api/") || isSearchPath(r.URL.Path) {
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; sandbox")
			// Authenticated JSON must not land in shared caches or the
			// browser's disk cache; handlers that serve immutable chunks
			// override this.
			h.Set("Cache-Control", "no-store")
		}
		// HSTS only when this request really arrived over TLS: sent over
		// plain HTTP it is ignored anyway, and behind a proxy we only
		// trust X-Forwarded-Proto when told to (NULLDRIVE_TRUST_PROXY).
		// No includeSubDomains: other services on the owner's domain may
		// legitimately still be HTTP.
		if r.TLS != nil || (s.trustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// ---- audit ----

const auditMaxAge = 180 * 24 * time.Hour

// audit records an event. Failures are logged, never fatal: the action
// itself already happened.
func (s *Server) audit(r *http.Request, event string, u *meta.User, detail map[string]string) {
	e := meta.AuditEvent{Event: event, IP: s.clientIP(r), Detail: detail}
	if u != nil {
		e.UserID, e.Username = u.ID, u.Username
	}
	if p := principalFrom(r.Context()); p.Device != nil {
		e.DeviceID = p.Device.ID
	}
	ctx := context.WithoutCancel(r.Context())
	if err := s.store.AddAudit(ctx, e); err != nil {
		s.log.Warn("audit log", "event", event, "err", err)
		return
	}
	if s.auditCount.Add(1)%200 == 0 {
		if _, err := s.store.PruneAudit(ctx, auditMaxAge); err != nil {
			s.log.Warn("prune audit log", "err", err)
		}
	}
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if !principalFrom(r.Context()).User.IsAdmin {
		writeError(w, http.StatusForbidden, "admin_required", "only an administrator can view the audit log")
		return
	}
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	evs, err := s.store.ListAudit(r.Context(), before, limit+1)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := api.AuditList{Events: make([]api.AuditEvent, 0, len(evs))}
	if len(evs) > limit {
		evs = evs[:limit]
		next := evs[len(evs)-1].ID
		out.Next = &next
	}
	for _, e := range evs {
		out.Events = append(out.Events, api.AuditEvent{
			ID: e.ID, At: e.At, Event: e.Event, Username: e.Username, DeviceID: e.DeviceID, IP: e.IP, Detail: e.Detail,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
