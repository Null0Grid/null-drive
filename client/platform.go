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
	exe, err := selfPath()
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
		if inFlatpak() {
			return flatpakAutostart(enabled)
		}
		cfg, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		desktop := filepath.Join(cfg, "autostart", "nulldrive-desktop.desktop")
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

// selfPath is the program to point autostart entries and link handlers at.
// Inside an AppImage, os.Executable is a temporary mount that changes on
// every start; the AppImage file itself is in $APPIMAGE.
func selfPath() (string, error) {
	if p := os.Getenv("APPIMAGE"); runtime.GOOS == "linux" && p != "" {
		return p, nil
	}
	return os.Executable()
}

// inFlatpak reports whether the app runs in a Flatpak sandbox.
func inFlatpak() bool {
	return runtime.GOOS == "linux" && os.Getenv("FLATPAK_ID") != ""
}

// flatpakAutostart turns start-on-login on or off through the Background
// portal. A sandboxed app can't write ~/.config/autostart (and an entry
// there would point at a path inside the sandbox); the portal asks the user
// once and writes an entry that runs the app with `flatpak run`.
// gdbus is in the GNOME runtime, so this needs no D-Bus library.
func flatpakAutostart(enabled bool) error {
	cmd := exec.Command("gdbus", flatpakAutostartArgs(enabled)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("background portal: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func flatpakAutostartArgs(enabled bool) []string {
	return []string{
		"call", "--session",
		"--dest", "org.freedesktop.portal.Desktop",
		"--object-path", "/org/freedesktop/portal/desktop",
		"--method", "org.freedesktop.portal.Background.RequestBackground",
		"", // no parent window
		fmt.Sprintf("{'reason': <'Keep your Null Drive folder in sync after you sign in'>, "+
			"'autostart': <%t>, 'commandline': <['nulldrive-desktop', '--hidden']>, "+
			"'dbus-activatable': <false>}", enabled),
	}
}
