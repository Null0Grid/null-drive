package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"nas:8080", "http://nas:8080"},
		{"  http://192.168.1.20:8080/ ", "http://192.168.1.20:8080"},
		{"https://drive.example.com", "https://drive.example.com"},
		{"https://example.com/nulldrive/?x=1#y", "https://example.com/nulldrive"},
	}
	for _, tt := range tests {
		got, err := NormalizeURL(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("NormalizeURL(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
	for _, bad := range []string{"", "ftp://x", "http://"} {
		if _, err := NormalizeURL(bad); err == nil {
			t.Errorf("NormalizeURL(%q) accepted", bad)
		}
	}
}

func TestEncodePath(t *testing.T) {
	if got := EncodePath("My Docs/a#b?.txt"); got != "My%20Docs/a%23b%3F.txt" {
		t.Fatal(got)
	}
}

func TestErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/files/conflict.txt":
			if r.Header.Get("If-Match") != `"3"` || r.Header.Get("X-Content-SHA256") != "abc" || r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(400)
				return
			}
			w.WriteHeader(409)
			_, _ = io.WriteString(w, `{"error":{"code":"conflict","message":"changed","current":{"path":"conflict.txt","version":7}}}`)
		case "/api/v1/files/plain":
			w.WriteHeader(502)
			_, _ = io.WriteString(w, "bad gateway")
		default:
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"nope"}}`)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", nil)
	ctx := context.Background()

	v := int64(3)
	_, err := c.Upload(ctx, "conflict.txt", strings.NewReader("x"), 1, "abc", &v)
	var e *Error
	if !errors.As(err, &e) || e.Code != "conflict" || e.Current == nil || e.Current.Version != 7 {
		t.Fatalf("conflict error = %#v", err)
	}
	if !IsCode(err, "conflict") || IsNetwork(err) {
		t.Fatal("classification wrong for 409")
	}

	_, _, err = c.Download(ctx, "plain")
	if !IsStatus(err, 502) || !IsNetwork(err) {
		t.Fatalf("502 should be retryable: %v", err)
	}

	_, err = c.Delete(ctx, "missing", nil)
	if !IsCode(err, "not_found") {
		t.Fatalf("not found: %v", err)
	}

	dead := New("http://127.0.0.1:1", "", nil)
	if _, err := dead.Health(ctx); !IsNetwork(err) {
		t.Fatalf("unreachable server should be a network error: %v", err)
	}
}
