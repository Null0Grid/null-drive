package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Null0Grid/null-drive/shared/ws"
)

// TestTOFUAgainstSelfSignedServer: probe the certificate, pin it, then
// both HTTPS calls and the wss:// notifier work; a different certificate
// is refused.
func TestTOFUAgainstSelfSignedServer(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","version":"test"}`))
	})
	mux.HandleFunc("/api/v1/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := ws.Accept(w, r)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteMessage(ws.OpText, []byte(`{"type":"hello","version":9}`))
		drain(c)
	})
	srv := httptest.NewTLSServer(mux) // self-signed
	defer srv.Close()
	ctx := context.Background()

	// Unpinned: normal verification fails.
	if _, err := New(srv.URL, "", nil).Health(ctx); err == nil {
		t.Fatal("self-signed certificate accepted without pinning")
	}
	info, err := ProbeTLS(ctx, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if info.Trusted || info.Problem == "" || len(info.Fingerprint) != 95 {
		t.Fatalf("probe = %+v", info)
	}
	pinned := NewPinned(srv.URL, "ndd_x", info.Fingerprint)
	if h, err := pinned.Health(ctx); err != nil || h.Version != "test" {
		t.Fatalf("pinned health: %+v %v", h, err)
	}
	got := make(chan int64, 1)
	n := &Notifier{Client: pinned, OnVersion: func(v int64) { got <- v }}
	nctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = n.Run(nctx) }()
	select {
	case v := <-got:
		if v != 9 {
			t.Fatalf("version %d", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no hello over pinned wss")
	}

	// A wrong pin (the cert "changed") is refused with ErrCertChanged.
	wrong := NewPinned(srv.URL, "", "00:"+info.Fingerprint[3:])
	if _, err := wrong.Health(ctx); !errors.Is(err, ErrCertChanged) {
		t.Fatalf("wrong pin: %v", err)
	}
	if info, err := ProbeTLS(ctx, "http://example.invalid"); info != nil || err != nil {
		t.Fatal("plain http has nothing to probe")
	}
}
