//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopEntryQuotesThePath(t *testing.T) {
	e := desktopEntry(`/home/a b/My "Apps"/$x/100%/Null Drive.AppImage`)
	if !strings.Contains(e, `Exec="/home/a b/My \"Apps\"/\$x/100%%/Null Drive.AppImage" %u`+"\n") {
		t.Fatalf("Exec line not quoted per the Desktop Entry spec:\n%s", e)
	}
	for _, want := range []string{"MimeType=x-scheme-handler/nulldrive;", "Icon=nulldrive-desktop", "Type=Application"} {
		if !strings.Contains(e, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

// An AppImage (or unpacked tarball) registers itself on first start: a
// per-user .desktop entry, the icon, and the xdg-mime default.
func TestRegisterURLSchemeForAppImage(t *testing.T) {
	data := t.TempDir()
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	// Fake xdg-mime / update-desktop-database that record their arguments.
	fake := "#!/bin/sh\necho \"$(basename \"$0\") $*\" >> " + log + "\n"
	for _, n := range []string{"xdg-mime", "update-desktop-database"} {
		if err := os.WriteFile(filepath.Join(bin, n), []byte(fake), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("APPIMAGE", "/home/me/Apps/NullDrive.AppImage")
	if err := registerURLScheme(); err != nil {
		t.Fatal(err)
	}
	entry, err := os.ReadFile(filepath.Join(data, "applications", desktopID))
	if err != nil || !strings.Contains(string(entry), `Exec="/home/me/Apps/NullDrive.AppImage" %u`) {
		t.Fatalf("desktop entry: %v\n%s", err, entry)
	}
	if _, err := os.Stat(filepath.Join(data, "icons", "hicolor", "1024x1024", "apps", "nulldrive-desktop.png")); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "xdg-mime default nulldrive-desktop.desktop x-scheme-handler/nulldrive") {
		t.Fatalf("xdg-mime not called: %s", calls)
	}
	// Installed by a package (/usr/...): leave it to the package.
	t.Setenv("APPIMAGE", "/usr/bin/nulldrive-desktop")
	_ = os.Remove(log)
	if err := registerURLScheme(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Fatalf("a package install must not register itself: %s", b)
	}
}

// Inside Flatpak the exported .desktop file owns the scheme, and autostart
// goes through the Background portal.
func TestFlatpak(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	fake := "#!/bin/sh\nfor a in \"$@\"; do echo \"$a\"; done >> " + log + "\n"
	for _, n := range []string{"xdg-mime", "update-desktop-database", "gdbus"} {
		if err := os.WriteFile(filepath.Join(bin, n), []byte(fake), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("FLATPAK_ID", "dev.nullgrid.NullDrive")
	if err := registerURLScheme(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("registered the scheme inside Flatpak")
	}
	if err := setStartOnLogin(true); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(log)
	for _, want := range []string{
		"org.freedesktop.portal.Background.RequestBackground",
		"'autostart': <true>",
		"'commandline': <['nulldrive-desktop', '--hidden']>",
	} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("portal call misses %q:\n%s", want, got)
		}
	}
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	if err := setStartOnLogin(false); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(log)
	if !strings.Contains(string(got), "'autostart': <false>") {
		t.Fatalf("turning it off doesn't ask the portal:\n%s", got)
	}
}
