//go:build windows

package sysinfo

import (
	"syscall"
	"unsafe"
)

var getDiskFreeSpaceEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

// DiskUsage returns capacity for the volume containing path.
func DiskUsage(path string) (Disk, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return Disk{}, err
	}
	var free, total, totalFree uint64
	r, _, callErr := getDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&free)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r == 0 {
		return Disk{}, callErr
	}
	return Disk{TotalBytes: total, FreeBytes: free}, nil
}
