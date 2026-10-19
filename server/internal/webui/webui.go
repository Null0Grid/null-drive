// Package webui serves the Svelte single-page app embedded into the binary.
//
// `make build` (or `npm run build` in ui/) writes the production bundle to
// server/internal/webui/dist, and go:embed compiles it into the server.
// One binary therefore contains the API and the UI: nothing to deploy next
// to it, and the UI can never be out of sync with the API it talks to.
//
// When the binary is built without the UI (plain `go build`, `go test`),
// dist only holds a .gitkeep and the handler serves a short "UI not built"
// page instead, so the API keeps working.
package webui

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var embedded embed.FS

// CSP for the app shell. Everything is self-hosted (fonts included), so
// nothing needs to be allowed from other origins. 'unsafe-inline' is
// allowed for styles only: Svelte sets some style attributes. Scripts are
// strictly same-origin, which is what actually stops XSS. 'wasm-unsafe-eval'
// lets the page compile WebAssembly (libsodium, for end-to-end encryption
// in the browser); unlike 'unsafe-eval' it does not allow eval() or
// new Function() on JavaScript strings.
const csp = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' blob: data:; font-src 'self'; connect-src 'self'; " +
	"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

const notBuiltPage = `<!doctype html><meta charset="utf-8"><title>Null Drive</title>
<style>body{background:#0a0a0f;color:#c8d3e6;font:16px/1.5 monospace;padding:3rem}code{color:#00e5ff}</style>
<h1>Null Drive</h1><p>The API is running, but this binary was built without the web UI.</p>
<p>Build it with <code>make build</code>, or run <code>make dev</code> for development.</p>`

// Handler serves the SPA.
type Handler struct {
	files   fs.FS
	index   []byte
	modTime time.Time
}

// New returns a handler for the embedded bundle.
func New() *Handler {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err) // embed pattern guarantees dist exists
	}
	return NewFromFS(sub)
}

// NewFromFS serves an arbitrary bundle (used by tests).
func NewFromFS(files fs.FS) *Handler {
	h := &Handler{files: files, modTime: time.Now()}
	if b, err := fs.ReadFile(files, "index.html"); err == nil {
		h.index = b
	}
	return h
}

// Built reports whether a UI bundle is present.
func (h *Handler) Built() bool { return h.index != nil }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	hdr := w.Header()
	hdr.Set("Content-Security-Policy", csp)

	if h.index == nil {
		hdr.Set("Content-Type", "text/html; charset=utf-8")
		hdr.Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(notBuiltPage))
		return
	}

	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name != "" && name != "index.html" {
		if st, err := fs.Stat(h.files, name); err == nil && !st.IsDir() {
			// Vite puts content-hashed files under assets/: a new build
			// produces new names, so they can be cached forever.
			if strings.HasPrefix(name, "assets/") {
				hdr.Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				hdr.Set("Cache-Control", "no-cache")
			}
			http.ServeFileFS(w, r, h.files, name)
			return
		}
		// A missing file with an extension is a real 404 (a broken asset
		// link should fail loudly, not receive index.html).
		if path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
	}
	// SPA fallback: client-side routes (/files/Photos, /settings, ...) all
	// get the app shell, and the Svelte router takes over.
	hdr.Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", h.modTime, bytes.NewReader(h.index))
}
