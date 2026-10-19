package meta

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Null0Grid/null-drive/shared/pathutil"
)

var (
	// ErrPreconditionRequired: overwriting an existing file needs If-Match.
	ErrPreconditionRequired = errors.New("overwriting an existing file requires If-Match")
	// ErrTargetExists: a move would overwrite a live file or folder.
	ErrTargetExists = errors.New("destination already exists")
	// ErrPathCollision: a path would be both a file and a folder.
	ErrPathCollision = errors.New("path collides with an existing file or folder")
	// ErrInvalidMove: e.g. moving a folder into itself.
	ErrInvalidMove = errors.New("invalid move")
)

// ConflictError is returned when an If-Match precondition fails. Current is
// the server's row for the path (nil if it never existed).
type ConflictError struct {
	Current *File
}

func (e *ConflictError) Error() string {
	if e.Current == nil {
		return "version conflict: file does not exist"
	}
	return fmt.Sprintf("version conflict: current version is %d", e.Current.Version)
}

// File is one row of the files table: a file or (IsDir) a folder.
// DeletedAt != nil means tombstone.
//
// Folder invariant: every live entry's ancestor folders exist as live
// folder rows. CommitFile, CreateFolder and MoveFile create missing parents.
type File struct {
	ID         int64
	UserID     int64
	Path       string
	IsDir      bool
	Size       int64
	SHA256     string
	Version    int64
	ModifiedAt time.Time
	DeletedAt  *time.Time
	// VersionID is the file_versions row holding the content (0 for
	// folders). Tombstones keep it so the file can be restored.
	VersionID int64
	// UID is the file's stable id: unlike ID it survives renames, moves
	// and restores (see migration 0006). Search results and
	// nulldrive://open links use it.
	UID string
	// Mtime is the modification time the uploading device reported (nil
	// if it sent none); SyncedID the Null Notes note id from a Markdown
	// file's frontmatter (migration 0007).
	Mtime    *time.Time
	SyncedID string
	// MovedTo is set on the tombstone a move leaves at the old path: the
	// file's new path (migration 0008). Empty for real deletes.
	MovedTo string
}

func (f File) Live() bool { return f.DeletedAt == nil }

// Precondition is a parsed If-Match header. Version 0 means "I expect this
// path not to exist" (create-only). Any (If-Match: *) means "whatever is
// there": create or overwrite without knowing the current version, for
// tools that mirror a folder (backups); history still keeps the old one.
type Precondition struct {
	Set     bool
	Any     bool
	Version int64
}

// mismatch reports whether a precondition rules out the current version v.
func (p Precondition) mismatch(v int64) bool { return p.Set && !p.Any && p.Version != v }

const fileCols = `id, user_id, path, is_dir, size, sha256, version, modified_at, deleted_at, version_id, COALESCE(uid, ''), mtime, COALESCE(synced_id, ''), COALESCE(moved_to, '')`

func scanFile(row interface{ Scan(...any) error }) (File, error) {
	var f File
	var mod string
	var del sql.NullString
	var vid sql.NullInt64
	var mtime sql.NullString
	if err := row.Scan(&f.ID, &f.UserID, &f.Path, &f.IsDir, &f.Size, &f.SHA256, &f.Version, &mod, &del, &vid, &f.UID, &mtime, &f.SyncedID, &f.MovedTo); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return File{}, ErrNotFound
		}
		return File{}, err
	}
	f.ModifiedAt = parseTime(mod)
	f.DeletedAt = parseNullTime(del)
	f.VersionID = vid.Int64
	f.Mtime = parseNullTime(mtime)
	return f, nil
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return fmtTime(*t)
}

func collect(rows *sql.Rows, err error) ([]File, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []File{}
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// folderRange returns [lo, hi) bounds matching every path inside folder p.
// '0' is the byte after '/', so "p/" <= path < "p0" selects exactly the
// paths starting with "p/". Unlike LIKE, this needs no wildcard escaping
// (paths may contain % and _) and can use the (user_id, path) index.
func folderRange(p string) (lo, hi string) { return p + "/", p + "0" }

// bump advances the global change counter and returns the new value. It
// must run inside the write transaction that applies the change, so the
// counter and the rows it stamps commit atomically.
func bump(ctx context.Context, tx *sql.Tx) (int64, error) {
	var v int64
	err := tx.QueryRowContext(ctx,
		`UPDATE change_counter SET value = value + 1 WHERE id = 1 RETURNING value`).Scan(&v)
	return v, err
}

// CurrentVersion returns the global change counter.
func (s *Store) CurrentVersion(ctx context.Context) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx, `SELECT value FROM change_counter WHERE id = 1`).Scan(&v)
	return v, err
}

// ListFiles returns the user's files and folders and the server version the
// client should remember.
//
//   - since == nil: a plain listing of live entries.
//   - since != nil: the change feed, i.e. every row (tombstones included)
//     with version > *since, oldest change first.
//
// The counter is read BEFORE the rows. Counter bumps commit together with
// the rows they stamp, so every change <= the returned version is already
// visible to the row query. Reading in the other order could return a
// version newer than the rows, and the client would skip that change forever.
func (s *Store) ListFiles(ctx context.Context, userID int64, since *int64) ([]File, int64, error) {
	current, err := s.CurrentVersion(ctx)
	if err != nil {
		return nil, 0, err
	}
	var files []File
	if since == nil {
		files, err = collect(s.db.QueryContext(ctx,
			`SELECT `+fileCols+` FROM files WHERE user_id = ? AND deleted_at IS NULL ORDER BY path`, userID))
	} else {
		files, err = collect(s.db.QueryContext(ctx,
			`SELECT `+fileCols+` FROM files WHERE user_id = ? AND version > ? ORDER BY version, path`,
			userID, *since))
	}
	return files, current, err
}

// ListFilesUnder is ListFiles limited to prefix and everything below it
// (a folder subtree; "" = everything). A backup tool lists only its own
// folder this way, and with since only the changes there.
func (s *Store) ListFilesUnder(ctx context.Context, userID int64, prefix string, since *int64) ([]File, int64, error) {
	if prefix == "" {
		return s.ListFiles(ctx, userID, since)
	}
	current, err := s.CurrentVersion(ctx)
	if err != nil {
		return nil, 0, err
	}
	lo, hi := folderRange(prefix)
	var files []File
	if since == nil {
		files, err = collect(s.db.QueryContext(ctx,
			`SELECT `+fileCols+` FROM files WHERE user_id = ? AND deleted_at IS NULL
			   AND (path = ? OR (path >= ? AND path < ?)) ORDER BY path`, userID, prefix, lo, hi))
	} else {
		files, err = collect(s.db.QueryContext(ctx,
			`SELECT `+fileCols+` FROM files WHERE user_id = ? AND version > ?
			   AND (path = ? OR (path >= ? AND path < ?)) ORDER BY version, path`, userID, *since, prefix, lo, hi))
	}
	return files, current, err
}

// GetFile returns the row for path, tombstone or not.
func (s *Store) GetFile(ctx context.Context, userID int64, path string) (File, error) {
	return getFile(ctx, s.db, userID, path)
}

// FileByUID returns the user's file (live or deleted) with the stable id
// uid. Another user's id is ErrNotFound, exactly like a missing one.
func (s *Store) FileByUID(ctx context.Context, userID int64, uid string) (File, error) {
	return scanFile(s.db.QueryRowContext(ctx,
		`SELECT `+fileCols+` FROM files WHERE user_id = ? AND uid = ?`, userID, uid))
}

func getFile(ctx context.Context, q querier, userID int64, path string) (File, error) {
	return scanFile(q.QueryRowContext(ctx,
		`SELECT `+fileCols+` FROM files WHERE user_id = ? AND path = ?`, userID, path))
}

func liveInFolder(ctx context.Context, q querier, userID int64, folder string) ([]File, error) {
	lo, hi := folderRange(folder)
	return collect(q.QueryContext(ctx,
		`SELECT `+fileCols+` FROM files
		  WHERE user_id = ? AND deleted_at IS NULL AND path >= ? AND path < ?
		  ORDER BY path`, userID, lo, hi))
}

// checkAncestors fails if any ancestor of p is a live *file* (a file cannot
// contain anything). Paths in ignore are being moved away in the same
// transaction and do not count.
func checkAncestors(ctx context.Context, q querier, userID int64, p string, ignore map[string]bool) error {
	for _, a := range pathutil.Ancestors(p) {
		f, err := getFile(ctx, q, userID, a)
		if err == nil && f.Live() && !f.IsDir && !ignore[a] {
			return ErrPathCollision
		}
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return nil
}

// checkNoChildren fails if p has live entries inside it (other than ignored
// ones), i.e. p is in use as a folder.
func checkNoChildren(ctx context.Context, q querier, userID int64, p string, ignore map[string]bool) error {
	lo, hi := folderRange(p)
	rows, err := q.QueryContext(ctx,
		`SELECT path FROM files WHERE user_id = ? AND deleted_at IS NULL AND path >= ? AND path < ? LIMIT ?`,
		userID, lo, hi, len(ignore)+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return err
		}
		if !ignore[path] {
			return ErrPathCollision
		}
	}
	return rows.Err()
}

// ensureParents creates (or revives) a live folder row for every ancestor
// of p that lacks one, stamped with version. Callers have already checked
// that no ancestor is a live file.
func ensureParents(ctx context.Context, tx *sql.Tx, userID int64, p string, version int64, now time.Time) error {
	for _, a := range pathutil.Ancestors(p) {
		f, err := getFile(ctx, tx, userID, a)
		if err == nil && f.Live() {
			continue
		}
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if _, err := upsert(ctx, tx, File{UserID: userID, Path: a, IsDir: true, Version: version, ModifiedAt: now}, uidKeep); err != nil {
			return err
		}
	}
	return nil
}

// uidMode says which stable id an upserted row ends up with.
type uidMode string

const (
	// uidKeep: a live row keeps its id; a new row, or a new file at a
	// previously deleted path, gets a fresh one.
	uidKeep uidMode = "keep"
	// uidRevive: also keep the id of a tombstone (restoring a deleted file).
	uidRevive uidMode = "revive"
	// uidForce: take f.UID (a move: the file keeps its id at the new path).
	uidForce uidMode = "force"
)

// NewUID returns a fresh stable file id: 128 random bits, hex.
func NewUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand doesn't fail on supported platforms
	}
	return hex.EncodeToString(b)
}

func upsert(ctx context.Context, tx *sql.Tx, f File, mode uidMode) (File, error) {
	uid := f.UID
	if mode != uidForce || uid == "" {
		uid = NewUID()
	}
	return scanFile(tx.QueryRowContext(ctx,
		`INSERT INTO files (user_id, path, is_dir, size, sha256, version, modified_at, deleted_at, version_id, uid, mtime, synced_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?)
		 ON CONFLICT (user_id, path) DO UPDATE SET
		   is_dir = excluded.is_dir, size = excluded.size, sha256 = excluded.sha256,
		   version = excluded.version, modified_at = excluded.modified_at, deleted_at = NULL,
		   version_id = excluded.version_id, mtime = excluded.mtime, synced_id = excluded.synced_id, moved_to = NULL,
		   uid = CASE
		     WHEN ? = 'force' THEN excluded.uid
		     WHEN files.uid IS NOT NULL AND (files.deleted_at IS NULL OR ? = 'revive') THEN files.uid
		     ELSE excluded.uid
		   END
		 RETURNING `+fileCols,
		f.UserID, f.Path, f.IsDir, f.Size, f.SHA256, f.Version, fmtTime(f.ModifiedAt), nullID(f.VersionID), uid,
		nullTime(f.Mtime), nullStr(f.SyncedID), string(mode), string(mode)))
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func tombstone(ctx context.Context, tx *sql.Tx, id, version int64, at time.Time) (File, error) {
	return scanFile(tx.QueryRowContext(ctx,
		`UPDATE files SET deleted_at = ?, version = ? WHERE id = ? RETURNING `+fileCols,
		fmtTime(at), version, id))
}

// SetSyncedID records the Null Notes note id of a file's current version
// (read from its content after the commit).
func (s *Store) SetSyncedID(ctx context.Context, fileID, versionID int64, id string) (f File, err error) {
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE file_versions SET synced_id = ? WHERE id = ?`, nullStr(id), versionID); err != nil {
			return err
		}
		f, err = scanFile(tx.QueryRowContext(ctx,
			`UPDATE files SET synced_id = ? WHERE id = ? AND version_id = ? RETURNING `+fileCols, nullStr(id), fileID, versionID))
		return err
	})
	return f, err
}

// CheckPut applies the CommitFile precondition rules to cur (nil if the path
// has no row). noop is true when the upload would not change anything.
//
// It is exported so the API can reject a doomed upload *before* reading the
// request body (saving the bandwidth of a large upload that would 409).
// Pass size < 0 when the size is not known yet. CommitFile re-checks inside
// its transaction, which is the authoritative check.
func CheckPut(cur *File, sha string, size int64, pre Precondition) (noop bool, err error) {
	if cur != nil && cur.Live() {
		if cur.IsDir {
			return false, ErrPathCollision
		}
		if sha != "" && cur.SHA256 == sha && (size < 0 || cur.Size == size) {
			return true, nil
		}
		if !pre.Set {
			return false, ErrPreconditionRequired
		}
		if pre.mismatch(cur.Version) {
			return false, &ConflictError{Current: cur}
		}
		return false, nil
	}
	if pre.Set && !pre.Any && pre.Version != 0 && (cur == nil || pre.Version != cur.Version) {
		return false, &ConflictError{Current: cur}
	}
	return false, nil
}

// CreateFolder creates an (empty) folder and any missing parents. Creating
// a folder that already exists is a no-op (created=false, no new version),
// so "mkdir -p" semantics and retries are safe. A live file at the path or
// at any ancestor is ErrPathCollision.
func (s *Store) CreateFolder(ctx context.Context, userID int64, path string) (f File, created bool, err error) {
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		cur, err := getFile(ctx, tx, userID, path)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if err == nil && cur.Live() {
			if !cur.IsDir {
				return ErrPathCollision
			}
			f = cur
			return nil
		}
		if err := checkAncestors(ctx, tx, userID, path, nil); err != nil {
			return err
		}
		v, err := bump(ctx, tx)
		if err != nil {
			return err
		}
		now := s.now()
		if err := ensureParents(ctx, tx, userID, path, v, now); err != nil {
			return err
		}
		f, err = upsert(ctx, tx, File{UserID: userID, Path: path, IsDir: true, Version: v, ModifiedAt: now}, uidKeep)
		created = err == nil
		return err
	})
	return f, created, err
}

// subtree returns what a delete or move of path acts on: the live row at
// path itself (if any) followed by everything inside it when path is a
// folder. For a live file only the file is returned. Rows are ordered by
// path, so a folder always precedes its contents.
func subtree(ctx context.Context, tx *sql.Tx, userID int64, path string, pre Precondition) ([]File, error) {
	cur, err := getFile(ctx, tx, userID, path)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	var out []File
	if err == nil && cur.Live() {
		if pre.mismatch(cur.Version) {
			return nil, &ConflictError{Current: &cur}
		}
		out = append(out, cur)
		if !cur.IsDir {
			return out, nil
		}
	}
	children, err := liveInFolder(ctx, tx, userID, path)
	if err != nil {
		return nil, err
	}
	out = append(out, children...)
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out, nil
}

// DeleteFile tombstones a file, or a folder and everything inside it. One
// delete, however many rows it touches, takes exactly one version number.
// Parent folders are left in place (deleting the last file in a folder
// leaves an empty folder, as in every desktop file manager).
func (s *Store) DeleteFile(ctx context.Context, userID int64, path string, pre Precondition) (version int64, deleted []File, err error) {
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		targets, err := subtree(ctx, tx, userID, path, pre)
		if err != nil {
			return err
		}
		if version, err = bump(ctx, tx); err != nil {
			return err
		}
		now := s.now()
		for _, t := range targets {
			f, err := tombstone(ctx, tx, t.ID, version, now)
			if err != nil {
				return err
			}
			deleted = append(deleted, f)
		}
		return nil
	})
	return version, deleted, err
}

// Moved pairs an old path with the entry's new row.
type Moved struct {
	From string
	File File
}

// MoveFile renames a file or a folder. Content is not touched: the new rows
// point at the same blobs. The old paths become tombstones so other devices
// learn that they are gone. Missing destination parents are created. A move
// never overwrites: if anything already lives at the destination it fails
// with ErrTargetExists.
func (s *Store) MoveFile(ctx context.Context, userID int64, from, to string, pre Precondition) (version int64, moved []Moved, err error) {
	if from == to || pathutil.IsWithin(to, from) {
		return 0, nil, ErrInvalidMove
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		sources, err := subtree(ctx, tx, userID, from, pre)
		if err != nil {
			return err
		}
		ignore := make(map[string]bool, len(sources))
		for _, f := range sources {
			ignore[f.Path] = true
		}
		dst, err := getFile(ctx, tx, userID, to)
		if err == nil && dst.Live() {
			return ErrTargetExists
		}
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if err := checkAncestors(ctx, tx, userID, to, ignore); err != nil {
			return ErrTargetExists
		}
		if err := checkNoChildren(ctx, tx, userID, to, ignore); err != nil {
			return ErrTargetExists
		}

		if version, err = bump(ctx, tx); err != nil {
			return err
		}
		now := s.now()
		if err := ensureParents(ctx, tx, userID, to, version, now); err != nil {
			return err
		}
		for _, f := range sources {
			newPath := to + strings.TrimPrefix(f.Path, from)
			if _, err := pathutil.Clean(newPath); err != nil {
				return fmt.Errorf("%w: %w", ErrInvalidMove, err)
			}
			if _, err := tombstone(ctx, tx, f.ID, version, now); err != nil {
				return err
			}
			// The old path is gone, not deleted: it must not show up in the
			// recently-deleted bin, and its history moves with the file. Its
			// stable id moves too, so the tombstone gets a throwaway one.
			// It records where the file went (moved_to) and drops the note
			// id, so the moved file is the only row carrying it.
			if _, err := tx.ExecContext(ctx, `UPDATE files SET version_id = NULL, uid = ?, moved_to = ?, synced_id = NULL WHERE id = ?`,
				NewUID(), to+strings.TrimPrefix(f.Path, from), f.ID); err != nil {
				return err
			}
			if !f.IsDir {
				if _, err := tx.ExecContext(ctx,
					`UPDATE file_versions SET path = ? WHERE user_id = ? AND path = ?`, newPath, userID, f.Path); err != nil {
					return err
				}
			}
			nf, err := upsert(ctx, tx, File{
				UserID: userID, Path: newPath, IsDir: f.IsDir, Size: f.Size, SHA256: f.SHA256,
				Version: version, ModifiedAt: f.ModifiedAt, VersionID: f.VersionID, UID: f.UID,
				Mtime: f.Mtime, SyncedID: f.SyncedID,
			}, uidForce)
			if err != nil {
				return err
			}
			moved = append(moved, Moved{From: f.Path, File: nf})
		}
		return nil
	})
	return version, moved, err
}
