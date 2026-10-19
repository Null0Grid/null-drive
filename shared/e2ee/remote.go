package e2ee

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/Null0Grid/null-drive/shared/api"
	"github.com/Null0Grid/null-drive/shared/client"
)

// ErrWholeFile: encrypted accounts only use chunked (API v2) transfers. A
// v1 whole-file upload would hand the server plaintext, and a v1 download
// would return concatenated ciphertext.
var ErrWholeFile = errors.New("e2ee: encrypted accounts need chunked transfers (API v2)")

// Remote presents an encrypted account as a plaintext one to the sync
// engine: it encrypts paths on the way out, decrypts them on the way in,
// and seals/opens chunks through the transfer codec. It implements the
// engine's RemoteAPI and ChunkedRemote, so the engine itself (three-way
// merge, conflicts, renames) is unchanged. Pair it with
// Engine.ContentHash = keys.FileHash.
type Remote struct {
	c    *client.Client
	keys *Keys

	// OnUndecryptable is called for server entries this key cannot
	// decrypt (a plaintext leftover, or data written with another key).
	// They are hidden from the engine rather than synced as garbage.
	OnUndecryptable func(path string, err error)

	mu    sync.Mutex
	cache map[string]string // plaintext path -> encrypted path
}

// NewRemote wraps c (which is marked as an E2EE-capable client).
func NewRemote(c *client.Client, keys *Keys) *Remote {
	c.E2EE = true
	return &Remote{c: c, keys: keys, cache: map[string]string{}}
}

// Keys returns the account keys.
func (r *Remote) Keys() *Keys { return r.keys }

func (r *Remote) enc(p string) (string, error) {
	r.mu.Lock()
	e, ok := r.cache[p]
	r.mu.Unlock()
	if ok {
		return e, nil
	}
	e, err := r.keys.EncryptPath(p)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	if len(r.cache) > 100000 {
		r.cache = map[string]string{}
	}
	r.cache[p] = e
	r.mu.Unlock()
	return e, nil
}

func (r *Remote) dec(f *api.File) bool {
	if !IsEncryptedPath(f.Path) {
		if r.OnUndecryptable != nil {
			r.OnUndecryptable(f.Path, ErrName)
		}
		return false
	}
	p, err := r.keys.DecryptPath(f.Path)
	if err != nil {
		if r.OnUndecryptable != nil {
			r.OnUndecryptable(f.Path, err)
		}
		return false
	}
	f.Path = p
	return true
}

// ListFiles returns the decrypted listing or change feed.
func (r *Remote) ListFiles(ctx context.Context, since *int64) (api.FileList, error) {
	l, err := r.c.ListFiles(ctx, since)
	if err != nil {
		return l, err
	}
	out := l.Files[:0]
	for _, f := range l.Files {
		if r.dec(&f) {
			out = append(out, f)
		}
	}
	l.Files = out
	return l, nil
}

// Download is not available for encrypted accounts (see ErrWholeFile).
func (r *Remote) Download(context.Context, string) (io.ReadCloser, string, error) {
	return nil, "", ErrWholeFile
}

// Upload is not available for encrypted accounts (see ErrWholeFile).
func (r *Remote) Upload(context.Context, string, io.Reader, int64, string, *int64) (api.File, error) {
	return api.File{}, ErrWholeFile
}

func (r *Remote) decFile(f api.File, err error) (api.File, error) {
	if err != nil {
		return f, err
	}
	if !r.dec(&f) {
		return f, ErrName
	}
	return f, nil
}

// Delete deletes a path (and, for a folder, everything in it).
func (r *Remote) Delete(ctx context.Context, path string, ifMatch *int64) (api.DeleteResponse, error) {
	e, err := r.enc(path)
	if err != nil {
		return api.DeleteResponse{}, err
	}
	res, err := r.c.Delete(ctx, e, ifMatch)
	if err != nil {
		return res, err
	}
	out := res.Deleted[:0]
	for _, f := range res.Deleted {
		if r.dec(&f) {
			out = append(out, f)
		}
	}
	res.Deleted = out
	return res, nil
}

// Move renames a path. Names are encrypted independently of their folder,
// so moving a folder doesn't re-encrypt anything inside it.
func (r *Remote) Move(ctx context.Context, from, to string, ifMatch *int64) (api.MoveResponse, error) {
	ef, err := r.enc(from)
	if err != nil {
		return api.MoveResponse{}, err
	}
	et, err := r.enc(to)
	if err != nil {
		return api.MoveResponse{}, err
	}
	res, err := r.c.Move(ctx, ef, et, ifMatch)
	if err != nil {
		return res, err
	}
	out := res.Moved[:0]
	for _, m := range res.Moved {
		f := api.File{Path: m.From}
		if !r.dec(&f) || !r.dec(&m.File) {
			continue
		}
		m.From = f.Path
		out = append(out, m)
	}
	res.Moved = out
	return res, nil
}

// CreateFolder creates a folder.
func (r *Remote) CreateFolder(ctx context.Context, path string) (api.File, error) {
	e, err := r.enc(path)
	if err != nil {
		return api.File{}, err
	}
	return r.decFile(r.c.CreateFolder(ctx, e))
}

func (r *Remote) opts(o client.TransferOptions) client.TransferOptions {
	o.Codec = r.keys
	return o
}

// UploadFile encrypts and uploads a local file.
func (r *Remote) UploadFile(ctx context.Context, path, local string, ifMatch *int64, o client.TransferOptions) (api.File, client.TransferStats, error) {
	e, err := r.enc(path)
	if err != nil {
		return api.File{}, client.TransferStats{}, err
	}
	f, st, err := r.c.UploadFile(ctx, e, local, ifMatch, r.opts(o))
	if errors.Is(err, client.ErrV2Unsupported) {
		err = ErrWholeFile // never fall back to plaintext
	}
	f, err = r.decFile(f, err)
	return f, st, err
}

// DownloadFile downloads and decrypts a file into part.
func (r *Remote) DownloadFile(ctx context.Context, path, part, reuse string, o client.TransferOptions) (api.Manifest, client.TransferStats, error) {
	e, err := r.enc(path)
	if err != nil {
		return api.Manifest{}, client.TransferStats{}, err
	}
	m, st, err := r.c.DownloadFile(ctx, e, part, reuse, r.opts(o))
	if errors.Is(err, client.ErrV2Unsupported) {
		err = ErrWholeFile
	}
	if err == nil {
		m.Path = path
	}
	return m, st, err
}
