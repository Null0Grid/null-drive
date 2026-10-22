//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v2/pkg/options"
	"golang.org/x/sys/windows"
)

// Talking to a running copy without starting Wails, over the channel
// Wails' own single-instance lock uses: a WM_COPYDATA message, tagged 1542,
// carrying the second launch's arguments as JSON, sent to the hidden
// message-only window the first instance created ("wails-app-<id>-sic" /
// "-siw"). The running copy receives them in onSecondInstance.
//
// Skipping Wails matters for the installer helpers (Wails checks for the
// WebView2 runtime first and, if it's missing, shows a download prompt,
// which would hang a silent uninstall) and makes a link open at once.

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	procFindWindowExW            = user32.NewProc("FindWindowExW")
	procSendMessageTimeoutW      = user32.NewProc("SendMessageTimeoutW")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	procAllowSetForegroundWindow = user32.NewProc("AllowSetForegroundWindow")
)

// wailsMutexExists reports whether the named mutex Wails' single-instance lock creates
// ("wails-app-<id>sim") exists, which it does exactly while a copy runs.
func wailsMutexExists() bool {
	name, err := windows.UTF16PtrFromString("wails-app-" + singleInstanceID + "sim")
	if err != nil {
		return false
	}
	h, err := windows.OpenMutex(windows.SYNCHRONIZE, false, name)
	if err != nil {
		return false
	}
	_ = windows.CloseHandle(h)
	return true
}

// runningCopy returns the running copy's message window, or 0 if none is
// running. If the lock says a copy runs but its window isn't found at once
// (it may still be starting, and Wine's lookup occasionally misses), keep
// looking for up to two seconds instead of starting a second copy.
func runningCopy() uintptr {
	if h := findCopyWindow(); h != 0 {
		return h
	}
	if !wailsMutexExists() {
		return 0
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
		if h := findCopyWindow(); h != 0 {
			return h
		}
	}
	return 0
}

// findCopyWindow looks up the running copy's single-instance window, a
// message-only window (FindWindowEx with HWND_MESSAGE searches those).
func findCopyWindow() uintptr {
	id := "wails-app-" + singleInstanceID
	class, _ := windows.UTF16PtrFromString(id + "-sic")
	name, _ := windows.UTF16PtrFromString(id + "-siw")
	// HWND_MESSAGE ((HWND)-3): search message-only windows, which plain
	// FindWindow skips.
	const hwndMessage = ^uintptr(2)
	//nolint:gosec // Win32 call with NUL-terminated UTF-16 strings
	hwnd, _, _ := procFindWindowExW.Call(hwndMessage, 0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(name)))
	return hwnd
}

// sendToRunningCopy hands args to a running copy. sent is false if none
// is running.
func sendToRunningCopy(args []string) (sent bool, err error) {
	hwnd := runningCopy()
	if hwnd == 0 {
		return false, nil
	}
	// This process was started by the user's click (the browser launched
	// it), so it may pass its right to take the foreground to the running
	// copy: then the file that copy opens comes to the front instead of
	// flashing in the taskbar.
	var pid uint32
	procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid))) //nolint:errcheck,gosec // out-parameter
	if pid != 0 {
		procAllowSetForegroundWindow.Call(uintptr(pid)) //nolint:errcheck // best effort
	}
	wd, _ := os.Getwd()
	payload, err := json.Marshal(options.SecondInstanceData{Args: args, WorkingDirectory: wd})
	if err != nil {
		return true, err
	}
	text, err := windows.UTF16FromString(string(payload))
	if err != nil {
		return true, err
	}
	type copyData struct {
		dwData uintptr
		cbData uint32
		lpData uintptr
	}
	cd := copyData{dwData: 1542, cbData: uint32(len(text) * 2), lpData: uintptr(unsafe.Pointer(&text[0]))} //nolint:gosec // bounded by the JSON size
	// With a timeout, so a running copy that hangs can't hang this process
	// (a link launch, or the installer waiting on --quit) as well.
	const (
		wmCopyData       = 0x004A
		smtoAbortIfHung  = 0x0002
		sendTimeoutMilli = 5000
	)
	var result uintptr
	//nolint:gosec // Win32 call: cd and result outlive it
	r, _, callErr := procSendMessageTimeoutW.Call(hwnd, wmCopyData, 0, uintptr(unsafe.Pointer(&cd)), smtoAbortIfHung, sendTimeoutMilli, uintptr(unsafe.Pointer(&result)))
	if r == 0 {
		return true, fmt.Errorf("the running copy of Null Drive didn't respond: %w", callErr)
	}
	return true, nil
}

// signalRunningCopy delivers "--quit" (the installer) to a running copy.
// If none is running there is nothing to do: handled is always true.
func signalRunningCopy(args []string) (handled bool, err error) {
	_, err = sendToRunningCopy(args)
	return true, err
}

// serveOtherCopies is for Flatpak (ipc_linux.go); Windows uses Wails' lock.
func serveOtherCopies(func([]string)) {}
