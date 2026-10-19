package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	srvapi "github.com/Null0Grid/null-drive/server/internal/api"
	"github.com/Null0Grid/null-drive/shared/api"
)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newPairingServer(t *testing.T) (*client, *client, *testClock) {
	clk := &testClock{t: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
	ts := newTestServerWith(t, clk.Now, nil)
	return setupAdmin(t, ts), &client{t: t, base: ts.URL}, clk
}

func issue(t *testing.T, sess *client) api.PairingResponse {
	t.Helper()
	r := sess.json(http.MethodPost, "/api/v1/pairing", struct{}{})
	want(t, r, http.StatusCreated)
	var pr api.PairingResponse
	r.decode(t, &pr)
	return pr
}

func claim(anon *client, code, name string) resp {
	return anon.json(http.MethodPost, "/api/v1/pairing/claim", api.ClaimPairingRequest{Code: code, DeviceName: name})
}

func TestPairingHappyPath(t *testing.T) {
	sess, anon, _ := newPairingServer(t)
	pr := issue(t, sess)
	if !strings.HasPrefix(pr.Code, "NULL-") || pr.Status != "pending" {
		t.Fatalf("issued %+v", pr)
	}
	// Typed sloppily: lowercase, spaces, no prefix.
	sloppy := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(pr.Code, "NULL-"), "-", " "))
	r := claim(anon, sloppy, "Work laptop")
	want(t, r, http.StatusCreated)
	var cr api.ClaimPairingResponse
	r.decode(t, &cr)
	if !strings.HasPrefix(cr.Token, api.DeviceTokenPrefix) || cr.User.Username != "admin" || cr.Device.AddedVia != "pairing" {
		t.Fatalf("claim = %+v", cr)
	}
	// The token works for syncing...
	dev := &client{t: t, base: anon.base, token: cr.Token}
	want(t, dev.do(http.MethodGet, "/api/v1/files", nil, nil), http.StatusOK)
	// ...but a paired device cannot mint more codes (session only).
	want(t, dev.json(http.MethodPost, "/api/v1/pairing", struct{}{}), http.StatusForbidden)

	// The web UI's poll sees the result.
	var st api.PairingResponse
	sess.do(http.MethodGet, "/api/v1/pairing/"+itoa(pr.ID), nil, nil).decode(t, &st)
	if st.Status != "claimed" || st.Device == nil || st.Device.Name != "Work laptop" || st.Code != "" {
		t.Fatalf("status = %+v", st)
	}
}

func TestPairingSingleUse(t *testing.T) {
	sess, anon, _ := newPairingServer(t)
	pr := issue(t, sess)
	want(t, claim(anon, pr.Code, "one"), http.StatusCreated)
	r := claim(anon, pr.Code, "two")
	want(t, r, http.StatusBadRequest)
	if r.errCode(t) != "invalid_code" {
		t.Fatal(r.errCode(t))
	}
}

func TestPairingConcurrentClaimsOnlyOneWins(t *testing.T) {
	sess, anon, _ := newPairingServer(t)
	pr := issue(t, sess)
	var wg sync.WaitGroup
	results := make(chan int, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- claim(anon, pr.Code, "racer"+itoa(int64(i))).status
		}(i)
	}
	wg.Wait()
	close(results)
	wins := 0
	for s := range results {
		if s == http.StatusCreated {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("%d claims succeeded", wins)
	}
}

func TestPairingExpiry(t *testing.T) {
	sess, anon, clk := newPairingServer(t)
	pr := issue(t, sess)
	clk.Advance(5*time.Minute + time.Second)
	want(t, claim(anon, pr.Code, "late"), http.StatusBadRequest)
	var st api.PairingResponse
	sess.do(http.MethodGet, "/api/v1/pairing/"+itoa(pr.ID), nil, nil).decode(t, &st)
	if st.Status != "expired" {
		t.Fatalf("status %q", st.Status)
	}
}

func TestPairingCancel(t *testing.T) {
	sess, anon, _ := newPairingServer(t)
	pr := issue(t, sess)
	r := sess.do(http.MethodDelete, "/api/v1/pairing/"+itoa(pr.ID), nil, nil)
	want(t, r, http.StatusOK)
	want(t, claim(anon, pr.Code, "x"), http.StatusBadRequest)
}

func TestPairingLimitsActiveCodes(t *testing.T) {
	sess, _, clk := newPairingServer(t)
	for i := 0; i < 5; i++ {
		issue(t, sess)
	}
	want(t, sess.json(http.MethodPost, "/api/v1/pairing", struct{}{}), http.StatusTooManyRequests)
	clk.Advance(6 * time.Minute) // old codes expired
	issue(t, sess)
}

func TestPairingBruteForceRateLimited(t *testing.T) {
	sess, anon, clk := newPairingServer(t)
	pr := issue(t, sess)
	// Five wrong guesses are answered normally...
	for i := 0; i < 5; i++ {
		want(t, claim(anon, "NULL-0000-0"+itoa(int64(i)), "guess"), http.StatusBadRequest)
	}
	// ...then even the right code is refused: the limiter runs first, so
	// a guesser cannot tell a hit from a throttled miss.
	r := claim(anon, pr.Code, "guess")
	want(t, r, http.StatusTooManyRequests)
	if r.header.Get("Retry-After") == "" {
		t.Fatal("no Retry-After")
	}
	// Malformed input also costs a token.
	want(t, claim(anon, "garbage", "guess"), http.StatusTooManyRequests)
	// Tokens refill at 5/min.
	clk.Advance(13 * time.Second)
	want(t, claim(anon, pr.Code, "real"), http.StatusCreated)
}

func TestPairingGlobalLimit(t *testing.T) {
	// A distributed guesser (many addresses) is capped by the global
	// bucket. TrustProxy lets the test vary the address per request.
	clk := &testClock{t: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
	ts := newTestServerWith(t, clk.Now, func(o *srvapi.Options) { o.TrustProxy = true })
	anon := &client{t: t, base: ts.URL}
	guess := func(ip string) int {
		b, _ := json.Marshal(api.ClaimPairingRequest{Code: "NULL-0000-00", DeviceName: "g"})
		return anon.do(http.MethodPost, "/api/v1/pairing/claim", bytes.NewReader(b),
			map[string]string{"X-Forwarded-For": "198.51.100.1, 10.9.9." + ip}).status
	}
	allowed := 0
	for ip := 0; ip < 10; ip++ {
		for i := 0; i < 5; i++ {
			if guess(itoa(int64(ip))) == http.StatusBadRequest {
				allowed++
			}
		}
	}
	if allowed != 30 {
		t.Fatalf("global limit let %d guesses through, want 30", allowed)
	}
}

func TestPairingRequiresSessionToIssue(t *testing.T) {
	_, anon, _ := newPairingServer(t)
	want(t, anon.json(http.MethodPost, "/api/v1/pairing", struct{}{}), http.StatusUnauthorized)
}

func TestDeviceDetails(t *testing.T) {
	ts := newTestServer(t)
	sess := setupAdmin(t, ts)
	dev := registerDevice(t, sess, "desk")
	want(t, dev.do(http.MethodGet, "/api/v1/files", nil, map[string]string{"User-Agent": "NullDrive/0.5.0 (windows/amd64)"}), http.StatusOK)
	// A browser-style UA on the same device must not overwrite it.
	want(t, dev.do(http.MethodGet, "/api/v1/files", nil, map[string]string{"User-Agent": "curl/8"}), http.StatusOK)
	var dl api.DeviceList
	sess.do(http.MethodGet, "/api/v1/devices", nil, nil).decode(t, &dl)
	d := dl.Devices[0]
	if d.LastIP != "127.0.0.1" || d.ClientVersion != "NullDrive/0.5.0 (windows/amd64)" || d.AddedVia != "password" || d.Online {
		t.Fatalf("device = %+v", d)
	}
}
