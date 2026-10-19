package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func sumOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func newDisk(t *testing.T) (*Disk, string) {
	t.Helper()
	root := t.TempDir()
	d, err := NewDisk(root)
	if err != nil {
		t.Fatal(err)
	}
	return d, root
}

func TestPutAndOpen(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name     string
		content  string
		expected string
		wantErr  error
	}{
		{"no expected hash", "hello world", "", nil},
		{"matching expected hash", "hello world", sumOf("hello world"), nil},
		{"uppercase expected hash ok", "abc", strings.ToUpper(sumOf("abc")), nil},
		{"empty file", "", "", nil},
		{"mismatched hash", "hello", sumOf("goodbye"), ErrHashMismatch},
		{"malformed hash", "hello", "not-a-hash", ErrInvalidHash},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, root := newDisk(t)
			b, err := d.Put(ctx, strings.NewReader(tt.content), tt.expected)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				assertTmpEmpty(t, root)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if b.SHA256 != sumOf(tt.content) || b.Size != int64(len(tt.content)) {
				t.Fatalf("blob = %+v", b)
			}
			// Content-addressed layout: blobs/ab/cd/<hash>
			want := filepath.Join(root, "blobs", b.SHA256[:2], b.SHA256[2:4], b.SHA256)
			if _, err := os.Stat(want); err != nil {
				t.Fatalf("blob not at %s: %v", want, err)
			}
			rc, size, err := d.Open(b.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			got, _ := io.ReadAll(rc)
			if string(got) != tt.content || size != int64(len(tt.content)) {
				t.Fatalf("read back %q (%d bytes)", got, size)
			}
			assertTmpEmpty(t, root)
		})
	}
}

func TestDedup(t *testing.T) {
	d, root := newDisk(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := d.Put(ctx, strings.NewReader("same bytes"), ""); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	_ = filepath.WalkDir(filepath.Join(root, "blobs"), func(_ string, e os.DirEntry, _ error) error {
		if e != nil && !e.IsDir() {
			count++
		}
		return nil
	})
	if count != 1 {
		t.Fatalf("stored %d blobs, want 1", count)
	}
}

func TestConcurrentPutsOfSameContent(t *testing.T) {
	d, _ := newDisk(t)
	content := bytes.Repeat([]byte("x"), 1<<20)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := d.Put(context.Background(), bytes.NewReader(content), "")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	h := sha256.Sum256(content)
	ok, err := d.Exists(hex.EncodeToString(h[:]))
	if err != nil || !ok {
		t.Fatalf("exists = %v, %v", ok, err)
	}
}

func TestOpenExistsDelete(t *testing.T) {
	d, _ := newDisk(t)
	missing := sumOf("never stored")
	if _, _, err := d.Open(missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("open missing: %v", err)
	}
	if ok, err := d.Exists(missing); ok || err != nil {
		t.Fatalf("exists missing: %v %v", ok, err)
	}
	for _, bad := range []string{"../../etc/passwd", "ABC", ""} {
		if _, _, err := d.Open(bad); !errors.Is(err, ErrInvalidHash) {
			t.Errorf("Open(%q) err = %v, want ErrInvalidHash", bad, err)
		}
	}
	b, _ := d.Put(context.Background(), strings.NewReader("bye"), "")
	if err := d.Delete(b.SHA256); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(b.SHA256); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestCancelledContextStoresNothing(t *testing.T) {
	d, root := newDisk(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Put(ctx, strings.NewReader("data"), ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	assertTmpEmpty(t, root)
}

func TestStartupCleansTmp(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tmp"), 0o750); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(root, "tmp", "upload-123")
	if err := os.WriteFile(stray, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDisk(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatalf("stray temp file survived: %v", err)
	}
}

func assertTmpEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("tmp dir not empty: %d entries", len(entries))
	}
}
