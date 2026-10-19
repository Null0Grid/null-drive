package sync

import (
	"fmt"
	"path"
	"strings"
	"time"
)

// ConflictName returns the name for the local copy kept when both sides
// changed a file: "report (conflict - Laptop - 2026-10-14 2315).pdf".
// taken reports whether a candidate path already exists (locally or on the
// server); a counter is appended until the name is free.
func ConflictName(p, device string, at time.Time, taken func(string) bool) string {
	dir, file := path.Split(p)
	stem, ext := splitExt(file)
	device = sanitizeDevice(device)
	label := fmt.Sprintf("conflict - %s - %s", device, at.Format("2006-01-02 1504"))
	for n := 1; ; n++ {
		suffix := label
		if n > 1 {
			suffix = fmt.Sprintf("%s %d", label, n)
		}
		candidate := dir + stem + " (" + suffix + ")" + ext
		if taken == nil || !taken(candidate) {
			return candidate
		}
	}
}

// IsConflictCopy reports whether a name looks like a conflict copy.
func IsConflictCopy(p string) bool {
	return strings.Contains(path.Base(p), " (conflict - ")
}

func splitExt(name string) (string, string) {
	i := strings.LastIndex(name, ".")
	if i <= 0 {
		return name, ""
	}
	return name[:i], name[i:]
}

// sanitizeDevice keeps the device name safe inside a file name on every OS.
func sanitizeDevice(d string) string {
	var b strings.Builder
	for _, r := range d {
		switch {
		case r < 0x20, strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.TrimSpace(b.String())
	if s == "" {
		s = "device"
	}
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}
