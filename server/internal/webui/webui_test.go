package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSPAHandler(t *testing.T) {
	h := NewFromFS(fstest.MapFS{
		"index.html":           {Data: []byte("<html>app</html>")},
		"assets/app-abc123.js": {Data: []byte("console.log(1)")},
		"theme-init.js":        {Data: []byte("/*theme*/")},
	})
	tests := []struct {
		name, method, path string
		wantStatus         int
		wantBody           string
		wantCache          string
	}{
		{"root", "GET", "/", 200, "app", "no-cache"},
		{"client route", "GET", "/files/Photos/2024", 200, "app", "no-cache"},
		{"settings route", "GET", "/settings", 200, "app", "no-cache"},
		{"hashed asset", "GET", "/assets/app-abc123.js", 200, "console.log", "immutable"},
		{"unhashed asset", "GET", "/theme-init.js", 200, "theme", "no-cache"},
		{"missing asset", "GET", "/assets/gone.js", 404, "", ""},
		{"traversal is confined to the bundle", "GET", "/../../etc/passwd", 200, "app", "no-cache"},
		{"traversal to a real file name", "GET", "/../../etc/hosts.txt", 404, "", ""},
		{"index explicitly", "GET", "/index.html", 200, "app", "no-cache"},
		{"head", "HEAD", "/settings", 200, "", "no-cache"},
		{"post refused", "POST", "/", 405, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("body = %q, want to contain %q", rec.Body.String(), tt.wantBody)
			}
			if tt.wantCache != "" && !strings.Contains(rec.Header().Get("Cache-Control"), tt.wantCache) {
				t.Fatalf("Cache-Control = %q, want %q", rec.Header().Get("Cache-Control"), tt.wantCache)
			}
			if tt.wantStatus == http.StatusOK && !strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'self'") {
				t.Fatal("missing CSP")
			}
		})
	}
}

func TestNotBuilt(t *testing.T) {
	h := NewFromFS(fstest.MapFS{".gitkeep": {}})
	if h.Built() {
		t.Fatal("Built() = true without index.html")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/anything", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "built without the web UI") {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}
