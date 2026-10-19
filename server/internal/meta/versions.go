package meta

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"
)

// ChunkRef is one manifest entry.
type ChunkRef struct {
	Hash string
	Size int64
}

// MissingChunksError: a commit referenced chunks the server does not have
// (or that this user never uploaded and does not already own).
type MissingChunksError struct {
	Hashes []string
}

func (e *MissingChunksError) Error() string {
	return fmt.Sprintf("%d chunk(s) missing", len(e.Hashes))
}

// ErrManifestMismatch: the chunk sizes don't add up to the declared size,
// or a chunk's size differs from the stored one.
var ErrManifestMismatch = errors.New("manifest does not match the declared size")

// Version is one entry of a file's history.
type Version struct {
	ID         int64
	UserID     int64
	Path       string
	Version    int64
	Size       int64
	SHA256     string
	DeviceID   int64 // 0 = web / API session
	DeviceName string
	Origin     string
	CreatedAt  time.Time
	Chunks     int
	Mtime      *time.Time
	SyncedID   string
}

// Origins of a version.
const (
	OriginUpload   = "upload"
	OriginRestore  = "restore"
	OriginMigrated = "migrated"
)

// Commit describes new content for a path.
type Commit struct {
	Path     string
	SHA256   string
	Size     int64
	Chunks   []ChunkRef
	DeviceID int64
	Origin   string
	// Overhead is subtracted from each chunk's stored size when checking
	// Size: end-to-end encrypted chunks are e2ee.ChunkOverhead bytes
	// larger than the plaintext they carry, and Size is the plaintext's.
	Overhead int64
	// Mtime: the uploading device's modification time (optional).
	// SyncedID: the Null Notes note id from Markdown frontmatter.
	Mtime    *time.Time
	SyncedID string
}

// ---- chunks ----

// TouchChunk records that a chunk is stored (inserting it or refreshing
// its GC grace period) and that userID uploaded it, which lets this user
// reference it in a commit. The caller has written the data first.
func (s *Store) TouchChunk(ctx context.Context, userID int64, hash string, size int64) error {
	now := fmtTime(s.now())
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var stored int64
		err := tx.QueryRowContext(ctx,
			`INSERT INTO chunks (hash, size, created_at, touched_at) VALUES (?, ?, ?, ?)
			 ON CONFLICT (hash) DO UPDATE SET touched_at = excluded.touched_at
			 RETURNING size`, hash, size, now, now).Scan(&stored)
		if err != nil {
			return err
		}
		if stored != size {
			// Same SHA-256, different length: impossible short of a hash
			// collision or a corrupted row. Refuse rather than guess.
			return fmt.Errorf("chunk %s: stored size %d, got %d", hash, stored, size)
		}
		if userID == 0 {
			return nil
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO chunk_uploads (user_id, hash, uploaded_at) VALUES (?, ?, ?)
			 ON CONFLICT (user_id, hash) DO UPDATE SET uploaded_at = excluded.uploaded_at`, userID, hash, now)
		return err
	})
}

// ownedChunksSQL selects, from the JSON array bound to the first ?, the
// hashes this user may use: stored, and either uploaded by the user
// (pending commit) or already part of one of the user's versions.
//
// Scoping by user is deliberate. A global "do you have this chunk?" would
// tell anyone whether *some other* user stores a given file (the classic
// dedup side channel: confirm-a-file and learn-the-remaining-bytes
// attacks). Storage is still global: a chunk someone else already stored
// is verified and then not written again.
const ownedChunksSQL = `
SELECT j.value FROM json_each(?) j
  JOIN chunks c ON c.hash = j.value
 WHERE EXISTS (SELECT 1 FROM chunk_uploads u WHERE u.user_id = ? AND u.hash = j.value)
    OR EXISTS (SELECT 1 FROM version_chunks vc JOIN file_versions fv ON fv.id = vc.version_id
                WHERE vc.hash = j.value AND fv.user_id = ?)`

func (s *Store) ownedChunks(ctx context.Context, q querier, userID int64, hashes []string) (map[string]bool, error) {
	arr, err := json.Marshal(hashes)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, ownedChunksSQL, string(arr), userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out[h] = true
	}
	return out, rows.Err()
}

// MissingChunks returns which of hashes the user must upload. Chunks the
// user may already use get their grace period refreshed, so they can't be
// collected between this check and the commit.
func (s *Store) MissingChunks(ctx context.Context, userID int64, hashes []string) ([]string, error) {
	have, err := s.ownedChunks(ctx, s.db, userID, hashes)
	if err != nil {
		return nil, err
	}
	missing := []string{}
	var present []string
	seen := map[string]bool{}
	for _, h := range hashes {
		if seen[h] {
			continue
		}
		seen[h] = true
		if have[h] {
			present = append(present, h)
		} else {
			missing = append(missing, h)
		}
	}
	if len(present) > 0 {
		arr, _ := json.Marshal(present)
		if _, err := s.db.ExecContext(ctx,
			`UPDATE chunks SET touched_at = ? WHERE hash IN (SELECT value FROM json_each(?))`,
			fmtTime(s.now()), string(arr)); err != nil {
			return nil, err
		}
	}
	return missing, nil
}

// CanReadChunk reports whether the user may download a chunk (it is part
// of one of their versions or they uploaded it) and its size.
func (s *Store) CanReadChunk(ctx context.Context, userID int64, hash string) (int64, bool, error) {
	have, err := s.ownedChunks(ctx, s.db, userID, []string{hash})
	if err != nil || !have[hash] {
		return 0, false, err
	}
	var size int64
	err = s.db.QueryRowContext(ctx, `SELECT size FROM chunks WHERE hash = ?`, hash).Scan(&size)
	return size, err == nil, err
}

// ---- commits ----

// CommitFile records new content for c.Path from chunks already stored.
//
// Optimistic concurrency rules (unchanged from Phase 1's PutFile):
//   - Live file with identical content: success, nothing changes. This makes
//     retries idempotent (a client whose response got lost can re-send).
//   - Live file, no If-Match: ErrPreconditionRequired (never blind-overwrite).
//   - Live file, If-Match != current version: *ConflictError.
//   - Missing or deleted file: If-Match may be absent, 0, or the tombstone's
//     version; anything else is a *ConflictError (edited a file someone
//     else deleted).
//   - Live folder at path: ErrPathCollision.
//
// Every chunk must be stored and usable by this user (*MissingChunksError
// lists the ones that aren't), and the sizes must add up.
func (s *Store) CommitFile(ctx context.Context, userID int64, c Commit, pre Precondition) (f File, created bool, err error) {
	var total int64
	hashes := make([]string, len(c.Chunks))
	for i, ch := range c.Chunks {
		if ch.Size <= c.Overhead {
			return File{}, false, ErrManifestMismatch
		}
		total += ch.Size - c.Overhead
		hashes[i] = ch.Hash
	}
	if total != c.Size {
		return File{}, false, ErrManifestMismatch
	}
	if c.Origin == "" {
		c.Origin = OriginUpload
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		row, err := getFile(ctx, tx, userID, c.Path)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		var cur *File
		if err == nil {
			cur = &row
		}
		noop, err := CheckPut(cur, c.SHA256, c.Size, pre)
		if err != nil {
			return err
		}
		if noop {
			// Same content. A backup that touched the file still wants its
			// new mtime recorded (else it would re-upload it every run):
			// metadata only, no new version and no change-feed entry.
			if c.Mtime != nil && (row.Mtime == nil || !row.Mtime.Equal(*c.Mtime)) {
				if f, err = scanFile(tx.QueryRowContext(ctx,
					`UPDATE files SET mtime = ? WHERE id = ? RETURNING `+fileCols, fmtTime(*c.Mtime), row.ID)); err != nil {
					return err
				}
				return nil
			}
			f = row
			return nil
		}
		if len(hashes) > 0 {
			have, err := s.ownedChunks(ctx, tx, userID, hashes)
			if err != nil {
				return err
			}
			var missing []string
			for _, h := range hashes {
				if !have[h] {
					missing = append(missing, h)
				}
			}
			if len(missing) > 0 {
				return &MissingChunksError{Hashes: dedupe(missing)}
			}
			if err := checkChunkSizes(ctx, tx, c.Chunks); err != nil {
				return err
			}
		}
		if cur == nil || !cur.Live() {
			if err := checkAncestors(ctx, tx, userID, c.Path, nil); err != nil {
				return err
			}
			if err := checkNoChildren(ctx, tx, userID, c.Path, nil); err != nil {
				return err
			}
			created = true
		}
		v, err := bump(ctx, tx)
		if err != nil {
			return err
		}
		now := s.now()
		if err := ensureParents(ctx, tx, userID, c.Path, v, now); err != nil {
			return err
		}
		vid, err := insertVersion(ctx, tx, userID, c, v, now)
		if err != nil {
			return err
		}
		mode := uidKeep
		if c.Origin == OriginRestore {
			mode = uidRevive // a restored file is the same file again
		}
		f, err = upsert(ctx, tx, File{
			UserID: userID, Path: c.Path, Size: c.Size, SHA256: c.SHA256,
			Version: v, ModifiedAt: now, VersionID: vid, Mtime: c.Mtime, SyncedID: c.SyncedID,
		}, mode)
		if err != nil {
			return err
		}
		// Committed: these uploads are now owned through the version.
		if len(hashes) > 0 {
			arr, _ := json.Marshal(hashes)
			_, err = tx.ExecContext(ctx,
				`DELETE FROM chunk_uploads WHERE user_id = ? AND hash IN (SELECT value FROM json_each(?))`,
				userID, string(arr))
		}
		return err
	})
	return f, created, err
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, h := range in {
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

func checkChunkSizes(ctx context.Context, tx *sql.Tx, chunks []ChunkRef) error {
	type pair struct {
		H string `json:"h"`
		S int64  `json:"s"`
	}
	ps := make([]pair, len(chunks))
	for i, c := range chunks {
		ps[i] = pair{c.Hash, c.Size}
	}
	arr, _ := json.Marshal(ps)
	var bad int
	err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM json_each(?) j JOIN chunks c ON c.hash = json_extract(j.value, '$.h')
		 WHERE c.size <> json_extract(j.value, '$.s')`, string(arr)).Scan(&bad)
	if err != nil {
		return err
	}
	if bad > 0 {
		return ErrManifestMismatch
	}
	return nil
}

func insertVersion(ctx context.Context, tx *sql.Tx, userID int64, c Commit, version int64, now time.Time) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx,
		`INSERT INTO file_versions (user_id, path, version, size, sha256, device_id, origin, created_at, mtime, synced_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		userID, c.Path, version, c.Size, c.SHA256, nullID(c.DeviceID), c.Origin, fmtTime(now),
		nullTime(c.Mtime), nullStr(c.SyncedID)).Scan(&id)
	if err != nil {
		return 0, err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO version_chunks (version_id, seq, hash, size) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	for i, ch := range c.Chunks {
		if _, err := stmt.ExecContext(ctx, id, i, ch.Hash, ch.Size); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// Manifest returns a version's ordered chunk list.
func (s *Store) Manifest(ctx context.Context, versionID int64) ([]ChunkRef, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT hash, size FROM version_chunks WHERE version_id = ? ORDER BY seq`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChunkRef{}
	for rows.Next() {
		var c ChunkRef
		if err := rows.Scan(&c.Hash, &c.Size); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---- history ----

const versionCols = `fv.id, fv.user_id, fv.path, fv.version, fv.size, fv.sha256, COALESCE(fv.device_id, 0),
	COALESCE(d.name, ''), fv.origin, fv.created_at,
	(SELECT COUNT(*) FROM version_chunks vc WHERE vc.version_id = fv.id), fv.mtime, COALESCE(fv.synced_id, '')`

const versionFrom = ` FROM file_versions fv LEFT JOIN devices d ON d.id = fv.device_id `

func scanVersion(row interface{ Scan(...any) error }) (Version, error) {
	var v Version
	var created string
	var mtime sql.NullString
	if err := row.Scan(&v.ID, &v.UserID, &v.Path, &v.Version, &v.Size, &v.SHA256, &v.DeviceID,
		&v.DeviceName, &v.Origin, &created, &v.Chunks, &mtime, &v.SyncedID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Version{}, ErrNotFound
		}
		return Version{}, err
	}
	v.CreatedAt = parseTime(created)
	v.Mtime = parseNullTime(mtime)
	return v, nil
}

// ListVersions returns a path's history, newest first.
func (s *Store) ListVersions(ctx context.Context, userID int64, path string) ([]Version, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+versionCols+versionFrom+
		`WHERE fv.user_id = ? AND fv.path = ? ORDER BY fv.version DESC, fv.id DESC`, userID, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Version{}
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetVersion returns one version of a path by its version number.
func (s *Store) GetVersion(ctx context.Context, userID int64, path string, version int64) (Version, error) {
	return scanVersion(s.db.QueryRowContext(ctx, `SELECT `+versionCols+versionFrom+
		`WHERE fv.user_id = ? AND fv.path = ? AND fv.version = ?`, userID, path, version))
}

// RestoreVersion makes an old version current again by committing its
// manifest as a new version (origin "restore"). History is never
// rewritten: the versions in between stay restorable too. Restoring into
// a deleted path recreates the file and any missing parent folders.
func (s *Store) RestoreVersion(ctx context.Context, userID int64, path string, version int64, pre Precondition, deviceID int64) (File, error) {
	old, err := s.GetVersion(ctx, userID, path, version)
	if err != nil {
		return File{}, err
	}
	chunks, err := s.Manifest(ctx, old.ID)
	if err != nil {
		return File{}, err
	}
	cur, err := s.GetFile(ctx, userID, path)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return File{}, err
	}
	if !pre.Set && err == nil && cur.Live() {
		// The user chose a version in the UI: overwrite the current one,
		// but only if it is still the one they were looking at.
		pre = Precondition{Set: true, Version: cur.Version}
	}
	if err == nil && cur.Live() && cur.SHA256 == old.SHA256 && cur.Size == old.Size {
		// Already identical: nothing to do (CommitFile would no-op too).
		return cur, nil
	}
	f, _, err := s.CommitFile(ctx, userID, Commit{
		Path: path, SHA256: old.SHA256, Size: old.Size, Chunks: chunks, DeviceID: deviceID, Origin: OriginRestore,
		Mtime: old.Mtime, SyncedID: old.SyncedID,
	}, pre)
	return f, err
}

// TrashItem is a deleted file that can still be restored.
type TrashItem struct {
	Path      string
	Size      int64
	DeletedAt time.Time
	PurgeAt   time.Time // when it leaves the trash for good
	Version   int64     // the version a restore brings back
	Versions  int       // how many versions remain in its history
}

// Trash lists the user's deleted files whose content is still retained,
// most recently deleted first. Folders are not listed: restoring a file
// recreates its folders.
func (s *Store) Trash(ctx context.Context, userID int64) ([]TrashItem, error) {
	r, err := s.Retention(ctx)
	if err != nil {
		return nil, err
	}
	// Expired items (deleted before the cutoff) are hidden even before the
	// next prune removes them.
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.path, fv.size, f.deleted_at, fv.version,
		       (SELECT COUNT(*) FROM file_versions x WHERE x.user_id = f.user_id AND x.path = f.path)
		  FROM files f JOIN file_versions fv ON fv.id = f.version_id
		 WHERE f.user_id = ? AND f.deleted_at IS NOT NULL AND f.deleted_at >= ? AND f.is_dir = 0 AND fv.path = f.path
		 ORDER BY f.deleted_at DESC, f.path`, userID, fmtTime(r.TrashCutoff(s.now())))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TrashItem{}
	for rows.Next() {
		var it TrashItem
		var del string
		if err := rows.Scan(&it.Path, &it.Size, &del, &it.Version, &it.Versions); err != nil {
			return nil, err
		}
		it.DeletedAt = parseTime(del)
		it.PurgeAt = it.DeletedAt.Add(time.Duration(r.TrashDays) * 24 * time.Hour)
		out = append(out, it)
	}
	return out, rows.Err()
}

// ---- stats ----

// Stats summarizes storage for one user and for the whole server.
type Stats struct {
	Files         int64 // live files
	LogicalBytes  int64 // sum of live file sizes
	HistoryBytes  int64 // sum of all retained versions (what a naive store would hold)
	Versions      int64
	UniqueBytes   int64 // distinct chunks referenced by this user's versions
	ServerStored  int64 // all chunks on disk (every user)
	ServerLogical int64 // all retained versions of every user
	ServerChunks  int64
	UnreferencedB int64 // chunks awaiting garbage collection
	TrashFiles    int64
}

// Stats computes the numbers for the storage dashboard.
func (s *Store) Stats(ctx context.Context, userID int64) (Stats, error) {
	var st Stats
	q := []struct {
		sql  string
		args []any
		dst  []any
	}{
		{`SELECT COUNT(*), COALESCE(SUM(size), 0) FROM files WHERE user_id = ? AND deleted_at IS NULL AND is_dir = 0`,
			[]any{userID}, []any{&st.Files, &st.LogicalBytes}},
		{`SELECT COUNT(*), COALESCE(SUM(size), 0) FROM file_versions WHERE user_id = ?`,
			[]any{userID}, []any{&st.Versions, &st.HistoryBytes}},
		{`SELECT COALESCE(SUM(size), 0) FROM chunks WHERE hash IN (
		    SELECT vc.hash FROM version_chunks vc JOIN file_versions fv ON fv.id = vc.version_id WHERE fv.user_id = ?)`,
			[]any{userID}, []any{&st.UniqueBytes}},
		{`SELECT COUNT(*), COALESCE(SUM(size), 0), COALESCE(SUM(CASE WHEN refcount = 0 THEN size END), 0) FROM chunks`,
			nil, []any{&st.ServerChunks, &st.ServerStored, &st.UnreferencedB}},
		{`SELECT COALESCE(SUM(size), 0) FROM file_versions`, nil, []any{&st.ServerLogical}},
		{`SELECT COUNT(*) FROM files f JOIN file_versions fv ON fv.id = f.version_id
		   WHERE f.user_id = ? AND f.deleted_at IS NOT NULL AND f.is_dir = 0 AND fv.path = f.path`,
			[]any{userID}, []any{&st.TrashFiles}},
	}
	for _, x := range q {
		if err := s.db.QueryRowContext(ctx, x.sql, x.args...).Scan(x.dst...); err != nil {
			return st, err
		}
	}
	return st, nil
}

// ---- retention ----

// Retention is the version-history policy: keep every version for
// FullDays, then the newest version of each (UTC) day for DailyDays more.
// The current version of a live file is always kept.
//
// Deleting is never immediate: a deleted file goes to Recently deleted
// (the trash) and stays restorable, history included, for TrashDays. After
// that everything of it is purged; only the tombstone row stays, so sync
// clients still learn about the delete from the change feed.
type Retention struct {
	FullDays  int
	DailyDays int
	TrashDays int
}

// DefaultTrashDays: how long Recently deleted keeps files.
const DefaultTrashDays = 30

// TrashCutoff: files deleted before this are purged.
func (r Retention) TrashCutoff(now time.Time) time.Time {
	return now.Add(-time.Duration(r.TrashDays) * 24 * time.Hour)
}

// Window is how long anything is kept at all.
func (r Retention) Window() time.Duration {
	return time.Duration(r.FullDays+r.DailyDays) * 24 * time.Hour
}

// Retention reads the policy from settings (defaults 30/90).
func (s *Store) Retention(ctx context.Context) (Retention, error) {
	r := Retention{FullDays: 30, DailyDays: 90, TrashDays: DefaultTrashDays}
	for key, dst := range map[string]*int{"retention_full_days": &r.FullDays, "retention_daily_days": &r.DailyDays, "trash_days": &r.TrashDays} {
		v, err := s.Setting(ctx, key)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return r, err
		}
		if _, err := fmt.Sscan(v, dst); err != nil {
			return r, fmt.Errorf("setting %s=%q: %w", key, v, err)
		}
	}
	return r, nil
}

// SetRetention stores the policy.
func (s *Store) SetRetention(ctx context.Context, r Retention) error {
	if r.FullDays < 0 || r.DailyDays < 0 || r.FullDays > 3650 || r.DailyDays > 3650 {
		return fmt.Errorf("retention days must be between 0 and 3650")
	}
	if r.TrashDays < 1 || r.TrashDays > 3650 {
		return fmt.Errorf("trash days must be between 1 and 3650")
	}
	if err := s.SetSetting(ctx, "retention_full_days", fmt.Sprint(r.FullDays)); err != nil {
		return err
	}
	if err := s.SetSetting(ctx, "trash_days", fmt.Sprint(r.TrashDays)); err != nil {
		return err
	}
	return s.SetSetting(ctx, "retention_daily_days", fmt.Sprint(r.DailyDays))
}

type pruneRow struct {
	id      int64
	user    int64
	path    string
	created time.Time
}

// prunable decides, for one path's versions sorted newest first, which
// ones the policy drops. protected versions are always kept.
func prunable(vs []pruneRow, protected map[int64]bool, r Retention, now time.Time) []int64 {
	full := now.Add(-time.Duration(r.FullDays) * 24 * time.Hour)
	window := now.Add(-r.Window())
	var drop []int64
	seenDay := map[string]bool{}
	for _, v := range vs {
		day := v.created.UTC().Format("2006-01-02")
		switch {
		case protected[v.id]:
			seenDay[day] = true
		case !v.created.Before(full):
			// Recent: always kept. Does not claim its day, so the newest
			// version of a day that straddles the boundary is still kept.
		case !v.created.Before(window) && !seenDay[day]:
			seenDay[day] = true // newest of its day
		default:
			drop = append(drop, v.id)
		}
	}
	return drop
}

// PruneVersions applies the retention policy to every user's history and
// returns how many versions it removed. Freed chunks reach refcount 0 and
// are collected by the GC after its grace period.
func (s *Store) PruneVersions(ctx context.Context, r Retention) (int, error) {
	now := s.now()
	trashCutoff := fmtTime(r.TrashCutoff(now))
	protected := map[int64]bool{}
	// Live files' current versions, and deleted files' last versions while
	// they're in the trash.
	rows, err := s.db.QueryContext(ctx,
		`SELECT version_id FROM files WHERE version_id IS NOT NULL AND (deleted_at IS NULL OR deleted_at >= ?)`, trashCutoff)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		protected[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// Only versions older than FullDays can be dropped, so only those
	// paths need looking at; the per-path pass still needs every version
	// of such a path to know which is newest per day.
	rows, err = s.db.QueryContext(ctx, `
		SELECT id, user_id, path, created_at FROM file_versions
		 WHERE (user_id, path) IN (SELECT user_id, path FROM file_versions WHERE created_at < ?)
		 ORDER BY user_id, path, created_at DESC, id DESC`,
		fmtTime(now.Add(-time.Duration(r.FullDays)*24*time.Hour)))
	if err != nil {
		return 0, err
	}
	var drop []int64
	var group []pruneRow
	flush := func() {
		drop = append(drop, prunable(group, protected, r, now)...)
		group = group[:0]
	}
	for rows.Next() {
		var pr pruneRow
		var created string
		if err := rows.Scan(&pr.id, &pr.user, &pr.path, &created); err != nil {
			rows.Close()
			return 0, err
		}
		pr.created = parseTime(created)
		if len(group) > 0 && (group[0].user != pr.user || group[0].path != pr.path) {
			flush()
		}
		group = append(group, pr)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	flush()

	// Files out of the trash: purge their whole history (every version of
	// a path whose row is a tombstone deleted before the cutoff).
	rows, err = s.db.QueryContext(ctx, `
		SELECT fv.id FROM file_versions fv
		  JOIN files f ON f.user_id = fv.user_id AND f.path = fv.path
		 WHERE f.deleted_at IS NOT NULL AND f.deleted_at < ?`, trashCutoff)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		drop = append(drop, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	sort.Slice(drop, func(i, j int) bool { return drop[i] < drop[j] })
	drop = slices.Compact(drop)
	removed := len(drop)

	// Delete in batches so one huge prune doesn't hold the write lock.
	for len(drop) > 0 {
		n := min(len(drop), 500)
		batch := drop[:n]
		drop = drop[n:]
		arr, _ := json.Marshal(batch)
		err := s.withTx(ctx, func(tx *sql.Tx) error {
			// Tombstones past the bin window lose their restore pointer.
			if _, err := tx.ExecContext(ctx,
				`UPDATE files SET version_id = NULL WHERE version_id IN (SELECT value FROM json_each(?))`, string(arr)); err != nil {
				return err
			}
			// Manifest rows first: their delete trigger decrements refcounts.
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM version_chunks WHERE version_id IN (SELECT value FROM json_each(?))`, string(arr)); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx,
				`DELETE FROM file_versions WHERE id IN (SELECT value FROM json_each(?))`, string(arr))
			return err
		})
		if err != nil {
			return 0, err
		}
	}
	return removed, nil
}

// ---- garbage collection support ----

// CollectableChunks lists chunks with no references whose grace period
// (since their last upload/check) has passed.
func (s *Store) CollectableChunks(ctx context.Context, grace time.Duration, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT hash FROM chunks WHERE refcount <= 0 AND touched_at < ? LIMIT ?`,
		fmtTime(s.now().Add(-grace)), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// DeleteChunkIfUnused removes a chunk row if it is still unreferenced and
// past its grace period, re-checking both inside the transaction. The GC
// deletes the file only if this returns true (and holds the chunk's lock
// across both steps, see chunkstore).
func (s *Store) DeleteChunkIfUnused(ctx context.Context, hash string, grace time.Duration) (bool, int64, error) {
	var size int64
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx,
			`DELETE FROM chunks WHERE hash = ? AND refcount <= 0 AND touched_at < ? RETURNING size`,
			hash, fmtTime(s.now().Add(-grace))).Scan(&size)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM chunk_uploads WHERE hash = ?`, hash)
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return false, 0, nil
	}
	return err == nil, size, err
}

// PruneChunkUploads forgets uncommitted uploads older than the grace
// period (their chunks then become collectable).
func (s *Store) PruneChunkUploads(ctx context.Context, grace time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM chunk_uploads WHERE uploaded_at < ?`, fmtTime(s.now().Add(-grace)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// KnownChunk reports whether a chunk row exists (for the orphan sweep).
func (s *Store) KnownChunk(ctx context.Context, hash string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM chunks WHERE hash = ?`, hash).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
