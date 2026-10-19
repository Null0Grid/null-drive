package meta

import (
	"context"
	"strconv"
)

// PutFile is the pre-Phase 6 single-call API, kept for the existing
// precondition tests: it stores the content as one whole-file chunk (as
// migrated data is) and commits it. The fake chunk key includes the size
// because those tests reuse one fake hash with different sizes, which
// real chunks never do.
func (s *Store) PutFile(ctx context.Context, userID int64, path, sha string, size int64, pre Precondition) (File, bool, error) {
	var chunks []ChunkRef
	if size > 0 {
		key := sha + ":" + strconv.FormatInt(size, 10)
		if err := s.TouchChunk(ctx, userID, key, size); err != nil {
			return File{}, false, err
		}
		chunks = []ChunkRef{{Hash: key, Size: size}}
	}
	return s.CommitFile(ctx, userID, Commit{Path: path, SHA256: sha, Size: size, Chunks: chunks}, pre)
}
