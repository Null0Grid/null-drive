package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/Null0Grid/null-drive/server/internal/auth"
	"github.com/Null0Grid/null-drive/server/internal/db"
	"github.com/Null0Grid/null-drive/server/internal/meta"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestVersionAndUsage(t *testing.T) {
	var out bytes.Buffer
	if err := dispatch([]string{"version"}, &out, env(nil)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "nulldrive dev (commit none)") {
		t.Fatalf("version output %q", out.String())
	}
	out.Reset()
	if err := dispatch([]string{"help"}, &out, env(nil)); err != nil || !strings.Contains(out.String(), "reset-password") {
		t.Fatalf("help: %v %q", err, out.String())
	}
	if err := dispatch([]string{"bogus"}, &out, env(nil)); err == nil {
		t.Fatal("unknown command accepted")
	}
	if err := dispatch([]string{"reset-password"}, &out, env(nil)); err == nil {
		t.Fatal("reset-password without user accepted")
	}
}

func TestHealthcheck(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer bad.Close()
	if err := healthcheck(ok.URL); err != nil {
		t.Fatalf("healthy server: %v", err)
	}
	if err := healthcheck(bad.URL); err == nil {
		t.Fatal("503 reported healthy")
	}
	if err := healthcheck("http://127.0.0.1:1/api/v1/health"); err == nil {
		t.Fatal("closed port reported healthy")
	}
}

func TestResetPassword(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	d, err := db.Open(filepath.Join(dir, "nulldrive.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	store := meta.New(d, nil)
	old, _ := auth.Hasher{P: auth.TestParams}.Hash("forgotten-password")
	u, err := store.CompleteSetup(ctx, "admin", old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSession(ctx, u.ID, "sess", 3600e9); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()

	var out bytes.Buffer
	e := env(map[string]string{"NULLDRIVE_DATA_DIR": dir})
	if err := dispatch([]string{"reset-password", "nobody"}, &out, e); err == nil {
		t.Fatal("unknown user accepted")
	}
	if err := dispatch([]string{"reset-password", "admin"}, &out, e); err != nil {
		t.Fatal(err)
	}
	pw := regexp.MustCompile(`(?m)^\s+([a-z2-9]{5}(-[a-z2-9]{5}){3})$`).FindStringSubmatch(out.String())
	if pw == nil {
		t.Fatalf("no password in output %q", out.String())
	}

	d, _ = db.Open(filepath.Join(dir, "nulldrive.db"))
	defer d.Close()
	store = meta.New(d, nil)
	got, _ := store.UserByUsername(ctx, "admin")
	if ok, _ := (auth.Hasher{}).Verify(pw[1], got.PasswordHash); !ok {
		t.Fatal("new password does not verify")
	}
	if _, _, err := store.SessionUser(ctx, "sess"); err == nil {
		t.Fatal("old session survived the reset")
	}
}

func TestEnsureWritable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data")
	if err := ensureWritable(dir); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("probe file left behind")
	}
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("permission check needs a non-root unix user")
	}
	ro := t.TempDir()
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	err := ensureWritable(ro)
	if err == nil || !strings.Contains(err.Error(), "chown") {
		t.Fatalf("want actionable permission error, got %v", err)
	}
}

func TestRandomPassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p, err := randomPassword()
		if err != nil || len(p) != 23 || strings.ContainsAny(p, "01lIO") || seen[p] {
			t.Fatalf("bad password %q (%v)", p, err)
		}
		seen[p] = true
	}
}

func TestDisable2FACommand(t *testing.T) {
	dir := t.TempDir()
	e := env(map[string]string{"NULLDRIVE_DATA_DIR": dir})
	var out bytes.Buffer
	if err := dispatch([]string{"disable-2fa"}, &out, e); err == nil {
		t.Fatal("disable-2fa without user accepted")
	}
	if err := dispatch([]string{"disable-2fa", "nobody"}, &out, e); err == nil {
		t.Fatal("unknown user accepted")
	}
}

func TestAddUserCommand(t *testing.T) {
	dir := t.TempDir()
	e := env(map[string]string{"NULLDRIVE_DATA_DIR": dir})
	var out bytes.Buffer
	if err := dispatch([]string{"add-user", "bob"}, &out, e); err == nil || !strings.Contains(err.Error(), "setup wizard") {
		t.Fatalf("before setup: %v", err)
	}
	if err := dispatch([]string{"add-user", "bad name"}, &out, e); err == nil {
		t.Fatal("invalid username accepted")
	}
}
