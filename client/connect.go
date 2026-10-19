package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/nulldrive/nulldrive/shared/client"
	"github.com/nulldrive/nulldrive/shared/mdns"
	"github.com/nulldrive/nulldrive/shared/netutil"
	"github.com/nulldrive/nulldrive/shared/pathutil"
)

// This file is the "connect to a server" half of first-run setup: LAN
// discovery, checking an address (including TLS trust), and pairing.

func userAgent(version string) string {
	return fmt.Sprintf("NullDrive/%s (%s/%s)", version, runtime.GOOS, runtime.GOARCH)
}

// DiscoveredServer is one server found on the LAN.
type DiscoveredServer struct {
	Name      string   `json:"name"`    // server name from TXT, e.g. "nas"
	URL       string   `json:"url"`     // best guess, e.g. http://192.168.1.20:8080
	Address   string   `json:"address"` // shown in the UI: "192.168.1.20"
	Version   string   `json:"version"`
	SetupDone bool     `json:"setup_done"`
	Addrs     []string `json:"addrs"`
}

// Discover browses the LAN for Null Drive servers (about 1.5 seconds).
func (a *App) Discover() ([]DiscoveredServer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	found, err := mdns.Browse(ctx, mdns.BrowseOptions{})
	if err != nil {
		a.log.Warn("lan discovery failed", "err", err)
		return []DiscoveredServer{}, nil // not fatal: manual entry still works
	}
	out := []DiscoveredServer{}
	for _, f := range found {
		if d, ok := toDiscovered(f, localPrefixes()); ok {
			out = append(out, d)
		}
	}
	return out, nil
}

func localPrefixes() []netip.Prefix {
	ifs, _ := mdns.Interfaces()
	var out []netip.Prefix
	for _, ia := range ifs {
		out = append(out, ia.Prefix)
	}
	return out
}

// toDiscovered picks the address to connect to: one on a subnet this
// computer is directly attached to if possible (a multi-homed server
// may also list a storage-network or VPN address we cannot reach), IPv4
// before IPv6, global before link-local (which needs a zone).
func toDiscovered(f mdns.Found, local []netip.Prefix) (DiscoveredServer, bool) {
	if len(f.Addrs) == 0 || f.Port == 0 {
		return DiscoveredServer{}, false
	}
	score := func(a netip.Addr) int {
		s := 0
		for _, p := range local {
			if p.Contains(a) {
				s += 4
				break
			}
		}
		if a.Is4() {
			s += 2
		}
		if !a.IsLinkLocalUnicast() {
			s++
		}
		return s
	}
	best := f.Addrs[0]
	for _, a := range f.Addrs[1:] {
		if score(a) > score(best) {
			best = a
		}
	}
	name := f.TXT["name"]
	if name == "" {
		name = strings.TrimPrefix(f.Instance, "Null Drive on ")
	}
	host := best.String()
	if best.Is6() {
		host = "[" + host + "]"
	}
	d := DiscoveredServer{
		Name:      name,
		URL:       "http://" + host + ":" + strconv.Itoa(int(f.Port)),
		Address:   best.String(),
		Version:   f.TXT["version"],
		SetupDone: f.TXT["setup"] != "pending",
	}
	for _, a := range f.Addrs {
		d.Addrs = append(d.Addrs, a.String())
	}
	return d, true
}

// ServerInfo is the result of checking an address.
type ServerInfo struct {
	URL     string `json:"url"`
	Version string `json:"version"`
	// Warning is set for plain HTTP to a non-LAN address: passwords,
	// tokens and files would cross the internet unencrypted.
	Warning string `json:"warning,omitempty"`
	// Cert is set for HTTPS. When Cert.Trusted is false the UI must show
	// the fingerprint and get an explicit "Trust" before continuing, then
	// pass Cert.Fingerprint as cert_sha256.
	Cert *client.CertInfo `json:"cert,omitempty"`
}

// TestServer checks that an address is a Null Drive server and reports
// what the user should know about the connection's security. pin is a
// fingerprint the user already accepted ("" if none).
func (a *App) TestServer(raw, pin string) (ServerInfo, error) {
	u, err := client.NormalizeURL(raw)
	if err != nil {
		return ServerInfo{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	info := ServerInfo{URL: u}
	parsed, _ := url.Parse(u)
	if parsed.Scheme == "https" {
		cert, err := client.ProbeTLS(ctx, u)
		if err != nil {
			return ServerInfo{}, fmt.Errorf("could not connect to %s: %w", u, friendlyErr(err))
		}
		info.Cert = cert
		if !cert.Trusted && pin == "" {
			return info, nil // UI asks the user to trust the fingerprint first
		}
		if cert.Trusted {
			pin = "" // a valid certificate needs no pin (and survives renewals)
		}
	} else if !netutil.IsLocalHost(ctx, parsed.Hostname(), nil) {
		info.Warning = "This address is on the internet but the connection is not encrypted (http://). " +
			"Your password, device token and files could be read or altered on the way. " +
			"Use https:// (e.g. behind a reverse proxy) unless this is a VPN address."
	}
	h, err := client.NewPinned(u, "", pin).Health(ctx)
	if err != nil {
		return ServerInfo{}, fmt.Errorf("no Null Drive server answered at %s: %w", u, friendlyErr(err))
	}
	info.Version = h.Version
	return info, nil
}

// PairRequest is "Pair with code" from the setup screen.
type PairRequest struct {
	ServerURL  string `json:"server_url"`
	Code       string `json:"code"` // code, or a full nulldrive://pair link
	DeviceName string `json:"device_name"`
	Folder     string `json:"folder"`
	CertSHA256 string `json:"cert_sha256"`
}

// PairWithCode exchanges a pairing code (or link) for a device token.
func (a *App) PairWithCode(req PairRequest) error {
	server, code := req.ServerURL, req.Code
	if s, c, ok, err := client.ParsePairLink(req.Code); ok {
		if err != nil {
			return err
		}
		server, code = s, c
	}
	u, err := client.NormalizeURL(server)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(req.DeviceName)
	if name == "" {
		return errors.New("give this computer a name")
	}
	folder := strings.TrimSpace(req.Folder)
	if folder == "" || !filepath.IsAbs(folder) {
		return errors.New("choose a folder to sync")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := client.NewPinned(u, "", req.CertSHA256).ClaimPairing(ctx, code, name)
	switch {
	case client.IsCode(err, "invalid_code"):
		return errors.New("that code is not valid or has expired. Create a new one in the web interface (Settings → Devices → Add device)")
	case client.IsCode(err, "rate_limited"):
		return errors.New("too many attempts. Wait a minute and try again")
	case err != nil:
		return friendlyErr(err)
	}
	return a.finishSetup(Config{
		ServerURL: u, Username: res.User.Username, DeviceName: name, DeviceID: res.Device.ID,
		Folder: folder, PollSeconds: 30, CertSHA256: req.CertSHA256,
	}, res.Token)
}

// ParsePairLink lets the UI pre-fill the server address when a pairing
// link is pasted into the code box.
func (a *App) ParsePairLink(s string) (map[string]string, error) {
	server, code, ok, err := client.ParsePairLink(s)
	if !ok || err != nil {
		return map[string]string{}, err
	}
	return map[string]string{"server": server, "code": code}, nil
}

// friendlyErr shortens the most common network errors.
func friendlyErr(err error) error {
	var dnsErr *net.DNSError
	var opErr *net.OpError
	switch {
	case errors.Is(err, client.ErrCertChanged):
		return errors.New("the server's certificate has changed since you trusted it. " +
			"If you replaced it yourself, check the new fingerprint and trust it again; otherwise someone may be intercepting the connection")
	case errors.As(err, &dnsErr):
		return fmt.Errorf("the name %q could not be found", dnsErr.Name)
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("the server did not answer in time")
	case errors.As(err, &opErr) && opErr.Op == "dial":
		return errors.New("nothing is listening at that address (connection refused or unreachable)")
	}
	return err
}

// historyURL is the web app page with path's version history open:
// /files/<folder>?history=<name>. History, restore and the
// recently-deleted bin live in the web app; the desktop app links there
// instead of duplicating them.
func historyURL(server, p string) (string, error) {
	clean, err := pathutil.Clean(p)
	if err != nil {
		return "", err
	}
	dir, name := path.Split(clean)
	dir = strings.TrimSuffix(dir, "/")
	u := strings.TrimRight(server, "/") + "/files"
	if dir != "" {
		u += "/" + client.EncodePath(dir)
	}
	return u + "?history=" + url.QueryEscape(name), nil
}

// OpenHistory opens a file's version history in the browser.
func (a *App) OpenHistory(p string) error {
	a.mu.Lock()
	server := a.cfg.ServerURL
	a.mu.Unlock()
	u, err := historyURL(server, p)
	if err != nil {
		return err
	}
	wruntime.BrowserOpenURL(a.ctx, u)
	return nil
}

// OpenWebApp opens a page of the web app ("trash", "settings" or "").
func (a *App) OpenWebApp(page string) error {
	switch page {
	case "", "trash", "settings", "files":
	default:
		return fmt.Errorf("unknown page %q", page)
	}
	a.mu.Lock()
	server := a.cfg.ServerURL
	a.mu.Unlock()
	wruntime.BrowserOpenURL(a.ctx, strings.TrimRight(server, "/")+"/"+page)
	return nil
}
