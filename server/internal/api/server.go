// Package api is the HTTP layer: routing, middleware, and v1 handlers.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Null0Grid/null-drive/server/internal/auth"
	"github.com/Null0Grid/null-drive/server/internal/chunkstore"
	"github.com/Null0Grid/null-drive/server/internal/meta"
	"github.com/Null0Grid/null-drive/server/internal/realtime"
	"github.com/Null0Grid/null-drive/server/internal/search"
	"github.com/Null0Grid/null-drive/server/storage"
)

// DefaultGCGrace: unreferenced chunks are kept this long, so a client that
// uploaded chunks has a day to commit them (resumes across a night).
const DefaultGCGrace = 24 * time.Hour

const (
	filesPrefix = "/api/v1/files/"
	sessionTTL  = 30 * 24 * time.Hour
	maxJSONBody = 1 << 20
)

// Options configures a Server.
type Options struct {
	Store  *meta.Store
	Blobs  storage.Store
	Hasher auth.Hasher
	Logger *slog.Logger
	// DataDir is reported by GET /system/info.
	DataDir string
	// UI serves every non-/api path (the embedded single-page app). If nil,
	// those paths return a JSON 404 like unknown API paths.
	UI http.Handler
	// Hub delivers realtime notifications. If nil, a private hub is
	// created (tests); main passes one so it can shut it down.
	Hub *realtime.Hub
	// TrustProxy makes the server take the client address from the last
	// X-Forwarded-For entry. Only enable it behind a reverse proxy that
	// sets that header, or clients can spoof the address shown for their
	// device and dodge per-IP rate limits.
	TrustProxy bool
	// Discovery is the LAN advertiser (nil = discovery unavailable).
	Discovery Discovery
	// DiscoveryForcedOff reports NULLDRIVE_MDNS=off.
	DiscoveryForcedOff bool
	// Now is the clock for rate limiting (nil = time.Now).
	Now func() time.Time
	// Chunks is the chunk store (nil = built from Blobs and Store).
	Chunks *chunkstore.Store
	// GCGrace is how long unreferenced chunks survive (in-flight uploads).
	GCGrace time.Duration
	// SearchDir holds the per-user search indexes (<user id>.db). Empty
	// disables search (GET /search answers 503).
	SearchDir string
	// SearchOrigins may call GET /search from a browser (CORS), e.g.
	// "https://search.nullgrid.dev".
	SearchOrigins []string
}

// Advert is what the server announces on the LAN.
type Advert struct {
	Enabled   bool
	Name      string
	SetupDone bool
}

// DiscoveryStatus is the advertiser's state, for the settings page.
type DiscoveryStatus struct {
	Active bool
	Addrs  []string
	Err    string
}

// Discovery starts, stops or updates the LAN (mDNS) advertisement.
type Discovery interface {
	Apply(Advert)
	Status() DiscoveryStatus
}

// Server implements http.Handler for the whole API.
type Server struct {
	store     *meta.Store
	blobs     storage.Store
	hasher    auth.Hasher
	log       *slog.Logger
	dataDir   string
	ui        http.Handler
	dummyHash string
	handler   http.Handler

	hub          *realtime.Hub
	trustProxy   bool
	discovery    Discovery
	discoveryOff bool
	claimPerIP   *limiter
	claimGlobal  *limiter
	chunks       *chunkstore.Store
	gcGrace      time.Duration

	loginPerIP *limiter
	lock       *lockout
	csrfKey    []byte
	auditCount atomic.Int64

	search        *search.Manager
	searchOrigins map[string]bool
}

// New builds the server and its middleware chain.
func New(o Options) (*Server, error) {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	// A real hash of a throwaway password, verified against when a login
	// names an unknown user, so "no such user" and "wrong password" take
	// the same time and usernames cannot be enumerated by timing.
	dummy, err := o.Hasher.Hash("nulldrive-timing-equalizer")
	if err != nil {
		return nil, err
	}
	s := &Server{
		store: o.Store, blobs: o.Blobs, hasher: o.Hasher, log: o.Logger,
		dataDir: o.DataDir, ui: o.UI, dummyHash: dummy,
		hub: o.Hub, trustProxy: o.TrustProxy, discovery: o.Discovery, discoveryOff: o.DiscoveryForcedOff,
		// Pairing claims: 5/min per address (a human retyping a code is
		// far below that) and 30/min across all addresses, which caps a
		// distributed guesser at ~150 guesses per 5-minute code lifetime.
		claimPerIP:  newLimiter(5, 5, o.Now),
		claimGlobal: newLimiter(30, 30, o.Now),
		// Logins: 10/min per address. Generous for a person and for a
		// household behind one NAT; slow for a scripted guesser, whose
		// per-account guesses the lockout limits further.
		loginPerIP: newLimiter(10, 10, o.Now),
		lock:       newLockout(o.Now),
	}
	if err := s.loadCSRFKey(context.Background()); err != nil {
		return nil, err
	}
	if s.hub == nil {
		s.hub = realtime.NewHub(o.Logger)
	}
	s.chunks = o.Chunks
	if s.chunks == nil {
		s.chunks = chunkstore.New(o.Blobs, o.Store, o.Logger)
	}
	s.gcGrace = o.GCGrace
	if s.gcGrace <= 0 {
		s.gcGrace = DefaultGCGrace
	}

	if o.SearchDir != "" {
		s.search = search.New(o.SearchDir, o.Store, s.chunks, o.Logger)
	}
	s.searchOrigins = map[string]bool{}
	for _, origin := range o.SearchOrigins {
		s.searchOrigins[strings.ToLower(strings.TrimRight(origin, "/"))] = true
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET "+searchPath, s.requireAuth(s.handleSearch))
	mux.HandleFunc("GET "+searchPathAPIv1, s.requireAuth(s.handleSearch))
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/setup/status", s.handleSetupStatus)
	mux.HandleFunc("POST /api/v1/setup", s.handleSetup)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.requireAuth(s.handleLogout))
	mux.HandleFunc("POST /api/v1/auth/password", s.requireSession(s.handleChangePassword))
	mux.HandleFunc("POST /api/v1/auth/totp/setup", s.requireSession(s.handleTOTPSetup))
	mux.HandleFunc("POST /api/v1/auth/totp/enable", s.requireSession(s.handleTOTPEnable))
	mux.HandleFunc("POST /api/v1/auth/totp/disable", s.requireSession(s.handleTOTPDisable))
	mux.HandleFunc("GET /api/v1/admin/audit", s.requireSession(s.handleAudit))
	mux.HandleFunc("GET /api/v1/system/info", s.handleSystemInfo)
	mux.HandleFunc("GET /api/v1/me", s.requireAuth(s.handleMe))
	mux.HandleFunc("POST /api/v1/devices", s.requireSession(s.handleCreateDevice))
	mux.HandleFunc("GET /api/v1/devices", s.requireAuth(s.handleListDevices))
	mux.HandleFunc("DELETE /api/v1/devices/{id}", s.requireSession(s.handleRevokeDevice))
	mux.HandleFunc("POST /api/v1/pairing", s.requireSession(s.handleCreatePairing))
	mux.HandleFunc("GET /api/v1/pairing/{id}", s.requireSession(s.handleGetPairing))
	mux.HandleFunc("DELETE /api/v1/pairing/{id}", s.requireSession(s.handleCancelPairing))
	mux.HandleFunc("POST /api/v1/pairing/claim", s.handleClaimPairing)
	mux.HandleFunc("GET /api/v1/system/settings", s.requireSession(s.handleGetSettings))
	mux.HandleFunc("PATCH /api/v1/system/settings", s.requireSession(s.handleUpdateSettings))
	mux.HandleFunc("GET /api/v1/ws", s.requireAuth(s.handleWS))
	mux.HandleFunc("GET /api/v1/files", s.requireAuth(s.handleListFiles))
	s.registerV2(mux)
	s.registerE2EE(mux)
	mux.HandleFunc("POST /api/v1/folders", s.requireAuth(s.handleCreateFolder))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if s.ui != nil && r.URL.Path != "/api" && !strings.HasPrefix(r.URL.Path, "/api/") {
			s.ui.ServeHTTP(w, r)
			return
		}
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})

	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// File paths are routed by hand instead of through ServeMux.
		// ServeMux "cleans" paths and answers "a/../b" with a redirect to
		// "b"; for a file API that would silently turn a malicious or
		// buggy path into a different, valid one. Here the raw (escaped)
		// path goes through pathutil.Clean, which rejects it with a 400.
		if strings.HasPrefix(r.URL.EscapedPath(), v2FilesPrefix) {
			s.requireAuth(s.handleV2File)(w, r)
			return
		}
		if esc := r.URL.EscapedPath(); strings.HasPrefix(esc, filesPrefix) {
			if esc == filesPrefix+"move" && r.Method == http.MethodPost {
				s.requireAuth(s.handleMove)(w, r)
				return
			}
			s.requireAuth(s.handleFile)(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})

	s.handler = chain(root, s.recoverer, s.requestID, s.logRequests, s.securityHeaders, s.cors, s.originGuard)
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// Close releases what the server holds open (the search indexes). Call
// it after the HTTP server has shut down.
func (s *Server) Close() error {
	if s.search != nil {
		return s.search.Close()
	}
	return nil
}

// Hub returns the realtime hub (for shutdown).
func (s *Server) Hub() *realtime.Hub { return s.hub }
