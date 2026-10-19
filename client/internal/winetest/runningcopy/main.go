//go:build windows

// A stand-in for a running Null Drive: it creates the mutex and the
// message-only window Wails' single-instance lock creates, prints
// "listening", then prints the second instance's WM_COPYDATA payload
// ("GOT {...}"), decoded the same way Wails decodes it, and exits.
//
//nolint:errcheck,gosec,unused // a test tool mirroring raw Win32 structs and calls
package main

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type wndclassex struct {
	Size, Style                        uint32
	WndProc                            uintptr
	ClsExtra, WndExtra                 int32
	Instance, Icon, Cursor, Background windows.Handle
	MenuName, ClassName                *uint16
	IconSm                             windows.Handle
}
type copydata struct {
	dwData uintptr
	cbData uint32
	lpData uintptr
}
type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      [2]int32
}

// A window's messages go to the thread that created it, so keep main on
// one OS thread (as Wails does for its window thread).
func init() { runtime.LockOSThread() }

func main() {
	u := windows.NewLazySystemDLL("user32.dll")
	reg, create, get, disp, def := u.NewProc("RegisterClassExW"), u.NewProc("CreateWindowExW"), u.NewProc("GetMessageW"), u.NewProc("DispatchMessageW"), u.NewProc("DefWindowProcW")
	id := "wails-app-io.nulldrive.client"
	// Like Wails: the single-instance mutex first, then the window.
	if _, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr(id+"sim")); err != nil {
		fmt.Println("mutex:", err)
	}
	cls, _ := windows.UTF16PtrFromString(id + "-sic")
	name, _ := windows.UTF16PtrFromString(id + "-siw")
	proc := syscall.NewCallback(func(h uintptr, m uint32, w, l uintptr) uintptr {
		if m == 0x004A {
			// l and lpData are pointers Windows passes as integers; read
			// them through a pointer to the integer (keeps go vet happy).
			cd := *(**copydata)(unsafe.Pointer(&l))
			if cd.dwData == 1542 {
				fmt.Println("GOT", windows.UTF16PtrToString(*(**uint16)(unsafe.Pointer(&cd.lpData))))
				// Reply first (as Wails does: it queues the data and returns),
				// then leave the message loop.
				u.NewProc("PostQuitMessage").Call(0)
				return 0
			}
		}
		r, _, _ := def.Call(h, uintptr(m), w, l)
		return r
	})
	wc := wndclassex{WndProc: proc, ClassName: cls}
	wc.Size = uint32(unsafe.Sizeof(wc))
	reg.Call(uintptr(unsafe.Pointer(&wc)))
	h, _, err := create.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(name)), 0, 0, 0, 0, 0, ^uintptr(2), 0, 0, 0)
	if h == 0 {
		fmt.Println("create failed", err)
		os.Exit(1)
	}
	fmt.Println("listening")
	var m msg
	for {
		r, _, _ := get.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if r == 0 {
			return
		}
		disp.Call(uintptr(unsafe.Pointer(&m)))
	}
}
