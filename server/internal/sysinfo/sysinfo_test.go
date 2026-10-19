package sysinfo

import "testing"

func TestDiskUsage(t *testing.T) {
	d, err := DiskUsage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if d.TotalBytes == 0 || d.FreeBytes > d.TotalBytes {
		t.Fatalf("implausible disk usage %+v", d)
	}
}
