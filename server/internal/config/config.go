// Package config loads server settings from environment variables.
//
// Only bootstrap settings live here (where to store data, which port).
// Everything a user configures after first run is stored in the database,
// so a Docker user never has to edit a file.
package config

import (
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DataDir  string
	Port     int
	LogLevel slog.Level

	// TrustProxy: take client addresses from X-Forwarded-For.
	TrustProxy bool
	// MDNS: false when NULLDRIVE_MDNS=off (overrides the UI setting).
	MDNS bool
	// AdvertiseAddrs replaces the auto-detected LAN addresses in mDNS
	// answers; AdvertisePort replaces the port (Docker port mapping).
	AdvertiseAddrs []netip.Addr
	AdvertisePort  int

	// GCInterval: how often retention + garbage collection run (Phase 6).
	GCInterval time.Duration

	// SearchOrigins may call GET /search from a browser (CORS). Default:
	// the Null Search web app.
	SearchOrigins []string
}

// DefaultSearchOrigin is Null Search's public web app.
const DefaultSearchOrigin = "https://search.nullgrid.dev"

// Load reads configuration using getenv (os.Getenv in production; a map in
// tests) and applies defaults.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		DataDir:    "./data",
		Port:       8080,
		LogLevel:   slog.LevelInfo,
		MDNS:       true,
		GCInterval: time.Hour,
	}
	if v := strings.TrimSpace(getenv("NULLDRIVE_DATA_DIR")); v != "" {
		cfg.DataDir = v
	}
	abs, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return Config{}, fmt.Errorf("NULLDRIVE_DATA_DIR: %w", err)
	}
	cfg.DataDir = abs

	if v := strings.TrimSpace(getenv("NULLDRIVE_PORT")); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			return Config{}, fmt.Errorf("NULLDRIVE_PORT: %q is not a valid port", v)
		}
		cfg.Port = p
	}

	if v := strings.TrimSpace(getenv("NULLDRIVE_LOG_LEVEL")); v != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(v)); err != nil {
			return Config{}, fmt.Errorf("NULLDRIVE_LOG_LEVEL: %q (want debug, info, warn or error)", v)
		}
	}
	if v := strings.TrimSpace(getenv("NULLDRIVE_TRUST_PROXY")); v != "" {
		b, err := parseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("NULLDRIVE_TRUST_PROXY: %w", err)
		}
		cfg.TrustProxy = b
	}
	if v := strings.TrimSpace(getenv("NULLDRIVE_MDNS")); v != "" {
		b, err := parseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("NULLDRIVE_MDNS: %w", err)
		}
		cfg.MDNS = b
	}
	if v := strings.TrimSpace(getenv("NULLDRIVE_ADVERTISE_ADDR")); v != "" {
		for _, f := range strings.Split(v, ",") {
			a, err := netip.ParseAddr(strings.TrimSpace(f))
			if err != nil {
				return Config{}, fmt.Errorf("NULLDRIVE_ADVERTISE_ADDR: %q is not an IP address", f)
			}
			cfg.AdvertiseAddrs = append(cfg.AdvertiseAddrs, a)
		}
	}
	if v := strings.TrimSpace(getenv("NULLDRIVE_GC_INTERVAL")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Minute {
			return Config{}, fmt.Errorf("NULLDRIVE_GC_INTERVAL: %q (want a duration of at least 1m, e.g. 1h)", v)
		}
		cfg.GCInterval = d
	}
	cfg.SearchOrigins = []string{DefaultSearchOrigin}
	if v := strings.TrimSpace(getenv("NULLDRIVE_SEARCH_ORIGINS")); v != "" {
		origins, err := parseOrigins(v)
		if err != nil {
			return Config{}, fmt.Errorf("NULLDRIVE_SEARCH_ORIGINS: %w", err)
		}
		cfg.SearchOrigins = origins
	}
	cfg.AdvertisePort = cfg.Port
	if v := strings.TrimSpace(getenv("NULLDRIVE_ADVERTISE_PORT")); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			return Config{}, fmt.Errorf("NULLDRIVE_ADVERTISE_PORT: %q is not a valid port", v)
		}
		cfg.AdvertisePort = p
	}
	return cfg, nil
}

// parseOrigins reads a comma-separated list of web origins
// ("https://search.example.com,http://localhost:5173"), or "off" for none.
func parseOrigins(v string) ([]string, error) {
	if b, err := parseBool(v); err == nil && !b {
		return []string{}, nil
	}
	var out []string
	for _, f := range strings.Split(v, ",") {
		f = strings.TrimRight(strings.TrimSpace(f), "/")
		if f == "" {
			continue
		}
		u, err := url.Parse(f)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
			u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return nil, fmt.Errorf("%q is not an origin (want scheme://host[:port], e.g. https://search.nullgrid.dev)", f)
		}
		out = append(out, strings.ToLower(u.Scheme+"://"+u.Host))
	}
	return out, nil
}

func parseBool(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("%q (want on/off, true/false)", v)
}

// Addr is the listen address.
func (c Config) Addr() string { return fmt.Sprintf(":%d", c.Port) }
