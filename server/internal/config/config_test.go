package config

import (
	"log/slog"
	"net/netip"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	absData, _ := filepath.Abs("./data")
	def := []string{DefaultSearchOrigin}
	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr bool
	}{
		{"defaults", nil, Config{DataDir: absData, Port: 8080, LogLevel: slog.LevelInfo, MDNS: true, AdvertisePort: 8080, GCInterval: time.Hour, SearchOrigins: def}, false},
		{"all set", map[string]string{
			"NULLDRIVE_DATA_DIR":       "/srv/nulldrive",
			"NULLDRIVE_PORT":           "9000",
			"NULLDRIVE_LOG_LEVEL":      "debug",
			"NULLDRIVE_TRUST_PROXY":    "true",
			"NULLDRIVE_MDNS":           "off",
			"NULLDRIVE_ADVERTISE_ADDR": "192.168.1.20, fd00::20",
			"NULLDRIVE_ADVERTISE_PORT": "443",
			"NULLDRIVE_GC_INTERVAL":    "6h",
			"NULLDRIVE_SEARCH_ORIGINS": "https://Search.Example.com/, http://localhost:5173",
		}, Config{DataDir: "/srv/nulldrive", Port: 9000, LogLevel: slog.LevelDebug, TrustProxy: true, MDNS: false,
			AdvertiseAddrs: []netip.Addr{netip.MustParseAddr("192.168.1.20"), netip.MustParseAddr("fd00::20")}, AdvertisePort: 443,
			GCInterval: 6 * time.Hour, SearchOrigins: []string{"https://search.example.com", "http://localhost:5173"}}, false},
		{"search origins off", map[string]string{"NULLDRIVE_SEARCH_ORIGINS": "off"},
			Config{DataDir: absData, Port: 8080, LogLevel: slog.LevelInfo, MDNS: true, AdvertisePort: 8080, GCInterval: time.Hour, SearchOrigins: []string{}}, false},
		{"search origin with a path", map[string]string{"NULLDRIVE_SEARCH_ORIGINS": "https://search.example.com/app"}, Config{}, true},
		{"search origin without a scheme", map[string]string{"NULLDRIVE_SEARCH_ORIGINS": "search.example.com"}, Config{}, true},
		{"gc interval too short", map[string]string{"NULLDRIVE_GC_INTERVAL": "5s"}, Config{}, true},
		{"log level case-insensitive", map[string]string{"NULLDRIVE_LOG_LEVEL": "WARN"},
			Config{DataDir: absData, Port: 8080, LogLevel: slog.LevelWarn, MDNS: true, AdvertisePort: 8080, GCInterval: time.Hour, SearchOrigins: def}, false},
		{"bad bool", map[string]string{"NULLDRIVE_MDNS": "maybe"}, Config{}, true},
		{"bad advertise addr", map[string]string{"NULLDRIVE_ADVERTISE_ADDR": "nas.local"}, Config{}, true},
		{"bad port", map[string]string{"NULLDRIVE_PORT": "http"}, Config{}, true},
		{"port out of range", map[string]string{"NULLDRIVE_PORT": "70000"}, Config{}, true},
		{"bad log level", map[string]string{"NULLDRIVE_LOG_LEVEL": "loud"}, Config{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Load(func(k string) string { return tt.env[k] })
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
