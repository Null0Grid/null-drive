//go:build linux

package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed build/appicon.png
var appIconPNG []byte

const desktopID = "nulldrive-desktop.desktop"

// registerURLScheme makes nulldrive:// links open this copy, the Linux way:
// a .desktop entry that declares MimeType=x-scheme-handler/nulldrive, then
// `xdg-mime default`.
//
// The .deb and .rpm install that entry system-wide (/usr/share/applications)
// and need nothing here. The AppImage and the .tar.gz have no installer, so
// the first start writes a per-user entry (~/.local/share/applications),
// which also puts Null Drive in the app menu. Like the portable Windows
// copy, it leaves the scheme alone when another installed copy owns it.
func registerURLScheme() error {
	exe, err := selfPath()
	if err != nil {
		return err
	}
	if strings.HasPrefix(exe, "/usr/") || strings.HasPrefix(exe, "/opt/") {
		return nil // installed by a package: its .desktop file handles links
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	apps := filepath.Join(dataHome, "applications")
	desktop := filepath.Join(apps, desktopID)
	if cur := currentHandler(); cur != "" && cur != desktopID {
		if desktopFileExists(dataHome, cur) {
			return nil // another copy (a package install) owns the scheme
		}
	}
	entry := desktopEntry(exe)
	if old, err := os.ReadFile(desktop); err == nil && string(old) == entry && currentHandler() == desktopID {
		return nil // already us
	}
	icon := filepath.Join(dataHome, "icons", "hicolor", "1024x1024", "apps", "nulldrive-desktop.png")
	for _, d := range []string{apps, filepath.Dir(icon)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(icon, appIconPNG, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(desktop, []byte(entry), 0o644); err != nil {
		return err
	}
	_ = exec.Command("update-desktop-database", apps).Run() // optional
	if out, err := exec.Command("xdg-mime", "default", desktopID, "x-scheme-handler/nulldrive").CombinedOutput(); err != nil {
		return fmt.Errorf("xdg-mime: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// desktopEntry quotes the program path as the Desktop Entry spec requires
// (inside double quotes, \ " ` $ are escaped with a backslash; % starts
// a field code, so a literal one is %%).
func desktopEntry(exe string) string {
	q := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`, `%`, `%%`).Replace(exe)
	return "[Desktop Entry]\nType=Application\nName=Null Drive\nComment=Keep a folder in sync with your Null Drive server\n" +
		"Exec=\"" + q + "\" %u\nIcon=nulldrive-desktop\nCategories=Network;FileTransfer;\n" +
		"MimeType=x-scheme-handler/nulldrive;\nStartupNotify=false\nStartupWMClass=nulldrive-desktop\n"
}

func currentHandler() string {
	out, err := exec.Command("xdg-mime", "query", "default", "x-scheme-handler/nulldrive").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func desktopFileExists(dataHome, id string) bool {
	dirs := []string{filepath.Join(dataHome, "applications")}
	xdg := os.Getenv("XDG_DATA_DIRS")
	if xdg == "" {
		xdg = "/usr/local/share:/usr/share"
	}
	for _, d := range strings.Split(xdg, ":") {
		dirs = append(dirs, filepath.Join(d, "applications"))
	}
	for _, d := range dirs {
		if _, err := os.Stat(filepath.Join(d, id)); err == nil {
			return true
		}
	}
	return false
}
