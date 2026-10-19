package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/nulldrive/nulldrive/shared/client"

	"github.com/nulldrive/nulldrive/shared/pathutil"
)

// ErrRootMissing means the sync folder doesn't exist (an unplugged drive, a
// deleted folder). Sync stops instead of concluding "everything was
// deleted" and wiping the server.
var ErrRootMissing = errors.New("sync folder is missing")

// Skipped is a local item that can't be synced, shown to the user.
type Skipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// ScanResult is the local side of a sync cycle.
type ScanResult struct {
	Items   map[string]*Local
	Skipped []Skipped
	Ignore  *Ignore
}

// Scan walks root. Files whose size and mtime match the base entry reuse the
// recorded hash, so a steady-state scan reads metadata only; changed files
// are hashed.
func Scan(root string, bases map[string]*Entry) (*ScanResult, error) {
	return ScanWith(root, bases, nil)
}

// ScanWith is Scan with a content hash mapping (nil = hex SHA-256). End-to-
// end encrypted accounts store a keyed hash of each file's SHA-256 on the
// server (e2ee.Keys.FileHash); scanning in that same space keeps the
// three-way merge comparing like with like.
func ScanWith(root string, bases map[string]*Entry, mapHash func(sum []byte) string) (*ScanResult, error) {
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return nil, fmt.Errorf("%w: %s", ErrRootMissing, root)
	}
	ig := NewIgnore(nil)
	if f, err := os.Open(filepath.Join(root, IgnoreFile)); err == nil {
		ig = NewIgnore(ParseIgnore(f))
		_ = f.Close()
	}

	res := &ScanResult{Items: map[string]*Local{}, Ignore: ig}
	err = filepath.WalkDir(root, func(full string, d fs.DirEntry, err error) error {
		if full == root {
			return err
		}
		rel, rerr := filepath.Rel(root, full)
		if rerr != nil {
			return rerr
		}
		p := filepath.ToSlash(rel)
		if err != nil {
			res.Skipped = append(res.Skipped, Skipped{p, err.Error()})
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		isDir := d.IsDir()
		if !isDir && strings.Contains(path.Base(p), partSuffix) {
			cleanPart(full, d)
			return nil
		}
		if ig.Match(p, isDir) {
			if isDir {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			res.Skipped = append(res.Skipped, Skipped{p, "symbolic links are not synced"})
			return nil
		}
		if !isDir && !d.Type().IsRegular() {
			return nil // sockets, devices, pipes
		}
		if clean, cerr := pathutil.Clean(p); cerr != nil || clean != p {
			reason := "name is not allowed on every platform"
			if cerr != nil {
				reason = cerr.Error()
			}
			res.Skipped = append(res.Skipped, Skipped{p, reason})
			if isDir {
				return fs.SkipDir
			}
			return nil
		}
		if isDir {
			res.Items[p] = &Local{Path: p, IsDir: true}
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			res.Skipped = append(res.Skipped, Skipped{p, ierr.Error()})
			return nil
		}
		l := &Local{Path: p, Size: info.Size(), MtimeNs: info.ModTime().UnixNano()}
		if b := bases[p]; b != nil && !b.IsDir && b.Size == l.Size && b.MtimeNs == l.MtimeNs {
			l.SHA256 = b.SHA256
		} else {
			sum, herr := hashFile(full, mapHash)
			if herr != nil {
				// Typically locked by another program (Office on Windows).
				// Skip it this cycle; it is retried on the next one.
				res.Skipped = append(res.Skipped, Skipped{p, "could not read: " + herr.Error()})
				return nil
			}
			l.SHA256 = sum
		}
		res.Items[p] = l
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func hashFile(path string, mapHash func(sum []byte) string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	if mapHash != nil {
		return mapHash(h.Sum(nil)), nil
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// cleanPart handles leftovers of interrupted downloads. A partial file
// with a resume state (chunked download) is kept for a week so the next
// attempt continues where it stopped; anything else is removed.
func cleanPart(full string, d fs.DirEntry) {
	switch {
	case strings.HasSuffix(full, partSuffix):
		st, err := os.Stat(full + client.PartStateSuffix)
		if err == nil && time.Since(st.ModTime()) < 7*24*time.Hour {
			return
		}
		_ = os.Remove(full)
		_ = os.Remove(full + client.PartStateSuffix)
	case strings.HasSuffix(full, partSuffix+client.PartStateSuffix):
		if _, err := os.Stat(strings.TrimSuffix(full, client.PartStateSuffix)); err != nil {
			_ = os.Remove(full) // state without data
		}
	default:
		if info, err := d.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
			_ = os.Remove(full) // e.g. a .state.tmp from a crash
		}
	}
}
