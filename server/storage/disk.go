package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Disk stores blobs under <root>/blobs/ab/cd/<sha256>.
//
// The two levels of 2-hex-char fan-out (65,536 leaf dirs) keep any single
// directory small even with millions of blobs, which matters for ext4/NTFS
// lookup speed and for tools like ls/rsync.
//
// Writes go to <root>/tmp first and are renamed into place only after the
// content is fully written, fsynced and hash-verified. rename(2) within one
// file system is atomic, so a crash can leave a stray temp file but never a
// truncated blob under a valid hash.
type Disk struct {
	blobDir string
	tmpDir  string
}

// NewDisk prepares the directory layout under root.
func NewDisk(root string) (*Disk, error) {
	d := &Disk{
		blobDir: filepath.Join(root, "blobs"),
		tmpDir:  filepath.Join(root, "tmp"),
	}
	for _, dir := range []string{d.blobDir, d.tmpDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	// Leftover temp files are from interrupted uploads; they were never
	// referenced, so they are safe to remove at startup.
	if entries, err := os.ReadDir(d.tmpDir); err == nil {
		for _, e := range entries {
			_ = os.Remove(filepath.Join(d.tmpDir, e.Name()))
		}
	}
	return d, nil
}

func (d *Disk) path(sum string) string {
	return filepath.Join(d.blobDir, sum[0:2], sum[2:4], sum)
}

func (d *Disk) Put(ctx context.Context, r io.Reader, expected string) (Blob, error) {
	expected = strings.ToLower(expected)
	if expected != "" && !ValidHash(expected) {
		return Blob{}, ErrInvalidHash
	}
	tmp, err := os.CreateTemp(d.tmpDir, "upload-*")
	if err != nil {
		return Blob{}, err
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), ctxReader{ctx, r})
	if err != nil {
		return Blob{}, fmt.Errorf("write blob: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return Blob{}, err
	}
	if err := tmp.Close(); err != nil {
		return Blob{}, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if expected != "" && sum != expected {
		return Blob{}, ErrHashMismatch
	}

	final := d.path(sum)
	if _, err := os.Stat(final); err == nil {
		// Already stored: content addressing makes this a free dedup.
		return Blob{SHA256: sum, Size: n}, nil
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o750); err != nil {
		return Blob{}, err
	}
	if err := os.Rename(tmpName, final); err != nil {
		// A concurrent upload of the same content may have won the race
		// (on Windows rename does not replace an existing file).
		if _, statErr := os.Stat(final); statErr == nil {
			return Blob{SHA256: sum, Size: n}, nil
		}
		return Blob{}, fmt.Errorf("commit blob: %w", err)
	}
	keep = true
	syncDir(filepath.Dir(final))
	return Blob{SHA256: sum, Size: n}, nil
}

func (d *Disk) Open(sum string) (io.ReadSeekCloser, int64, error) {
	if !ValidHash(sum) {
		return nil, 0, ErrInvalidHash
	}
	f, err := os.Open(d.path(sum))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, st.Size(), nil
}

func (d *Disk) Exists(sum string) (bool, error) {
	if !ValidHash(sum) {
		return false, ErrInvalidHash
	}
	_, err := os.Stat(d.path(sum))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func (d *Disk) Delete(sum string) error {
	if !ValidHash(sum) {
		return ErrInvalidHash
	}
	err := os.Remove(d.path(sum))
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return err
}

// syncDir fsyncs a directory so the rename itself survives power loss.
// Best effort: Windows does not support fsync on directories.
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	if f, err := os.Open(dir); err == nil { //nolint:gosec // dir is derived from a validated hash
		_ = f.Sync()
		_ = f.Close()
	}
}

// ctxReader aborts long uploads when the request context is cancelled
// (client disconnected, server shutting down).
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// Walk visits every blob file. Names that aren't valid hashes (stray
// files) are skipped.
func (d *Disk) Walk(fn func(sum string, size int64, modTime time.Time) error) error {
	return filepath.WalkDir(d.blobDir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !ValidHash(e.Name()) {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return nil //nolint:nilerr // vanished between listing and stat: skip
		}
		return fn(e.Name(), info.Size(), info.ModTime())
	})
}
