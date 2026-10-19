//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	shlwapi  = windows.NewLazySystemDLL("shlwapi.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procAssocIsDangerous            = shlwapi.NewProc("AssocIsDangerous")
	procILCreateFromPathW           = shell32.NewProc("ILCreateFromPathW")
	procILFree                      = shell32.NewProc("ILFree")
	procSHOpenFolderAndSelectItems  = shell32.NewProc("SHOpenFolderAndSelectItems")
	procGetCurrentPackageFullName   = kernel32.NewProc("GetCurrentPackageFullName")
	procGetCurrentPackageFamilyName = kernel32.NewProc("GetCurrentPackageFamilyName")
)

// systemSaysDangerous asks Windows whether a file type is one it considers
// unsafe to open (AssocIsDangerous: the list Explorer and Office use, plus
// anything an administrator added by policy).
func systemSaysDangerous(ext string) bool {
	if procAssocIsDangerous.Find() != nil {
		return false
	}
	p, err := windows.UTF16PtrFromString(ext)
	if err != nil {
		return true
	}
	r, _, _ := procAssocIsDangerous.Call(uintptr(unsafe.Pointer(p))) //nolint:gosec // NUL-terminated UTF-16
	return r != 0
}

// shellOpen opens a file with its default app (or a folder in Explorer)
// through ShellExecute. Unlike `explorer <path>`, nothing in the path is
// parsed as a command-line switch (Explorer treats commas specially).
func shellOpen(p string) error {
	return windows.ShellExecute(0, windows.StringToUTF16Ptr("open"), windows.StringToUTF16Ptr(p), nil,
		windows.StringToUTF16Ptr(filepath.Dir(p)), windows.SW_SHOWNORMAL)
}

// shellReveal opens the file's folder in Explorer with the file selected,
// without running it.
func shellReveal(p string) error {
	if err := procSHOpenFolderAndSelectItems.Find(); err == nil {
		// COM must be initialized on the calling thread; S_FALSE (already
		// initialized) is fine.
		_ = windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED)
		path, err := windows.UTF16PtrFromString(p)
		if err == nil {
			pidl, _, _ := procILCreateFromPathW.Call(uintptr(unsafe.Pointer(path))) //nolint:gosec // NUL-terminated UTF-16
			if pidl != 0 {
				hr, _, _ := procSHOpenFolderAndSelectItems.Call(pidl, 0, 0, 0)
				procILFree.Call(pidl) //nolint:errcheck // frees memory, no result
				if hr == 0 {
					return nil
				}
			}
		}
	}
	return shellOpen(filepath.Dir(p)) // at least show the folder
}

// appModelErrorNoPackage: GetCurrentPackage* in an unpackaged process.
const appModelErrorNoPackage = 15700

// isPackaged reports whether the app runs from an MSIX package (the
// Microsoft Store build). Packaged apps get their protocol and startup
// task from the manifest, and their AppData and HKCU writes are
// redirected to a private per-package location.
func isPackaged() bool {
	if procGetCurrentPackageFullName.Find() != nil {
		return false // before Windows 8
	}
	var n uint32
	r, _, _ := procGetCurrentPackageFullName.Call(uintptr(unsafe.Pointer(&n)), 0) //nolint:gosec // out-parameter
	return r != appModelErrorNoPackage
}

func packageFamilyName() string {
	if procGetCurrentPackageFamilyName.Find() != nil {
		return ""
	}
	var n uint32
	procGetCurrentPackageFamilyName.Call(uintptr(unsafe.Pointer(&n)), 0) //nolint:errcheck,gosec // size query
	if n == 0 || n > 1024 {
		return ""
	}
	buf := make([]uint16, n)
	r, _, _ := procGetCurrentPackageFamilyName.Call(uintptr(unsafe.Pointer(&n)), uintptr(unsafe.Pointer(&buf[0]))) //nolint:gosec // sized buffer
	if r != 0 {
		return ""
	}
	return windows.UTF16ToString(buf)
}

// realPath maps a path under AppData to where a packaged app's writes
// actually land (%LOCALAPPDATA%\Packages\<family>\LocalCache\...), so
// "Open logs" shows Explorer a folder that exists for it. Unpackaged, or
// if nothing is there, the path is returned as is.
func realPath(p string) string {
	if !isPackaged() {
		return p
	}
	fam := packageFamilyName()
	local := os.Getenv("LOCALAPPDATA")
	if fam == "" || local == "" {
		return p
	}
	cache := filepath.Join(local, "Packages", fam, "LocalCache")
	for _, m := range []struct{ env, sub string }{{"APPDATA", "Roaming"}, {"LOCALAPPDATA", "Local"}} {
		base := os.Getenv(m.env)
		if base == "" {
			continue
		}
		if rel, err := filepath.Rel(base, p); err == nil && !strings.HasPrefix(rel, "..") {
			cand := filepath.Join(cache, m.sub, rel)
			if _, err := os.Stat(cand); err == nil {
				return cand
			}
		}
	}
	return p
}

const urlKey = `Software\Classes\` + linkScheme

// registerURLScheme makes nulldrive:// links open this copy when nothing
// else handles them: the portable zip has no installer to register it.
// An installed copy that registered itself (and still exists) is left
// alone, so running a portable copy once doesn't take links over. Per user
// (HKCU), no admin rights. The Store build declares the protocol in its
// manifest instead (its HKCU writes would be private to the package).
func registerURLScheme() error {
	if isPackaged() {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if cur, err := registeredExe(); err == nil && cur != "" {
		if strings.EqualFold(filepath.Clean(cur), filepath.Clean(exe)) {
			return nil // already us
		}
		if _, err := os.Stat(cur); err == nil {
			return nil // another copy (the installed one) owns it
		}
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, urlKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.SetStringValue("", "URL:Null Drive"); err != nil {
		return err
	}
	if err := k.SetStringValue("URL Protocol", ""); err != nil {
		return err
	}
	for sub, val := range map[string]string{`DefaultIcon`: `"` + exe + `",0`, `shell\open\command`: urlCommand(exe)} {
		sk, _, err := registry.CreateKey(k, sub, registry.SET_VALUE)
		if err != nil {
			return err
		}
		err = sk.SetStringValue("", val)
		sk.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// registeredExe returns the program the nulldrive:// handler runs.
func registeredExe() (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, urlKey+`\shell\open\command`, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	cmd, _, err := k.GetStringValue("")
	if err != nil {
		return "", err
	}
	return commandExe(cmd)
}
