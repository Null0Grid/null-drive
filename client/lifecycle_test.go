package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestForgetEverything(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("relocates the config dir with XDG_CONFIG_HOME (Linux only)")
	}
	keyring.MockInit()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	synced := t.TempDir()
	if err := os.WriteFile(filepath.Join(synced, "keep.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{ServerURL: "http://nas:8080", DeviceName: "PC", Folder: synced, PollSeconds: 30}
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := saveToken(cfg, "ndd_secret"); err != nil {
		t.Fatal(err)
	}
	if err := keyring.Set(keychainService, keyAccount(cfg), "00"); err != nil {
		t.Fatal(err)
	}
	if err := forgetEverything(); err != nil {
		t.Fatal(err)
	}
	d, _ := appDir()
	if _, err := os.Stat(d); !os.IsNotExist(err) {
		t.Fatalf("settings folder still there: %v", err)
	}
	if _, err := loadToken(cfg); err == nil {
		t.Fatal("device token still in the keychain")
	}
	if _, err := keyring.Get(keychainService, keyAccount(cfg)); err == nil {
		t.Fatal("encryption key still in the keychain")
	}
	if b, err := os.ReadFile(filepath.Join(synced, "keep.txt")); err != nil || string(b) != "mine" {
		t.Fatal("synced files must not be touched")
	}
	// Nothing configured: still fine.
	if err := forgetEverything(); err != nil {
		t.Fatal(err)
	}
}
