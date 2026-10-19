package sync

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestIgnore(t *testing.T) {
	ig := NewIgnore(ParseIgnore(strings.NewReader(`
# build output
build/
*.log
!important.log
docs/drafts/*.md
`)))
	tests := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"report.docx", false, false},
		{"~$report.docx", false, true},
		{"Docs/~$draft.docx", false, true},
		{"photo.jpg", false, false},
		{"THUMBS.DB", false, true}, // case-insensitive
		{"a/b/.DS_Store", false, true},
		{"cache.tmp", false, true},
		{"x.nulldrive-part", false, true},
		{"build", true, true},
		{"build/out.bin", false, true}, // inside an ignored folder
		{"src/build/x.o", false, true}, // folder rule matches at any depth
		{"build", false, false},        // dir-only rule, but this is a file
		{"debug.log", false, true},
		{"logs/debug.log", false, true},
		{"important.log", false, false},      // negated
		{"docs/drafts/idea.md", false, true}, // anchored
		{"other/drafts/idea.md", false, false},
		{".nullignore", false, false}, // the rules file itself syncs
	}
	for _, tt := range tests {
		if got := ig.Match(tt.path, tt.isDir); got != tt.want {
			t.Errorf("Match(%q, dir=%v) = %v, want %v", tt.path, tt.isDir, got, tt.want)
		}
	}
}

func TestConflictName(t *testing.T) {
	at := time.Date(2026, 10, 14, 23, 15, 0, 0, time.UTC)
	tests := []struct{ path, device, want string }{
		{"report.pdf", "Laptop", "report (conflict - Laptop - 2026-10-14 2315).pdf"},
		{"Docs/a.tar.gz", "PC", "Docs/a.tar (conflict - PC - 2026-10-14 2315).gz"},
		{"Makefile", "PC", "Makefile (conflict - PC - 2026-10-14 2315)"},
		{".env", "PC", ".env (conflict - PC - 2026-10-14 2315)"},
		{"x.txt", `Bad:Name/\?`, "x (conflict - Bad-Name--- - 2026-10-14 2315).txt"},
		{"x.txt", "   ", "x (conflict - device - 2026-10-14 2315).txt"},
	}
	for _, tt := range tests {
		got := ConflictName(tt.path, tt.device, at, nil)
		if got != tt.want {
			t.Errorf("ConflictName(%q, %q) = %q, want %q", tt.path, tt.device, got, tt.want)
		}
		if !IsConflictCopy(got) {
			t.Errorf("IsConflictCopy(%q) = false", got)
		}
	}
	taken := map[string]bool{"r (conflict - PC - 2026-10-14 2315).txt": true}
	if got := ConflictName("r.txt", "PC", at, func(p string) bool { return taken[p] }); got != "r (conflict - PC - 2026-10-14 2315 2).txt" {
		t.Fatalf("collision: %q", got)
	}
}

func TestLimiter(t *testing.T) {
	var slept time.Duration
	now := time.Unix(0, 0)
	l := NewLimiter(1000) // 1000 B/s
	l.now = func() time.Time { return now }
	l.sleep = func(_ context.Context, d time.Duration) error { slept += d; now = now.Add(d); return nil }

	data := bytes.Repeat([]byte("x"), 5000)
	n, err := io.Copy(io.Discard, l.Reader(context.Background(), bytes.NewReader(data)))
	if err != nil || n != 5000 {
		t.Fatal(n, err)
	}
	// 1 s burst allowance, then 4000 bytes at 1000 B/s ≈ 4 s.
	if slept < 3900*time.Millisecond || slept > 4100*time.Millisecond {
		t.Fatalf("slept %v, want ~4s", slept)
	}

	var nilL *Limiter
	if r := nilL.Reader(context.Background(), bytes.NewReader(data)); r == nil {
		t.Fatal("nil limiter must pass through")
	}
	unlimited := NewLimiter(0)
	unlimited.sleep = func(context.Context, time.Duration) error { t.Fatal("unlimited limiter slept"); return nil }
	_, _ = io.Copy(io.Discard, unlimited.Reader(context.Background(), bytes.NewReader(data)))
}
