package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// openPath opens a folder (or file) with the OS file manager / default app.
func openPath(p string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", p)
	case "darwin":
		cmd = exec.Command("open", p)
	default:
		cmd = exec.Command("xdg-open", p)
	}
	hideConsole(cmd)
	return cmd.Start()
}

// setStartOnLogin registers/unregisters the app to start (in the tray) at
// login, using each OS's per-user mechanism. No admin rights needed.
func setStartOnLogin(enabled bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "windows":
		const key = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
		var cmd *exec.Cmd
		if enabled {
			cmd = exec.Command("reg", "add", key, "/v", "NullDrive", "/t", "REG_SZ", "/d", fmt.Sprintf(`"%s" --hidden`, exe), "/f")
		} else {
			cmd = exec.Command("reg", "delete", key, "/v", "NullDrive", "/f")
		}
		hideConsole(cmd)
		out, err := cmd.CombinedOutput()
		if err != nil && enabled {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	case "darwin":
		home, _ := os.UserHomeDir()
		plist := filepath.Join(home, "Library", "LaunchAgents", "io.nulldrive.client.plist")
		if !enabled {
			_ = os.Remove(plist)
			return nil
		}
		content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>io.nulldrive.client</string>
  <key>ProgramArguments</key><array><string>%s</string><string>--hidden</string></array>
  <key>RunAtLoad</key><true/>
</dict></plist>
`, exe)
		if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
			return err
		}
		return os.WriteFile(plist, []byte(content), 0o644)
	default:
		cfg, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		desktop := filepath.Join(cfg, "autostart", "nulldrive.desktop")
		if !enabled {
			_ = os.Remove(desktop)
			return nil
		}
		content := fmt.Sprintf("[Desktop Entry]\nType=Application\nName=Null Drive\nExec=\"%s\" --hidden\nX-GNOME-Autostart-enabled=true\n", exe)
		if err := os.MkdirAll(filepath.Dir(desktop), 0o755); err != nil {
			return err
		}
		return os.WriteFile(desktop, []byte(content), 0o644)
	}
}
