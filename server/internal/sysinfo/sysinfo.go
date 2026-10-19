// Package sysinfo reports facts about the host the server runs on.
package sysinfo

// Disk is the capacity of the file system holding a directory.
type Disk struct {
	TotalBytes uint64
	FreeBytes  uint64 // available to this (unprivileged) process
}
