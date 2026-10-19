package sync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite, same driver as the server
)

// Index is the client's durable state. It is the source of truth after a
// crash: anything not recorded here is re-derived by the next cycle.
//
//	base     last-synced state per path (the "B" of the three-way merge)
//	remote   mirror of the server's live entries, kept fresh from the feed
//	meta     cursor (server version the mirror is current to) and settings
//	conflicts conflict copies created, for the UI to show and resolve
type Index struct {
	db *sql.DB
}

// ConflictRecord is one "keep both" event.
type ConflictRecord struct {
	ID           int64     `json:"id"`
	Path         string    `json:"path"`          // the original path (server's version now)
	ConflictPath string    `json:"conflict_path"` // the copy holding this device's version
	Device       string    `json:"device"`
	At           time.Time `json:"at"`
}

const indexSchema = `
CREATE TABLE IF NOT EXISTS base (
  path TEXT PRIMARY KEY, is_dir INTEGER NOT NULL, size INTEGER NOT NULL,
  mtime_ns INTEGER NOT NULL, sha256 TEXT NOT NULL, version INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS remote (
  path TEXT PRIMARY KEY, is_dir INTEGER NOT NULL, size INTEGER NOT NULL,
  sha256 TEXT NOT NULL, version INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS conflicts (
  id INTEGER PRIMARY KEY, path TEXT NOT NULL, conflict_path TEXT NOT NULL,
  device TEXT NOT NULL, at TEXT NOT NULL);
`

// OpenIndex opens (creating if needed) the index database at path. Keep it
// outside the sync folder (the desktop app uses the OS config dir).
func OpenIndex(path string) (*Index, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer; keeps the engine's view consistent
	if _, err := db.Exec(indexSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("index schema: %w", err)
	}
	return &Index{db: db}, nil
}

func (ix *Index) Close() error { return ix.db.Close() }

// ---- meta ----

func (ix *Index) Meta(ctx context.Context, key string) (string, error) {
	var v string
	err := ix.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (ix *Index) SetMeta(ctx context.Context, key, value string) error {
	_, err := ix.db.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// Cursor is the server version the remote mirror is current to (0 = never synced).
func (ix *Index) Cursor(ctx context.Context) (int64, error) {
	v, err := ix.Meta(ctx, "cursor")
	if err != nil || v == "" {
		return 0, err
	}
	var n int64
	_, err = fmt.Sscan(v, &n)
	return n, err
}

// Reset forgets all sync state (used when the server or folder changes).
// The next cycle treats both sides as new: identical files converge,
// different ones become conflicts. Nothing is deleted.
func (ix *Index) Reset(ctx context.Context) error {
	_, err := ix.db.ExecContext(ctx, `DELETE FROM base; DELETE FROM remote; DELETE FROM meta WHERE key = 'cursor';`)
	return err
}

// ---- base ----

func (ix *Index) Bases(ctx context.Context) (map[string]*Entry, error) {
	rows, err := ix.db.QueryContext(ctx, `SELECT path, is_dir, size, mtime_ns, sha256, version FROM base`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*Entry{}
	for rows.Next() {
		e := &Entry{}
		if err := rows.Scan(&e.Path, &e.IsDir, &e.Size, &e.MtimeNs, &e.SHA256, &e.Version); err != nil {
			return nil, err
		}
		out[e.Path] = e
	}
	return out, rows.Err()
}

func (ix *Index) PutBase(ctx context.Context, e Entry) error {
	_, err := ix.db.ExecContext(ctx,
		`INSERT INTO base (path, is_dir, size, mtime_ns, sha256, version) VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (path) DO UPDATE SET is_dir = excluded.is_dir, size = excluded.size,
		   mtime_ns = excluded.mtime_ns, sha256 = excluded.sha256, version = excluded.version`,
		e.Path, e.IsDir, e.Size, e.MtimeNs, e.SHA256, e.Version)
	return err
}

// DeleteBase removes path and, for folders, everything below it.
func (ix *Index) DeleteBase(ctx context.Context, p string) error {
	lo, hi := p+"/", p+"0"
	_, err := ix.db.ExecContext(ctx, `DELETE FROM base WHERE path = ? OR (path >= ? AND path < ?)`, p, lo, hi)
	return err
}

// ---- remote mirror ----

func (ix *Index) Remotes(ctx context.Context) (map[string]*Remote, error) {
	rows, err := ix.db.QueryContext(ctx, `SELECT path, is_dir, size, sha256, version FROM remote`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*Remote{}
	for rows.Next() {
		r := &Remote{}
		if err := rows.Scan(&r.Path, &r.IsDir, &r.Size, &r.SHA256, &r.Version); err != nil {
			return nil, err
		}
		out[r.Path] = r
	}
	return out, rows.Err()
}

// RemoteChange is one entry of the server's change feed.
type RemoteChange struct {
	Remote
	Deleted bool
}

// ApplyFeed updates the mirror and the cursor in one transaction, so a crash
// can never leave the cursor ahead of the mirror (which would skip changes).
// replace=true replaces the whole mirror (initial full listing).
func (ix *Index) ApplyFeed(ctx context.Context, changes []RemoteChange, cursor int64, replace bool) error {
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if replace {
		if _, err := tx.ExecContext(ctx, `DELETE FROM remote`); err != nil {
			return err
		}
	}
	for _, c := range changes {
		if err := putRemote(ctx, tx, c); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES ('cursor', ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		fmt.Sprint(cursor)); err != nil {
		return err
	}
	return tx.Commit()
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func putRemote(ctx context.Context, x execer, c RemoteChange) error {
	if c.Deleted {
		_, err := x.ExecContext(ctx, `DELETE FROM remote WHERE path = ?`, c.Path)
		return err
	}
	_, err := x.ExecContext(ctx,
		`INSERT INTO remote (path, is_dir, size, sha256, version) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (path) DO UPDATE SET is_dir = excluded.is_dir, size = excluded.size,
		   sha256 = excluded.sha256, version = excluded.version`,
		c.Path, c.IsDir, c.Size, c.SHA256, c.Version)
	return err
}

// PutRemote records a single server change we caused (so the mirror is
// right before the feed reports it back).
func (ix *Index) PutRemote(ctx context.Context, c RemoteChange) error {
	return putRemote(ctx, ix.db, c)
}

// DeleteRemoteTree removes path and everything below it from the mirror.
func (ix *Index) DeleteRemoteTree(ctx context.Context, p string) error {
	_, err := ix.db.ExecContext(ctx, `DELETE FROM remote WHERE path = ? OR (path >= ? AND path < ?)`, p, p+"/", p+"0")
	return err
}

// ---- conflicts ----

func (ix *Index) AddConflict(ctx context.Context, c ConflictRecord) (int64, error) {
	res, err := ix.db.ExecContext(ctx, `INSERT INTO conflicts (path, conflict_path, device, at) VALUES (?, ?, ?, ?)`,
		c.Path, c.ConflictPath, c.Device, c.At.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (ix *Index) Conflicts(ctx context.Context) ([]ConflictRecord, error) {
	rows, err := ix.db.QueryContext(ctx, `SELECT id, path, conflict_path, device, at FROM conflicts ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConflictRecord{}
	for rows.Next() {
		var c ConflictRecord
		var at string
		if err := rows.Scan(&c.ID, &c.Path, &c.ConflictPath, &c.Device, &at); err != nil {
			return nil, err
		}
		c.At, _ = time.Parse(time.RFC3339, at)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (ix *Index) ConflictByID(ctx context.Context, id int64) (ConflictRecord, error) {
	cs, err := ix.Conflicts(ctx)
	if err != nil {
		return ConflictRecord{}, err
	}
	for _, c := range cs {
		if c.ID == id {
			return c, nil
		}
	}
	return ConflictRecord{}, fmt.Errorf("conflict %d not found", id)
}

func (ix *Index) DeleteConflict(ctx context.Context, id int64) error {
	_, err := ix.db.ExecContext(ctx, `DELETE FROM conflicts WHERE id = ?`, id)
	return err
}

// isWithin reports whether child is strictly inside folder parent.
func isWithin(child, parent string) bool { return strings.HasPrefix(child, parent+"/") }

// UseKeyFingerprint records which encryption key (e2ee fingerprint, ""
// for none) the index's hashes and paths belong to. When it changes (the
// account was switched to end-to-end encryption, or its key replaced) the
// synced state is reset, so the next cycle re-adopts files by comparing
// content in the new hash space instead of seeing every file as changed
// on both sides (which would make conflict copies of everything).
// Local files are not touched. Returns whether a reset happened.
func (ix *Index) UseKeyFingerprint(ctx context.Context, fp string) (bool, error) {
	cur, err := ix.Meta(ctx, "key_fingerprint")
	if err != nil {
		return false, err
	}
	if cur == fp {
		return false, nil
	}
	bases, err := ix.Bases(ctx)
	if err != nil {
		return false, err
	}
	reset := len(bases) > 0 || cur != ""
	if reset {
		if err := ix.Reset(ctx); err != nil {
			return false, err
		}
	}
	return reset, ix.SetMeta(ctx, "key_fingerprint", fp)
}
