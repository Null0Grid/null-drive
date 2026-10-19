package synctest

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Null0Grid/null-drive/shared/client"
)

func randomData(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, 7))
	b := make([]byte, n)
	for i := 0; i+8 <= n; i += 8 {
		v := r.Uint64()
		for j := 0; j < 8; j++ {
			b[i+j] = byte(v >> (8 * j))
		}
	}
	return b
}

type transfers struct {
	mu   sync.Mutex
	up   map[string]client.TransferStats
	down map[string]client.TransferStats
}

func track(d *device) *transfers {
	t := &transfers{up: map[string]client.TransferStats{}, down: map[string]client.TransferStats{}}
	d.engine.OnTransfer = func(p, dir string, st client.TransferStats) {
		t.mu.Lock()
		defer t.mu.Unlock()
		if dir == "up" {
			t.up[p] = st
		} else {
			t.down[p] = st
		}
	}
	return t
}

// TestChunkedDeltaSync: a 1-KiB edit in the middle of a 24 MiB file moves
// a few MiB at most in both directions, and the result is byte-identical.
func TestChunkedDeltaSync(t *testing.T) {
	w := newWorld(t)
	a, b := w.device("A"), w.device("B")
	ta, tb := track(a), track(b)
	big := randomData(1, 24<<20)
	if err := os.WriteFile(a.full("video.mov"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	settle(t, a, b)
	if st := ta.up["video.mov"]; st.BytesSent != int64(len(big)) {
		t.Fatalf("first upload sent %d", st.BytesSent)
	}

	edited := append([]byte{}, big...)
	copy(edited[12<<20:], randomData(2, 1024))
	if err := os.WriteFile(a.full("video.mov"), edited, 0o644); err != nil {
		t.Fatal(err)
	}
	settle(t, a, b)
	up, down := ta.up["video.mov"], tb.down["video.mov"]
	t.Logf("edit 1 KiB of 24 MiB: uploaded %d bytes in %d/%d chunks; downloaded %d, reused %d locally",
		up.BytesSent, up.Transferred, up.Chunks, down.BytesSent, down.BytesReused)
	if up.BytesSent > 3*8<<20 || down.BytesSent > 3*8<<20 || down.BytesReused < 12<<20 {
		t.Fatalf("not a delta: up %+v down %+v", up, down)
	}
	got, _ := os.ReadFile(b.full("video.mov"))
	if !bytes.Equal(got, edited) {
		t.Fatal("B's copy differs after delta download")
	}
	// Identical content elsewhere: nothing travels at all.
	if err := os.WriteFile(a.full("copy.mov"), edited, 0o644); err != nil {
		t.Fatal(err)
	}
	settle(t, a, b)
	if st := ta.up["copy.mov"]; st.BytesSent != 0 || st.BytesReused != int64(len(edited)) {
		t.Fatalf("duplicate file uploaded bytes: %+v", st)
	}
}

// cancelAfter cancels the sync once n bytes of a download have arrived.
func TestChunkedDownloadResumes(t *testing.T) {
	w := newWorld(t)
	a, b := w.device("A"), w.device("B")
	tb := track(b)
	big := randomData(3, 30<<20)
	if err := os.WriteFile(a.full("disk.img"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	a.sync()

	ctx, cancel := context.WithCancel(context.Background())
	b.engine.Parallel = 1
	b.engine.OnBytes = func(_ string, done, _ int64) {
		if done >= 10<<20 {
			cancel()
		}
	}
	if _, err := b.engine.Sync(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted sync: %v", err)
	}
	if _, err := os.Stat(b.full("disk.img.nulldrive-part" + client.PartStateSuffix)); err != nil {
		t.Fatalf("no resume state after interruption: %v", err)
	}
	b.engine.OnBytes = nil
	settle(t, b)
	st := tb.down["disk.img"]
	t.Logf("resumed: %d bytes from the earlier attempt, %d fetched now", st.ResumedBytes, st.BytesSent)
	if st.ResumedBytes < 8<<20 || st.BytesSent+st.ResumedBytes != int64(len(big)) {
		t.Fatalf("did not resume: %+v", st)
	}
	got, _ := os.ReadFile(b.full("disk.img"))
	if !bytes.Equal(got, big) {
		t.Fatal("resumed download differs")
	}
	if entries, _ := os.ReadDir(b.root); len(entries) != 1 {
		t.Fatalf("leftovers in the folder: %v", entries)
	}
}

// TestFallbackToV1: against a server without the v2 API (simulated by a
// proxy that 404s /api/v2), the engine transfers whole files as before.
func TestFallbackToV1(t *testing.T) {
	w := newWorld(t)
	target, _ := url.Parse(w.ts.URL)
	rp := httputil.NewSingleHostReverseProxy(target)
	old := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v2/") {
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusNotFound)
			_, _ = rw.Write([]byte(`{"error":{"code":"not_found","message":"no such endpoint"}}`))
			return
		}
		rp.ServeHTTP(rw, r)
	}))
	defer old.Close()
	a, b := w.device("A"), w.device("B")
	for _, d := range []*device{a, b} {
		c := d.engine.Remote.(*client.Client)
		d.engine.Remote = client.New(old.URL, c.Token(), nil)
	}
	ta := track(a)
	a.write("notes.txt", "via v1")
	settle(t, a, b)
	assertConverged(t, w, map[string]string{"notes.txt": "via v1"}, a, b)
	if len(ta.up) != 0 {
		t.Fatal("chunked transfer used against a v1 server")
	}
	if a.engine.Remote.(*client.Client).SupportsV2(context.Background()) {
		t.Fatal("v2 detected on a v1 server")
	}
}

// TestChunkedUploadResumes: chunks sent before an interruption are kept
// by the server (pending commit, within the GC grace period), so the next
// attempt only sends the rest.
func TestChunkedUploadResumes(t *testing.T) {
	w := newWorld(t)
	a := w.device("A")
	ta := track(a)
	big := randomData(4, 30<<20)
	if err := os.WriteFile(a.full("backup.tar"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.engine.Parallel = 1
	a.engine.OnBytes = func(_ string, done, _ int64) {
		if done >= 10<<20 {
			cancel()
		}
	}
	if _, err := a.engine.Sync(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted sync: %v", err)
	}
	a.engine.OnBytes = nil
	settle(t, a)
	st := ta.up["backup.tar"]
	t.Logf("resumed upload: sent %d of %d bytes, %d already on the server", st.BytesSent, st.Size, st.BytesReused)
	if st.BytesReused < 8<<20 {
		t.Fatalf("upload restarted from scratch: %+v", st)
	}
	if w.serverTree()["backup.tar"] != string(big) {
		t.Fatal("server content differs")
	}
}
