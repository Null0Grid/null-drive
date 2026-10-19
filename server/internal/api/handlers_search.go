package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/nulldrive/nulldrive/server/internal/meta"
	"github.com/nulldrive/nulldrive/server/internal/search"
)

// Search (Null franchise search contract v1). GET /search is the contract
// path; /api/v1/search is the same handler under the API prefix for
// reverse proxies that only forward /api/.
const (
	searchPath      = "/search"
	searchPathAPIv1 = "/api/v1/search"
)

func isSearchPath(p string) bool { return p == searchPath || p == searchPathAPIv1 }

// scopeAllows reports whether a device token of the given scope may call
// path. Search-only tokens (the ones pasted into Null Search, a web page
// that keeps them in browser storage) can search and check who they
// belong to, nothing else: a leaked one can't download, change or delete
// a single file.
func scopeAllows(scope, method, path string) bool {
	if scope != meta.ScopeSearch {
		return true
	}
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	return isSearchPath(path) || path == "/api/v1/me"
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if s.search == nil {
		writeError(w, http.StatusServiceUnavailable, "search_disabled", "search is not enabled on this server")
		return
	}
	q := r.URL.Query()
	limit := search.DefaultLimit
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be a positive number")
			return
		}
		limit = min(n, search.MaxLimit)
	}
	query := q.Get("q")
	if len(query) > 1000 {
		writeError(w, http.StatusBadRequest, "query_too_long", "the query is too long")
		return
	}
	// The user comes from the token, never from the request: this is the
	// only index the request can reach.
	p := principalFrom(r.Context())
	res, err := s.search.Search(r.Context(), p.User.ID, query, limit)
	if errors.Is(err, search.ErrEncrypted) {
		// Nothing the server could search; an empty list, not an error, so
		// Null Search just shows no Drive results.
		w.Header().Set("X-NullDrive-Search", "encrypted")
		writeJSON(w, http.StatusOK, res)
		return
	}
	if err != nil {
		if r.Context().Err() != nil {
			return // the caller gave up (Null Search times out after ~1 s)
		}
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// cors lets the Null Search web app (its origins come from
// NULLDRIVE_SEARCH_ORIGINS) call the search endpoint from the browser.
// Only the search paths, only listed origins, only GET with an
// Authorization header, and no cookies (no Allow-Credentials): a page on
// another origin can use a token it was given, never ride the user's
// web session.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || !isSearchPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Add("Vary", "Origin")
		allowed := s.searchOrigins[strings.ToLower(origin)]
		preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
		if preflight {
			h.Add("Vary", "Access-Control-Request-Method")
			h.Add("Vary", "Access-Control-Request-Headers")
			h.Add("Vary", "Access-Control-Request-Private-Network")
			if !allowed {
				writeError(w, http.StatusForbidden, "bad_origin", "this origin may not call the search API")
				return
			}
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Methods", "GET")
			h.Set("Access-Control-Allow-Headers", "Authorization")
			h.Set("Access-Control-Max-Age", "600")
			// Private Network Access: Null Search is a public site and a
			// home server usually sits on a LAN address. (Chrome's newer
			// Local Network Access asks the user instead, which this header
			// doesn't affect.)
			if strings.EqualFold(r.Header.Get("Access-Control-Request-Private-Network"), "true") {
				h.Set("Access-Control-Allow-Private-Network", "true")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if allowed {
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Expose-Headers", "X-NullDrive-Search, X-Request-Id")
		}
		next.ServeHTTP(w, r)
	})
}
