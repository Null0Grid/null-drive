package api_test

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Null0Grid/null-drive/server/internal/auth"
	"github.com/Null0Grid/null-drive/shared/api"
)

func login(anon *client, user, pass, code string) resp {
	return anon.json(http.MethodPost, "/api/v1/auth/login", api.Credentials{Username: user, Password: pass, TOTP: code})
}

func TestLoginLockoutBackoff(t *testing.T) {
	sess, anon, clk := newPairingServer(t)
	_ = sess
	for i := 0; i < 4; i++ {
		want(t, login(anon, "admin", "wrong password", ""), http.StatusUnauthorized)
	}
	// Fifth failure locks the account for 30 s.
	want(t, login(anon, "admin", "wrong password", ""), http.StatusUnauthorized)
	r := login(anon, "admin", "hunter2hunter2", "")
	want(t, r, http.StatusTooManyRequests)
	if r.errCode(t) != "locked_out" || r.header.Get("Retry-After") != "30" {
		t.Fatalf("locked: %s retry-after=%q", r.body, r.header.Get("Retry-After"))
	}
	// Case doesn't dodge the lock (usernames are case-insensitive).
	want(t, login(anon, "ADMIN", "hunter2hunter2", ""), http.StatusTooManyRequests)
	clk.Advance(31 * time.Second)
	// Another failure after the lock: 60 s now (doubling).
	want(t, login(anon, "admin", "wrong again", ""), http.StatusUnauthorized)
	r = login(anon, "admin", "hunter2hunter2", "")
	if r.status != http.StatusTooManyRequests || r.header.Get("Retry-After") != "60" {
		t.Fatalf("second lock: %d %q", r.status, r.header.Get("Retry-After"))
	}
	clk.Advance(61 * time.Second)
	want(t, login(anon, "admin", "hunter2hunter2", ""), http.StatusOK)
	// Success reset the counter: four more failures don't lock.
	clk.Advance(2 * time.Minute) // refill the per-IP bucket
	for i := 0; i < 4; i++ {
		want(t, login(anon, "admin", "nope nope", ""), http.StatusUnauthorized)
	}
	want(t, login(anon, "admin", "hunter2hunter2", ""), http.StatusOK)
	// Unknown users are tracked the same way, so the lock reveals nothing.
	clk.Advance(2 * time.Minute)
	for i := 0; i < 5; i++ {
		want(t, login(anon, "ghost", "whatever123", ""), http.StatusUnauthorized)
	}
	want(t, login(anon, "ghost", "whatever123", ""), http.StatusTooManyRequests)
}

func TestLoginPerIPRateLimit(t *testing.T) {
	_, anon, clk := newPairingServer(t)
	for i := 0; i < 10; i++ {
		want(t, login(anon, "user"+itoa(int64(i)), "x-password", ""), http.StatusUnauthorized)
	}
	r := login(anon, "admin", "hunter2hunter2", "")
	if r.status != http.StatusTooManyRequests || r.errCode(t) != "rate_limited" || r.header.Get("Retry-After") == "" {
		t.Fatalf("11th login: %d %s", r.status, r.body)
	}
	clk.Advance(7 * time.Second)
	want(t, login(anon, "admin", "hunter2hunter2", ""), http.StatusOK)
}

func enableTOTP(t *testing.T, sess *client, clk *testClock) (string, []string) {
	t.Helper()
	r := sess.json(http.MethodPost, "/api/v1/auth/totp/setup", struct{}{})
	want(t, r, http.StatusOK)
	var setup api.TOTPSetupResponse
	r.decode(t, &setup)
	if !strings.HasPrefix(setup.URI, "otpauth://totp/Null%20Drive") || !strings.Contains(setup.URI, "secret="+setup.Secret) {
		t.Fatalf("uri %q", setup.URI)
	}
	// A wrong code doesn't enable it.
	want(t, sess.json(http.MethodPost, "/api/v1/auth/totp/enable", api.TOTPCodeRequest{Code: "000000"}), http.StatusBadRequest)
	code, _ := auth.TOTPCode(setup.Secret, auth.TOTPStep(clk.Now()))
	r = sess.json(http.MethodPost, "/api/v1/auth/totp/enable", api.TOTPCodeRequest{Code: code})
	want(t, r, http.StatusOK)
	var en api.TOTPEnableResponse
	r.decode(t, &en)
	if len(en.RecoveryCodes) != 10 {
		t.Fatalf("recovery codes: %v", en.RecoveryCodes)
	}
	return setup.Secret, en.RecoveryCodes
}

func TestTOTPLogin(t *testing.T) {
	sess, anon, clk := newPairingServer(t)
	secret, recovery := enableTOTP(t, sess, clk)

	var me api.MeResponse
	sess.do(http.MethodGet, "/api/v1/me", nil, nil).decode(t, &me)
	if !me.User.TOTPEnabled {
		t.Fatal("me.totp_enabled = false")
	}
	// The code used to confirm setup can't be replayed to log in.
	setupCode, _ := auth.TOTPCode(secret, auth.TOTPStep(clk.Now()))
	r := login(anon, "admin", "hunter2hunter2", "")
	if r.status != http.StatusUnauthorized || r.errCode(t) != "totp_required" {
		t.Fatalf("no code: %d %s", r.status, r.body)
	}
	if r := login(anon, "admin", "hunter2hunter2", setupCode); r.errCode(t) != "invalid_totp" {
		t.Fatalf("replayed setup code: %s", r.body)
	}
	clk.Advance(30 * time.Second)
	code, _ := auth.TOTPCode(secret, auth.TOTPStep(clk.Now()))
	want(t, login(anon, "admin", "hunter2hunter2", code), http.StatusOK)
	// Same code again: replay rejected.
	if r := login(anon, "admin", "hunter2hunter2", code); r.errCode(t) != "invalid_totp" {
		t.Fatalf("replay: %s", r.body)
	}
	// Wrong password with a valid code is still just a wrong password.
	clk.Advance(30 * time.Second)
	code, _ = auth.TOTPCode(secret, auth.TOTPStep(clk.Now()))
	if r := login(anon, "admin", "wrongwrong", code); r.errCode(t) != "invalid_credentials" {
		t.Fatalf("wrong password + code: %s", r.body)
	}
	// One step of clock skew is tolerated.
	clk.Advance(30 * time.Second)
	prev, _ := auth.TOTPCode(secret, auth.TOTPStep(clk.Now())-1)
	want(t, login(anon, "admin", "hunter2hunter2", prev), http.StatusOK)
	// Recovery codes work once, in any formatting.
	clk.Advance(5 * time.Minute)
	rc := strings.ToUpper(strings.ReplaceAll(recovery[0], "-", " "))
	want(t, login(anon, "admin", "hunter2hunter2", rc), http.StatusOK)
	if r := login(anon, "admin", "hunter2hunter2", recovery[0]); r.errCode(t) != "invalid_totp" {
		t.Fatalf("recovery code reused: %s", r.body)
	}
	// Device tokens are unaffected by 2FA.
	dev := registerDevice(t, sess, "laptop")
	want(t, dev.do(http.MethodGet, "/api/v1/files", nil, nil), http.StatusOK)

	// Turning it off needs password + code.
	clk.Advance(time.Minute)
	code, _ = auth.TOTPCode(secret, auth.TOTPStep(clk.Now()))
	want(t, sess.json(http.MethodPost, "/api/v1/auth/totp/disable", api.TOTPDisableRequest{Password: "wrong", Code: code}), http.StatusUnauthorized)
	want(t, sess.json(http.MethodPost, "/api/v1/auth/totp/disable", api.TOTPDisableRequest{Password: "hunter2hunter2", Code: code}), http.StatusNoContent)
	clk.Advance(2 * time.Minute)
	want(t, login(anon, "admin", "hunter2hunter2", ""), http.StatusOK)
	// Setup can't replace an enabled secret.
	_, _ = enableTOTP(t, sess, clk)
	want(t, sess.json(http.MethodPost, "/api/v1/auth/totp/setup", struct{}{}), http.StatusConflict)
}

func TestTOTPFailuresCountTowardsLockout(t *testing.T) {
	sess, anon, clk := newPairingServer(t)
	enableTOTP(t, sess, clk)
	for i := 0; i < 5; i++ {
		want(t, login(anon, "admin", "hunter2hunter2", "123456"), http.StatusUnauthorized)
	}
	want(t, login(anon, "admin", "hunter2hunter2", "123456"), http.StatusTooManyRequests)
}

// cookieClient logs in through the browser path and returns the session
// cookie and CSRF token.
func cookieLogin(t *testing.T, base string) (*http.Cookie, string) {
	t.Helper()
	anon := &client{t: t, base: base}
	r := login(anon, "admin", "hunter2hunter2", "")
	want(t, r, http.StatusOK)
	var sr api.SessionResponse
	r.decode(t, &sr)
	for _, ck := range (&http.Response{Header: r.header}).Cookies() {
		if ck.Name == api.SessionCookieName {
			return ck, sr.CSRFToken
		}
	}
	t.Fatal("no cookie")
	return nil, ""
}

func cookieDo(t *testing.T, base string, ck *http.Cookie, method, path string, hdr map[string]string) int {
	t.Helper()
	req, _ := http.NewRequest(method, base+path, strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(ck)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return res.StatusCode
}

func TestCSRF(t *testing.T) {
	ts := newTestServer(t)
	sess := setupAdmin(t, ts)
	ck, tok := cookieLogin(t, ts.URL)
	if len(tok) < 40 {
		t.Fatalf("csrf token %q", tok)
	}
	// Reads don't need the token.
	if s := cookieDo(t, ts.URL, ck, http.MethodGet, "/api/v1/devices", nil); s != http.StatusOK {
		t.Fatalf("GET with cookie: %d", s)
	}
	// Writes with the cookie need it.
	if s := cookieDo(t, ts.URL, ck, http.MethodPost, "/api/v1/devices", nil); s != http.StatusForbidden {
		t.Fatalf("POST without token: %d", s)
	}
	if s := cookieDo(t, ts.URL, ck, http.MethodPost, "/api/v1/devices", map[string]string{api.HeaderCSRF: tok + "x"}); s != http.StatusForbidden {
		t.Fatalf("POST with wrong token: %d", s)
	}
	if s := cookieDo(t, ts.URL, ck, http.MethodPost, "/api/v1/devices", map[string]string{api.HeaderCSRF: tok}); s != http.StatusCreated {
		t.Fatalf("POST with token: %d", s)
	}
	// The token is per session: another session's token doesn't work.
	ck2, tok2 := cookieLogin(t, ts.URL)
	if tok2 == tok {
		t.Fatal("two sessions share a CSRF token")
	}
	if s := cookieDo(t, ts.URL, ck2, http.MethodPost, "/api/v1/devices", map[string]string{api.HeaderCSRF: tok}); s != http.StatusForbidden {
		t.Fatalf("cross-session token: %d", s)
	}
	// /me hands the token back to a reloaded page.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/me", nil)
	req.AddCookie(ck)
	res, _ := http.DefaultClient.Do(req)
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(b), tok) {
		t.Fatalf("/me without csrf token: %s", b)
	}
	// Bearer tokens are exempt (not sent by browsers on their own).
	want(t, sess.json(http.MethodPost, "/api/v1/devices", api.CreateDeviceRequest{Name: "cli"}), http.StatusCreated)
}

func TestOriginGuard(t *testing.T) {
	ts := newTestServer(t)
	setupAdmin(t, ts)
	anon := &client{t: t, base: ts.URL}
	body := `{"username":"admin","password":"hunter2hunter2"}`
	cases := []struct {
		name string
		hdr  map[string]string
		want int
	}{
		{"no origin (curl, desktop)", nil, http.StatusOK},
		{"same origin", map[string]string{"Origin": ts.URL}, http.StatusOK},
		{"other origin", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"null origin", map[string]string{"Origin": "null"}, http.StatusForbidden},
		{"cross-site fetch", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"same-site sibling app", map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"same-origin fetch", map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := map[string]string{"Content-Type": "application/json"}
			for k, v := range tc.hdr {
				h[k] = v
			}
			r := anon.do(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body), h)
			if r.status != tc.want {
				t.Fatalf("status %d, want %d: %s", r.status, tc.want, r.body)
			}
		})
	}
}

func TestSecurityHeaders(t *testing.T) {
	ts := newTestServer(t)
	anon := &client{t: t, base: ts.URL}
	r := anon.do(http.MethodGet, "/api/v1/health", nil, nil)
	h := r.header
	for k, v := range map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
		"Referrer-Policy":              "no-referrer",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Cache-Control":                "no-store",
	} {
		if h.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, h.Get(k), v)
		}
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") ||
		!strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Errorf("API CSP = %q", h.Get("Content-Security-Policy"))
	}
	if h.Get("Strict-Transport-Security") != "" {
		t.Error("HSTS sent over plain HTTP")
	}
	// A spoofed X-Forwarded-Proto doesn't trigger HSTS unless the proxy
	// is trusted.
	r = anon.do(http.MethodGet, "/api/v1/health", nil, map[string]string{"X-Forwarded-Proto": "https"})
	if r.header.Get("Strict-Transport-Security") != "" {
		t.Error("HSTS from untrusted X-Forwarded-Proto")
	}

	tlsSrv := httptest.NewUnstartedServer(ts.Config.Handler)
	tlsSrv.StartTLS()
	defer tlsSrv.Close()
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // test server
	res, err := hc.Get(tlsSrv.URL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Header.Get("Strict-Transport-Security") != "max-age=31536000" {
		t.Errorf("HSTS over TLS = %q", res.Header.Get("Strict-Transport-Security"))
	}
}

func TestAuditLog(t *testing.T) {
	sess, anon, clk := newPairingServer(t)
	want(t, login(anon, "admin", "wrong-password", ""), http.StatusUnauthorized)
	want(t, login(anon, "admin", "hunter2hunter2", ""), http.StatusOK)
	dev := registerDevice(t, sess, "Laptop")
	_ = dev
	r := sess.json(http.MethodGet, "/api/v1/devices", nil)
	var dl api.DeviceList
	r.decode(t, &dl)
	want(t, sess.do(http.MethodDelete, "/api/v1/devices/"+itoa(dl.Devices[0].ID), nil, nil), http.StatusOK)
	name := "box"
	want(t, sess.json(http.MethodPatch, "/api/v1/system/settings", api.UpdateSettingsRequest{ServerName: &name}), http.StatusOK)
	enableTOTP(t, sess, clk)

	r = sess.do(http.MethodGet, "/api/v1/admin/audit?limit=3", nil, nil)
	want(t, r, http.StatusOK)
	var page api.AuditList
	r.decode(t, &page)
	if len(page.Events) != 3 || page.Next == nil {
		t.Fatalf("page 1: %+v", page)
	}
	var all []api.AuditEvent
	all = append(all, page.Events...)
	for page.Next != nil {
		r = sess.do(http.MethodGet, "/api/v1/admin/audit?limit=3&before="+itoa(*page.Next), nil, nil)
		page = api.AuditList{}
		r.decode(t, &page)
		all = append(all, page.Events...)
	}
	var kinds []string
	for i := len(all) - 1; i >= 0; i-- {
		kinds = append(kinds, all[i].Event)
		if all[i].Event == "login_failed" && (all[i].Username != "admin" || all[i].IP == "") {
			t.Errorf("login_failed event: %+v", all[i])
		}
	}
	got := strings.Join(kinds, ",")
	wantSeq := "setup_completed,login_failed,login,device_added,device_revoked,settings_changed,totp_enabled"
	if got != wantSeq {
		t.Fatalf("events:\n got %s\nwant %s", got, wantSeq)
	}
	// Device tokens can't read it.
	dev2 := registerDevice(t, sess, "Phone")
	want(t, dev2.do(http.MethodGet, "/api/v1/admin/audit", nil, nil), http.StatusForbidden)
}

func TestLogoutDropsSessionSocket(t *testing.T) {
	ts := newTestServer(t)
	setupAdmin(t, ts)
	ck, tok := cookieLogin(t, ts.URL)
	hdr := http.Header{}
	hdr.Set("Cookie", ck.String())
	hdr.Set("Origin", ts.URL)
	c, err := dialWS(t, &client{t: t, base: ts.URL}, hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if m := readMsg(t, c); m.Type != api.RealtimeHello {
		t.Fatalf("first message %+v", m)
	}
	if s := cookieDo(t, ts.URL, ck, http.MethodPost, "/api/v1/auth/logout", map[string]string{api.HeaderCSRF: tok}); s != http.StatusNoContent {
		t.Fatalf("logout: %d", s)
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := c.ReadMessage(); err == nil {
		t.Fatal("socket still open after logout")
	}
}

func TestE2EEEndpointValidation(t *testing.T) {
	ts := newTestServer(t)
	sess := setupAdmin(t, ts)
	var st api.E2EEStatus
	sess.do(http.MethodGet, "/api/v2/e2ee", nil, nil).decode(t, &st)
	if st.State != api.E2EEOff || st.Bundle != nil {
		t.Fatalf("initial status %+v", st)
	}
	bad := []api.E2EEEnableRequest{
		{Bundle: []byte(`{"version":1,"fingerprint":"0123456789abcdef"}`), Fingerprint: "0123456789abcdef", AuthKey: "short"},
		{Bundle: []byte(`{"version":1,"fingerprint":"aaaaaaaaaaaaaaaa"}`), Fingerprint: "0123456789abcdef", AuthKey: strings.Repeat("a", 64)},
		{Bundle: []byte(`{"version":9,"fingerprint":"0123456789abcdef"}`), Fingerprint: "0123456789abcdef", AuthKey: strings.Repeat("a", 64)},
		{Bundle: []byte(`[]`), Fingerprint: "0123456789abcdef", AuthKey: strings.Repeat("a", 64)},
	}
	for i, b := range bad {
		if r := sess.json(http.MethodPost, "/api/v2/e2ee", b); r.status != http.StatusBadRequest {
			t.Fatalf("bad bundle %d: %d %s", i, r.status, r.body)
		}
	}
	good := api.E2EEEnableRequest{Bundle: []byte(`{"version":1,"fingerprint":"0123456789abcdef"}`), Fingerprint: "0123456789abcdef", AuthKey: strings.Repeat("a", 64)}
	// Turning encryption on is an account action: not from a device token.
	dev := registerDevice(t, sess, "laptop")
	want(t, dev.json(http.MethodPost, "/api/v2/e2ee", good), http.StatusForbidden)
	r := sess.json(http.MethodPost, "/api/v2/e2ee", good)
	want(t, r, http.StatusCreated)
	r.decode(t, &st)
	if st.State != api.E2EEOn || st.Fingerprint != "0123456789abcdef" {
		t.Fatalf("after enable %+v", st)
	}
	var me api.MeResponse
	sess.do(http.MethodGet, "/api/v1/me", nil, nil).decode(t, &me)
	if me.User.E2EE != api.E2EEOn {
		t.Fatalf("me.e2ee = %q", me.User.E2EE)
	}
	// Finishing needs the auth key.
	want(t, sess.json(http.MethodPost, "/api/v2/e2ee/finish", api.E2EEFinishRequest{AuthKey: strings.Repeat("b", 64)}), http.StatusForbidden)
	want(t, sess.json(http.MethodPost, "/api/v2/e2ee/finish", api.E2EEFinishRequest{AuthKey: strings.Repeat("a", 64)}), http.StatusOK)
}
