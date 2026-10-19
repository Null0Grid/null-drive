package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Null0Grid/null-drive/shared/api"
	"github.com/Null0Grid/null-drive/shared/chunker"
)

// Chunked transfers (API v2): only chunks the server lacks are uploaded,
// and only chunks the local disk lacks are downloaded. Both directions run
// up to Parallel chunk requests at once and resume after interruption:
// uploaded chunks are remembered by the server (for its GC grace period),
// downloaded ones by a small state file next to the partial download.

// TransferOptions tunes chunked transfers.
type TransferOptions struct {
	// Parallel chunk requests (default 4). More helps on high-latency
	// links; four already saturates a LAN with 2 MiB chunks.
	Parallel int
	// Wrap paces request/response bodies (bandwidth limit). May be nil.
	Wrap func(ctx context.Context, r io.Reader) io.Reader
	// Progress reports bytes transferred over the network for this file.
	Progress func(done, total int64)
	// Params must match the server's chunking for dedup to work.
	Params chunker.Params
	// Codec, if set, encrypts chunks end to end (an *e2ee.Keys). The
	// server then stores sealed chunks keyed by the SHA-256 of the sealed
	// bytes, and the file's sha256 is Codec.FileHash of the plaintext's.
	Codec Codec
}

// Codec transforms file content on its way to and from the server.
// Sealing must be deterministic, or unchanged chunks would get new IDs on
// every upload and delta sync would stop working.
type Codec interface {
	SealChunk(plain []byte) []byte
	OpenChunk(blob []byte) ([]byte, error)
	FileHash(sum []byte) string
	Overhead() int
}

func (o *TransferOptions) defaults() {
	if o.Parallel <= 0 {
		o.Parallel = 4
	}
	if o.Params == (chunker.Params{}) {
		o.Params = chunker.Default
	}
	if o.Wrap == nil {
		o.Wrap = func(_ context.Context, r io.Reader) io.Reader { return r }
	}
}

// TransferStats describes what a chunked transfer actually moved.
type TransferStats struct {
	Chunks       int   // in the file
	Transferred  int   // over the network
	BytesSent    int64 // payload bytes over the network
	BytesReused  int64 // satisfied from data already present
	Size         int64
	ResumedBytes int64 // download: from a previous partial attempt
}

var (
	// ErrV2Unsupported means the server predates API v2 (Phase 5 or
	// older); callers fall back to the v1 whole-file transfer.
	ErrV2Unsupported = errors.New("server does not support chunked transfers (API v2)")
	// ErrDownloadChanged means the file changed on the server while it was
	// being downloaded.
	ErrDownloadChanged = errors.New("file changed on the server during download")
)

// SupportsV2 probes the server once (an empty chunk check) and caches the
// answer for 10 minutes, so a server upgraded while the client runs is
// picked up without a restart.
func (c *Client) SupportsV2(ctx context.Context) bool {
	c.v2mu.Lock()
	defer c.v2mu.Unlock()
	if !c.v2checked.IsZero() && time.Since(c.v2checked) < 10*time.Minute {
		return c.v2ok
	}
	var out api.ChunkCheckResponse
	err := c.v2call(ctx, http.MethodPost, "/chunks/check", api.ChunkCheckRequest{Hashes: []string{}}, &out)
	switch {
	case err == nil:
		c.v2ok = true
	case IsStatus(err, http.StatusNotFound) || IsStatus(err, http.StatusMethodNotAllowed):
		c.v2ok = false
	default:
		return false // network trouble: don't cache, decide next time
	}
	c.v2checked = time.Now()
	return c.v2ok
}

func (c *Client) v2request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/api/v2"+path, body)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("User-Agent", c.UserAgent)
	if c.E2EE {
		req.Header.Set(api.HeaderE2EE, "1")
	}
	return req, nil
}

func (c *Client) v2call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := c.v2request(ctx, method, path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.do(req, out)
}

// FileByID resolves a file's stable id (search results, nulldrive://
// links) to its current server path. Deleted files come back with Deleted
// set; an unknown id (or another user's) is a 404 *Error (IsStatus(err, 404)).
func (c *Client) FileByID(ctx context.Context, id string) (api.File, error) {
	var f api.File
	return f, c.v2call(ctx, http.MethodGet, "/ids/"+url.PathEscape(id), nil, &f)
}

// Manifest fetches the current manifest of a file.
func (c *Client) Manifest(ctx context.Context, path string) (api.Manifest, error) {
	var m api.Manifest
	return m, c.v2call(ctx, http.MethodGet, "/files/"+EncodePath(path)+"/manifest", nil, &m)
}

// MissingChunks asks which hashes must be uploaded (batched).
func (c *Client) MissingChunks(ctx context.Context, hashes []string) ([]string, error) {
	var missing []string
	for len(hashes) > 0 {
		n := min(len(hashes), 10000)
		var out api.ChunkCheckResponse
		if err := c.v2call(ctx, http.MethodPost, "/chunks/check", api.ChunkCheckRequest{Hashes: hashes[:n]}, &out); err != nil {
			return nil, err
		}
		missing = append(missing, out.Missing...)
		hashes = hashes[n:]
	}
	return missing, nil
}

// PutChunk uploads one chunk.
func (c *Client) PutChunk(ctx context.Context, hash string, data []byte, wrap func(context.Context, io.Reader) io.Reader) error {
	req, err := c.v2request(ctx, http.MethodPut, "/chunks/"+hash, wrap(ctx, bytes.NewReader(data)))
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(data))
	req.Header.Set("Content-Type", "application/octet-stream")
	return c.do(req, nil)
}

// GetChunk downloads one chunk and verifies its hash.
func (c *Client) GetChunk(ctx context.Context, ref api.ChunkRef, wrap func(context.Context, io.Reader) io.Reader) ([]byte, error) {
	req, err := c.v2request(ctx, http.MethodGet, "/chunks/"+ref.Hash, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, readError(res)
	}
	buf := make([]byte, 0, ref.Size)
	b := bytes.NewBuffer(buf)
	if _, err := io.Copy(b, io.LimitReader(wrap(ctx, res.Body), ref.Size+1)); err != nil {
		return nil, err
	}
	data := b.Bytes()
	sum := sha256.Sum256(data)
	if int64(len(data)) != ref.Size || hex.EncodeToString(sum[:]) != ref.Hash {
		return nil, fmt.Errorf("chunk %s: corrupt or truncated (%d bytes)", ref.Hash, len(data))
	}
	return data, nil
}

// Commit makes a manifest the new content of path.
func (c *Client) Commit(ctx context.Context, path string, m api.CommitRequest, ifMatchV *int64) (api.File, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return api.File{}, err
	}
	req, err := c.v2request(ctx, http.MethodPut, "/files/"+EncodePath(path), bytes.NewReader(b))
	if err != nil {
		return api.File{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	ifMatch(req, ifMatchV)
	var out api.File
	return out, c.do(req, &out)
}

// piece is one chunk of a local file: its server ID and stored size
// (sealed, if a Codec is used) and where its plaintext is in the file.
type piece struct {
	id     string
	stored int64
	off    int64
	n      int64
}

// storedID returns the chunk's server ID and stored bytes.
func storedID(plain []byte, codec Codec) (string, []byte) {
	if codec != nil {
		plain = codec.SealChunk(plain)
	}
	sum := sha256.Sum256(plain)
	return hex.EncodeToString(sum[:]), plain
}

// chunkFile chunks an open file, returning its pieces and the hash the
// server records for the whole file.
func chunkFile(f io.ReaderAt, size int64, p chunker.Params, codec Codec) ([]piece, string, error) {
	var out []piece
	refs, whole, n, err := chunker.Manifest(io.NewSectionReader(f, 0, size), p, func(ch chunker.Chunk) error {
		pc := piece{id: ch.HexHash(), stored: int64(len(ch.Data)), off: ch.Offset, n: int64(len(ch.Data))}
		if codec != nil {
			var blob []byte
			pc.id, blob = storedID(ch.Data, codec)
			pc.stored = int64(len(blob))
		}
		out = append(out, pc)
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	if n != size || len(refs) != len(out) {
		return nil, "", errors.New("file changed while reading")
	}
	if codec != nil {
		raw, _ := hex.DecodeString(whole)
		whole = codec.FileHash(raw)
	}
	return out, whole, nil
}

// readPiece reads a piece's plaintext and returns the bytes to send
// (sealed with a Codec), checking that the file still has that content.
func readPiece(f io.ReaderAt, pc piece, codec Codec) ([]byte, error) {
	buf := make([]byte, pc.n)
	if _, err := f.ReadAt(buf, pc.off); err != nil {
		return nil, err
	}
	id, blob := storedID(buf, codec)
	if id != pc.id {
		return nil, errors.New("file changed while uploading")
	}
	return blob, nil
}

// parallel runs fn for each index with at most n in flight; the first
// error cancels the rest.
func parallel(ctx context.Context, n, count int, fn func(ctx context.Context, i int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, n)
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	for i := 0; i < count; i++ {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := fn(ctx, i); err != nil {
				once.Do(func() { first = err; cancel() })
			}
		}(i)
	}
	wg.Wait()
	if first != nil {
		return first
	}
	return ctx.Err()
}

// withRetry retries transient failures of one chunk request (network
// errors, 5xx, 429) a few times with backoff, so one dropped connection
// doesn't fail a 5 GB transfer.
func withRetry(ctx context.Context, fn func() error) error {
	delay := 500 * time.Millisecond
	for attempt := 0; ; attempt++ {
		err := fn()
		if err == nil || attempt == 3 || !IsNetwork(err) || ctx.Err() != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
}

// UploadFile uploads the file at local as path using chunks: chunk it
// locally, ask the server which chunks it lacks, upload those in
// parallel, then commit the manifest with If-Match. Returns
// ErrV2Unsupported for old servers.
func (c *Client) UploadFile(ctx context.Context, path, local string, ifMatchV *int64, o TransferOptions) (api.File, TransferStats, error) {
	o.defaults()
	var st TransferStats
	if !c.SupportsV2(ctx) {
		return api.File{}, st, ErrV2Unsupported
	}
	f, err := os.Open(local) //nolint:gosec // the sync engine passes paths inside the sync root
	if err != nil {
		return api.File{}, st, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return api.File{}, st, err
	}
	pieces, whole, err := chunkFile(f, info.Size(), o.Params, o.Codec)
	if err != nil {
		return api.File{}, st, err
	}
	st.Size, st.Chunks = info.Size(), len(pieces)
	manifest := api.CommitRequest{Size: info.Size(), SHA256: whole, Chunks: make([]api.ChunkRef, len(pieces))}
	byHash := map[string]piece{}
	hashes := make([]string, 0, len(pieces))
	for i, pc := range pieces {
		manifest.Chunks[i] = api.ChunkRef{Hash: pc.id, Size: pc.stored}
		if _, dup := byHash[pc.id]; !dup {
			byHash[pc.id] = pc
			hashes = append(hashes, pc.id)
		}
	}

	send := func(missing []string) error {
		var total int64
		for _, h := range missing {
			total += byHash[h].stored
		}
		var done atomic.Int64
		err := parallel(ctx, o.Parallel, len(missing), func(ctx context.Context, i int) error {
			pc := byHash[missing[i]]
			buf, err := readPiece(f, pc, o.Codec)
			if err != nil {
				return err
			}
			if err := withRetry(ctx, func() error { return c.PutChunk(ctx, pc.id, buf, o.Wrap) }); err != nil {
				return err
			}
			n := done.Add(pc.stored)
			if o.Progress != nil {
				o.Progress(n, total)
			}
			return nil
		})
		st.Transferred += len(missing)
		st.BytesSent += total
		return err
	}

	missing, err := c.MissingChunks(ctx, hashes)
	if err != nil {
		return api.File{}, st, err
	}
	if err := send(missing); err != nil {
		return api.File{}, st, err
	}
	st.BytesReused = max(0, st.Size-st.BytesSent)
	for attempt := 0; ; attempt++ {
		file, err := c.Commit(ctx, path, manifest, ifMatchV)
		var ae *Error
		if attempt == 0 && errors.As(err, &ae) && ae.Code == "missing_chunks" {
			// Rare: a chunk the check reported present was collected
			// before the commit. Upload what the server names and retry.
			missing, err2 := c.MissingChunks(ctx, hashes)
			if err2 != nil {
				return api.File{}, st, err2
			}
			if err := send(missing); err != nil {
				return api.File{}, st, err
			}
			continue
		}
		return file, st, err
	}
}

// partState is the resume record of a chunked download, stored next to
// the partial file as "<name>.nulldrive-part.state".
type partState struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Done   []bool `json:"done"`
}

// PartStateSuffix is appended to the partial file's name for its state.
const PartStateSuffix = ".state"

func loadPartState(p string, m api.Manifest) *partState {
	b, err := os.ReadFile(p) //nolint:gosec // derived from the target path
	if err != nil {
		return nil
	}
	var s partState
	if json.Unmarshal(b, &s) != nil || s.SHA256 != m.SHA256 || s.Size != m.Size || len(s.Done) != len(m.Chunks) {
		return nil
	}
	return &s
}

func savePartState(p string, s *partState) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// DownloadFile writes the current content of path into part (a temporary
// file next to the final location), reusing chunks found in the existing
// local file at reuse (may be "") and in an earlier partial download of
// the same version. Only missing chunks are fetched. The finished part is
// verified against the manifest's whole-file hash; the caller renames it
// into place. Returns the manifest that was written.
func (c *Client) DownloadFile(ctx context.Context, path, part, reuse string, o TransferOptions) (api.Manifest, TransferStats, error) {
	o.defaults()
	var st TransferStats
	if !c.SupportsV2(ctx) {
		return api.Manifest{}, st, ErrV2Unsupported
	}
	m, err := c.Manifest(ctx, path)
	if err != nil {
		return m, st, err
	}
	st.Size, st.Chunks = m.Size, len(m.Chunks)
	statePath := part + PartStateSuffix

	// What do we already have?  1) a previous attempt at this exact
	// version (chunks marked done in the state file);  2) the old local
	// copy of the file, re-chunked: an edit elsewhere in a large file
	// leaves most of its chunks identical.
	state := loadPartState(statePath, m)
	out, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o644) //nolint:gosec // inside the sync root
	if err != nil {
		return m, st, err
	}
	defer out.Close()
	if state == nil {
		state = &partState{SHA256: m.SHA256, Size: m.Size, Done: make([]bool, len(m.Chunks))}
		if err := out.Truncate(0); err != nil {
			return m, st, err
		}
	}
	if err := out.Truncate(m.Size); err != nil {
		return m, st, err
	}
	local := map[string]piece{}
	var src *os.File
	if reuse != "" {
		if f, err := os.Open(reuse); err == nil { //nolint:gosec // inside the sync root
			if info, err := f.Stat(); err == nil {
				if pieces, _, err := chunkFile(f, info.Size(), o.Params, o.Codec); err == nil {
					for _, pc := range pieces {
						local[pc.id] = pc
					}
					src = f
				}
			}
			if src == nil {
				_ = f.Close()
			}
		}
	}
	if src != nil {
		defer src.Close()
	}

	// Plaintext layout: with a Codec each stored chunk is Overhead bytes
	// larger than the data it carries.
	overhead := int64(0)
	if o.Codec != nil {
		overhead = int64(o.Codec.Overhead())
	}
	offsets := make([]int64, len(m.Chunks))
	lens := make([]int64, len(m.Chunks))
	var off int64
	for i, ch := range m.Chunks {
		offsets[i], lens[i] = off, ch.Size-overhead
		if lens[i] < 0 {
			return m, st, fmt.Errorf("chunk %s: smaller than the encryption overhead", ch.Hash)
		}
		off += lens[i]
	}
	if off != m.Size {
		return m, st, fmt.Errorf("manifest chunks add up to %d bytes, file is %d", off, m.Size)
	}
	var todo []int
	for i, ch := range m.Chunks {
		switch {
		case state.Done[i]:
			st.ResumedBytes += lens[i]
		case src != nil && local[ch.Hash].id != "" && local[ch.Hash].n == lens[i]:
			l := local[ch.Hash]
			buf := make([]byte, l.n)
			if _, err := src.ReadAt(buf, l.off); err == nil {
				if id, _ := storedID(buf, o.Codec); id == ch.Hash {
					if _, err := out.WriteAt(buf, offsets[i]); err != nil {
						return m, st, err
					}
					state.Done[i] = true
					st.BytesReused += lens[i]
					continue
				}
			}
			todo = append(todo, i)
		default:
			todo = append(todo, i)
		}
	}
	var total int64
	for _, i := range todo {
		total += m.Chunks[i].Size
	}
	var mu sync.Mutex
	var done atomic.Int64
	lastSave := time.Now()
	err = parallel(ctx, o.Parallel, len(todo), func(ctx context.Context, k int) error {
		i := todo[k]
		var data []byte
		err := withRetry(ctx, func() error {
			var err error
			data, err = c.GetChunk(ctx, m.Chunks[i], o.Wrap)
			return err
		})
		if err != nil {
			if IsCode(err, "not_found") {
				return ErrDownloadChanged // pruned/replaced since we read the manifest
			}
			return err
		}
		n := int64(len(data))
		if o.Codec != nil {
			if data, err = o.Codec.OpenChunk(data); err != nil {
				return fmt.Errorf("chunk %s: %w", m.Chunks[i].Hash, err)
			}
		}
		if int64(len(data)) != lens[i] {
			return fmt.Errorf("chunk %s: unexpected length", m.Chunks[i].Hash)
		}
		if _, err := out.WriteAt(data, offsets[i]); err != nil {
			return err
		}
		mu.Lock()
		state.Done[i] = true
		// Persist progress every few seconds, so an interrupted multi-GB
		// download resumes where it stopped.
		if time.Since(lastSave) > 2*time.Second {
			lastSave = time.Now()
			_ = out.Sync()
			_ = savePartState(statePath, state)
		}
		mu.Unlock()
		sent := done.Add(n)
		if o.Progress != nil {
			o.Progress(sent, total)
		}
		return nil
	})
	st.Transferred = len(todo)
	st.BytesSent = done.Load()
	if err != nil {
		_ = out.Sync()
		_ = savePartState(statePath, state)
		return m, st, err
	}
	if err := out.Sync(); err != nil {
		return m, st, err
	}
	// End-to-end check of the assembled file.
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(out, 0, m.Size)); err != nil {
		return m, st, err
	}
	_ = os.Remove(statePath)
	got := hex.EncodeToString(h.Sum(nil))
	if o.Codec != nil {
		got = o.Codec.FileHash(h.Sum(nil))
	}
	if got != m.SHA256 {
		_ = os.Remove(part)
		return m, st, ErrDownloadChanged
	}
	return m, st, nil
}
