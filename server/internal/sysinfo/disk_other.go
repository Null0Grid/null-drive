//go:build !linux && !darwin && !freebsd && !windows

package sysinfo

import "errors"

// DiskUsage is not implemented on this platform.
func DiskUsage(string) (Disk, error) {
	return Disk{}, errors.New("disk usage not supported on this OS")
}
