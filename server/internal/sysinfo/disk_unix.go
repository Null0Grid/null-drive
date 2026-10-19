//go:build linux || darwin || freebsd

package sysinfo

import "syscall"

// DiskUsage returns capacity for the file system containing path.
func DiskUsage(path string) (Disk, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Disk{}, err
	}
	bsize := uint64(st.Bsize) //nolint:gosec,unconvert // Bsize is positive; its type differs per OS
	return Disk{
		TotalBytes: uint64(st.Blocks) * bsize, //nolint:unconvert // field type differs per OS
		FreeBytes:  uint64(st.Bavail) * bsize, //nolint:gosec,unconvert // field type differs per OS
	}, nil
}
