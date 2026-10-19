package chunkstore

import (
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/nulldrive/nulldrive/server/internal/meta"
	"github.com/nulldrive/nulldrive/server/storage"
)

// Reader presents a manifest as one seekable stream, so v1 downloads (and
// http.ServeContent's Range support) work unchanged on chunked files.
// Chunk files are opened lazily, one at a time.
type Reader struct {
	blobs   storage.Store
	chunks  []meta.ChunkRef
	offsets []int64 // offsets[i] = start of chunk i; offsets[len] = size
	pos     int64
	cur     io.ReadSeekCloser
	curIdx  int
}

// Open returns a reader over a manifest.
func (s *Store) Open(chunks []meta.ChunkRef) *Reader {
	offs := make([]int64, len(chunks)+1)
	for i, c := range chunks {
		offs[i+1] = offs[i] + c.Size
	}
	return &Reader{blobs: s.Blobs, chunks: chunks, offsets: offs, curIdx: -1}
}

// Size is the total length.
func (r *Reader) Size() int64 { return r.offsets[len(r.chunks)] }

func (r *Reader) Read(p []byte) (int, error) {
	if r.pos >= r.Size() {
		return 0, io.EOF
	}
	i := sort.Search(len(r.chunks), func(i int) bool { return r.offsets[i+1] > r.pos })
	if i != r.curIdx {
		if r.cur != nil {
			_ = r.cur.Close()
			r.cur = nil
		}
		f, size, err := r.blobs.Open(r.chunks[i].Hash)
		if err != nil {
			return 0, fmt.Errorf("chunk %d (%s): %w", i, r.chunks[i].Hash, err)
		}
		if size != r.chunks[i].Size {
			_ = f.Close()
			return 0, fmt.Errorf("chunk %s: size %d on disk, %d in manifest", r.chunks[i].Hash, size, r.chunks[i].Size)
		}
		r.cur, r.curIdx = f, i
	}
	within := r.pos - r.offsets[i]
	if _, err := r.cur.Seek(within, io.SeekStart); err != nil {
		return 0, err
	}
	max := r.offsets[i+1] - r.pos
	if int64(len(p)) > max {
		p = p[:max]
	}
	n, err := r.cur.Read(p)
	r.pos += int64(n)
	if errors.Is(err, io.EOF) && n > 0 {
		err = nil
	}
	if errors.Is(err, io.EOF) {
		return n, io.ErrUnexpectedEOF // the chunk file is shorter than recorded
	}
	return n, err
}

// Seek implements io.Seeker.
func (r *Reader) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.pos + offset
	case io.SeekEnd:
		abs = r.Size() + offset
	default:
		return 0, errors.New("chunkstore: invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("chunkstore: negative position")
	}
	r.pos = abs
	return abs, nil
}

// Close releases the open chunk file.
func (r *Reader) Close() error {
	if r.cur != nil {
		err := r.cur.Close()
		r.cur = nil
		return err
	}
	return nil
}
