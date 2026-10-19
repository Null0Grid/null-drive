package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Config is the client's persisted settings. The device token is NOT here:
// it lives in the OS keychain (see keychain.go).
type Config struct {
	ServerURL    string `json:"server_url"`
	Username     string `json:"username"`
	DeviceName   string `json:"device_name"`
	DeviceID     int64  `json:"device_id"`
	Folder       string `json:"folder"`
	StartOnLogin bool   `json:"start_on_login"`
	UpKBps       int64  `json:"up_kbps"`   // 0 = unlimited
	DownKBps     int64  `json:"down_kbps"` // 0 = unlimited
	PollSeconds  int    `json:"poll_seconds"`
	// CertSHA256 pins a self-signed server certificate (trust on first
	// use). Empty = normal certificate verification.
	CertSHA256 string `json:"cert_sha256,omitempty"`
	// E2EEFingerprint identifies the end-to-end encryption key this
	// computer unlocked (not secret; the key is in the keychain).
	E2EEFingerprint string `json:"e2ee_fingerprint,omitempty"`
}

// Configured reports whether first-run setup has been completed.
func (c Config) Configured() bool { return c.ServerURL != "" && c.Folder != "" }

// appDir is %AppData%\Null Drive (Windows), ~/Library/Application Support/Null Drive
// (macOS) or ~/.config/Null Drive (Linux).
func appDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "Null Drive"), nil
}

func configPath() (string, error) {
	d, err := appDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

func loadConfig() (Config, error) {
	p, err := configPath()
	if err != nil {
		return Config{}, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return Config{PollSeconds: 30}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, err
	}
	if c.PollSeconds <= 0 {
		c.PollSeconds = 30
	}
	return c, nil
}

// saveConfig writes atomically (temp file + rename) so a crash can't leave
// a half-written config.
func saveConfig(c Config) error {
	p, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// indexPath is unique per (server, folder) pair: pointing the client at a
// different server or folder starts from a fresh index instead of
// misapplying old sync state.
func indexPath(c Config) (string, error) {
	d, err := appDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(c.ServerURL + "\x00" + c.Folder))
	return filepath.Join(d, "index-"+hex.EncodeToString(sum[:6])+".db"), nil
}

func logPath() (string, error) {
	d, err := appDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "logs", "client.log"), nil
}

// forgetEverything removes this computer's Null Drive settings: the device
// token and encryption key in the OS keychain, and the settings folder
// (config, sync index, logs). The synced folder itself is not touched. The
// uninstaller runs "NullDrive --forget" when the user chooses to remove
// settings; without it the keychain entries would outlive the app.
func forgetEverything() error {
	if cfg, err := loadConfig(); err == nil && cfg.Configured() {
		_ = deleteToken(cfg)
		deleteMasterKey(cfg)
	}
	d, err := appDir()
	if err != nil {
		return err
	}
	return os.RemoveAll(d)
}
