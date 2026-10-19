// Package discovery advertises the server on the LAN with mDNS/DNS-SD
// (_nulldrive._tcp) so desktop clients can find it without typing an
// address. See docs/DESIGN.md "LAN discovery" for how it works and where
// it does not (other subnets/VLANs, Docker bridge networks).
package discovery

import (
	"log/slog"
	"net/netip"
	"os"
	"sync"

	"github.com/Null0Grid/null-drive/server/internal/api"
	"github.com/Null0Grid/null-drive/server/internal/buildinfo"
	"github.com/Null0Grid/null-drive/shared/mdns"
)

// Advertiser implements api.Discovery.
type Advertiser struct {
	Port     uint16
	Override []netip.Addr
	Log      *slog.Logger

	mu     sync.Mutex
	adv    *mdns.Advertiser
	status api.DiscoveryStatus
	last   api.Advert
}

// Service builds the DNS-SD service for an advert. TXT keys (RFC 6763
// §6): txtvers first, short keys, no secrets — anyone on the LAN can read
// them. "setup=pending" lets a client say "this server is not set up
// yet; open it in a browser first".
func (a *Advertiser) Service(ad api.Advert) mdns.Service {
	setup := "pending"
	if ad.SetupDone {
		setup = "done"
	}
	return mdns.Service{
		Instance: mdns.TruncateLabel("Null Drive on " + ad.Name),
		Host:     "nulldrive-" + mdns.SanitizeLabel(ad.Name),
		Port:     a.Port,
		TXT: []string{
			"txtvers=1",
			"version=" + buildinfo.Version,
			"name=" + ad.Name,
			"setup=" + setup,
			"api=/api/v1",
		},
	}
}

// Apply starts, updates or stops advertising.
func (a *Advertiser) Apply(ad api.Advert) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.last = ad
	if !ad.Enabled {
		if a.adv != nil {
			a.adv.Stop()
			a.adv = nil
			a.Log.Info("lan discovery stopped")
		}
		a.status = api.DiscoveryStatus{}
		return
	}
	svc := a.Service(ad)
	if a.adv != nil {
		a.adv.R.SetService(svc)
		a.adv.Reannounce()
		return
	}
	r := mdns.NewResponder(svc, a.Log)
	r.Override = a.Override
	adv := &mdns.Advertiser{R: r}
	if err := adv.Start(); err != nil {
		a.status = api.DiscoveryStatus{Err: err.Error()}
		a.Log.Warn("lan discovery unavailable", "err", err,
			"hint", "in Docker, mDNS needs network_mode: host (see docs/INSTALL.md)")
		return
	}
	a.adv = adv
	st := api.DiscoveryStatus{Active: true}
	for _, ip := range adv.Addrs() {
		st.Addrs = append(st.Addrs, ip.String())
	}
	if warn := dockerBridgeWarning(adv.Addrs()); warn != "" {
		st.Err = warn
		a.Log.Warn(warn)
	}
	a.status = st
	a.Log.Info("lan discovery active", "instance", svc.Instance, "port", a.Port, "addrs", st.Addrs)
}

// Status reports the current state.
func (a *Advertiser) Status() api.DiscoveryStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

// Stop sends goodbye packets (on shutdown).
func (a *Advertiser) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.adv != nil {
		a.adv.Stop()
		a.adv = nil
	}
}

var dockerNet = netip.MustParsePrefix("172.16.0.0/12")

// dockerBridgeWarning detects the classic mistake: the server runs in a
// container on Docker's default bridge network. Multicast does not cross
// the bridge, and the only addresses the container knows are internal
// 172.x ones, so even a forwarded announcement would point at nothing.
func dockerBridgeWarning(addrs []netip.Addr) string {
	if _, err := os.Stat("/.dockerenv"); err != nil {
		return ""
	}
	for _, a := range addrs {
		if !dockerNet.Contains(a) && a.Is4() {
			return ""
		}
	}
	return "running in a Docker bridge network: LAN discovery cannot reach your network. " +
		"Use network_mode: host, or set NULLDRIVE_ADVERTISE_ADDR to the host's LAN address (with a reflector)"
}
