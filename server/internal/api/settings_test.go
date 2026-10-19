package api_test

import (
	"net/http"
	"sync"
	"testing"

	srvapi "github.com/nulldrive/nulldrive/server/internal/api"
	"github.com/nulldrive/nulldrive/shared/api"
)

type fakeDiscovery struct {
	mu      sync.Mutex
	applied []srvapi.Advert
}

func (f *fakeDiscovery) Apply(a srvapi.Advert) {
	f.mu.Lock()
	f.applied = append(f.applied, a)
	f.mu.Unlock()
}

func (f *fakeDiscovery) Status() srvapi.DiscoveryStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n := len(f.applied); n > 0 && f.applied[n-1].Enabled {
		return srvapi.DiscoveryStatus{Active: true, Addrs: []string{"192.168.1.20"}}
	}
	return srvapi.DiscoveryStatus{}
}

func (f *fakeDiscovery) last() srvapi.Advert {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.applied[len(f.applied)-1]
}

func TestSettingsAndDiscovery(t *testing.T) {
	fd := &fakeDiscovery{}
	ts := newTestServerWith(t, nil, func(o *srvapi.Options) { o.Discovery = fd })
	sess := setupAdmin(t, ts)
	// Completing setup re-announces with setup=done.
	if a := fd.last(); !a.Enabled || !a.SetupDone || a.Name == "" {
		t.Fatalf("after setup: %+v", a)
	}
	var st api.ServerSettings
	sess.do(http.MethodGet, "/api/v1/system/settings", nil, nil).decode(t, &st)
	if !st.MDNSEnabled || !st.MDNSActive || st.MDNSForcedOff || len(st.MDNSAddrs) != 1 {
		t.Fatalf("settings %+v", st)
	}

	off, name := false, "attic"
	r := sess.json(http.MethodPatch, "/api/v1/system/settings", api.UpdateSettingsRequest{MDNSEnabled: &off, ServerName: &name})
	want(t, r, http.StatusOK)
	r.decode(t, &st)
	if st.MDNSEnabled || st.MDNSActive || st.ServerName != "attic" {
		t.Fatalf("after patch %+v", st)
	}
	if a := fd.last(); a.Enabled || a.Name != "attic" {
		t.Fatalf("advert after patch %+v", a)
	}

	bad := "a.b"
	want(t, sess.json(http.MethodPatch, "/api/v1/system/settings", api.UpdateSettingsRequest{ServerName: &bad}), http.StatusBadRequest)
	// Devices cannot change server settings.
	dev := registerDevice(t, sess, "d")
	want(t, dev.json(http.MethodPatch, "/api/v1/system/settings", api.UpdateSettingsRequest{MDNSEnabled: &off}), http.StatusForbidden)
}

func TestDiscoveryForcedOff(t *testing.T) {
	fd := &fakeDiscovery{}
	ts := newTestServerWith(t, nil, func(o *srvapi.Options) { o.Discovery = fd; o.DiscoveryForcedOff = true })
	sess := setupAdmin(t, ts)
	if fd.last().Enabled {
		t.Fatal("NULLDRIVE_MDNS=off must win over the stored setting")
	}
	var st api.ServerSettings
	sess.do(http.MethodGet, "/api/v1/system/settings", nil, nil).decode(t, &st)
	if !st.MDNSForcedOff {
		t.Fatalf("%+v", st)
	}
}
