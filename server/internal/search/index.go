// Package search keeps a full-text index of each user's files and answers
// GET /search (Null franchise search contract v1).
//
// Every user has their own SQLite database, <data>/indexes/<user id>.db,
// never a shared table. The file name comes from the authenticated user's
// id only, so a request can't even name another user's index. Deleting a
// user's index is deleting a file.
//
// The index follows the server's change feed (the same one sync clients
// use): it remembers the last feed version it processed and applies what
// changed since. So it catches up after a restart, rebuilds itself if the
// file is deleted, and needs no hooks in the write paths beyond a nudge
// (Kick) to stay fresh between searches. Accounts with end-to-end
// encryption are never indexed: the server can't read their names or
// contents, and an index of plaintext would undo the encryption.
package search

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Null0Grid/null-drive/server/internal/chunkstore"
	"github.com/Null0Grid/null-drive/server/internal/db"
	"github.com/Null0Grid/null-drive/server/internal/meta"
	"github.com/Null0Grid/null-drive/shared/api"
)

// ErrEncrypted: the account uses end-to-end encryption; there's nothing
// the server could index.
var ErrEncrypted = errors.New("search: account is end-to-end encrypted")

// schemaVersion of the index files. An index with another version is
// deleted and rebuilt from the change feed.
const schemaVersion = "1"

const schema = `
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
-- One row per indexed file; rowid is shared with fts.
CREATE TABLE docs (
    rowid    INTEGER PRIMARY KEY,
    uid      TEXT NOT NULL UNIQUE,  -- the file's stable id
    path     TEXT NOT NULL,
    name     TEXT NOT NULL,
    modified TEXT NOT NULL,
    sha256   TEXT NOT NULL,
    fm_id    TEXT NOT NULL DEFAULT ''  -- id: from Markdown frontmatter
);
-- name, its squashed form (letters and digits only) and the text.
-- trigram: case-insensitive substring matching; remove_diacritics so
-- "resume" finds "résumé".
CREATE VIRTUAL TABLE fts USING fts5(name, key, body, tokenize = 'trigram remove_diacritics 1');
-- Null Notes vaults synced into Drive: any live file under a .nullnotes/
-- folder marks its parent as a vault root.
CREATE TABLE vault_files (path TEXT PRIMARY KEY, root TEXT NOT NULL);
-- Stable ids of non-Markdown notes, from <root>/.nullnotes/ids.json.
CREATE TABLE vault_ids (root TEXT NOT NULL, rel TEXT NOT NULL, id TEXT NOT NULL, PRIMARY KEY (root, rel));
`

// Manager owns the per-user indexes.
type Manager struct {
	dir    string
	store  *meta.Store
	chunks *chunkstore.Store
	log    *slog.Logger

	mu     sync.Mutex
	users  map[int64]*userIndex
	closed bool
}

type userIndex struct {
	sync   sync.Mutex   // one Sync or Drop at a time
	dbMu   sync.RWMutex // guards db (readers: Search)
	db     *sql.DB
	kicked atomic.Bool
}

// New returns a Manager keeping indexes in dir (created on first use).
func New(dir string, store *meta.Store, chunks *chunkstore.Store, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Manager{dir: dir, store: store, chunks: chunks, log: log, users: map[int64]*userIndex{}}
}

func (m *Manager) user(id int64) *userIndex {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.users[id]
	if u == nil {
		u = &userIndex{}
		m.users[id] = u
	}
	return u
}

// path of a user's index. Built from the numeric id only.
func (m *Manager) path(userID int64) string {
	return filepath.Join(m.dir, strconv.FormatInt(userID, 10)+".db")
}

// Kick schedules a catch-up for userID shortly (bursts of changes coalesce).
func (m *Manager) Kick(userID int64) {
	u := m.user(userID)
	if u.kicked.Swap(true) {
		return
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		u.kicked.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := m.Sync(ctx, userID); err != nil && !errors.Is(err, ErrEncrypted) && !errors.Is(err, errClosed) {
			m.log.Warn("search index update", "user_id", userID, "err", err)
		}
	}()
}

var errClosed = errors.New("search: closed")

// Drop deletes a user's index (end-to-end encryption turned on).
func (m *Manager) Drop(userID int64) error {
	u := m.user(userID)
	u.sync.Lock()
	defer u.sync.Unlock()
	return m.dropLocked(userID, u)
}

func (m *Manager) dropLocked(userID int64, u *userIndex) error {
	u.dbMu.Lock()
	defer u.dbMu.Unlock()
	if u.db != nil {
		_ = u.db.Close()
		u.db = nil
	}
	var first error
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.Remove(m.path(userID) + suffix); err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
			first = err
		}
	}
	return first
}

// Close closes every open index.
func (m *Manager) Close() error {
	m.mu.Lock()
	m.closed = true
	users := m.users
	m.mu.Unlock()
	for _, u := range users {
		u.sync.Lock()
		u.dbMu.Lock()
		if u.db != nil {
			_ = u.db.Close()
			u.db = nil
		}
		u.dbMu.Unlock()
		u.sync.Unlock()
	}
	return nil
}

func (m *Manager) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

// open opens (creating if needed) the user's index; called under u.sync.
func (m *Manager) open(userID int64, u *userIndex) (*sql.DB, error) {
	u.dbMu.RLock()
	d := u.db
	u.dbMu.RUnlock()
	if d != nil {
		return d, nil
	}
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return nil, err
	}
	p := m.path(userID)
	d, err := db.Open(p)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(p, 0o600)
	var v string
	err = d.QueryRow(`SELECT value FROM meta WHERE key = 'schema'`).Scan(&v)
	if err != nil || v != schemaVersion {
		// New file, or an index from another version: start over.
		_ = d.Close()
		if err := m.dropLocked(userID, u); err != nil {
			return nil, err
		}
		if d, err = db.Open(p); err != nil {
			return nil, err
		}
		_ = os.Chmod(p, 0o600)
		if _, err := d.Exec(schema); err != nil {
			_ = d.Close()
			return nil, fmt.Errorf("search: create index: %w", err)
		}
		if _, err := d.Exec(`INSERT INTO meta (key, value) VALUES ('schema', ?), ('version', '-1')`, schemaVersion); err != nil {
			_ = d.Close()
			return nil, err
		}
	}
	u.dbMu.Lock()
	u.db = d
	u.dbMu.Unlock()
	return d, nil
}

// Sync brings the user's index up to date with the change feed.
func (m *Manager) Sync(ctx context.Context, userID int64) error {
	if m.isClosed() {
		return errClosed
	}
	u := m.user(userID)
	u.sync.Lock()
	defer u.sync.Unlock()

	k, err := m.store.E2EEKeys(ctx, userID)
	if err != nil {
		return err
	}
	if k.State != meta.E2EEOff {
		if err := m.dropLocked(userID, u); err != nil {
			return err
		}
		return ErrEncrypted
	}
	d, err := m.open(userID, u)
	if err != nil {
		return err
	}
	var vs string
	if err := d.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'version'`).Scan(&vs); err != nil {
		return err
	}
	since, _ := strconv.ParseInt(vs, 10, 64)
	var files []meta.File
	var current int64
	if since < 0 {
		files, current, err = m.store.ListFiles(ctx, userID, nil)
	} else {
		files, current, err = m.store.ListFiles(ctx, userID, &since)
	}
	if err != nil {
		return err
	}
	if since > current {
		// The feed went backwards: the database was restored from a backup
		// older than this index. Its rows may describe files that no longer
		// exist; start over.
		since = -1
		if files, current, err = m.store.ListFiles(ctx, userID, nil); err != nil {
			return err
		}
	}
	if since >= 0 && len(files) == 0 {
		return nil
	}
	if since < 0 {
		if _, err := d.ExecContext(ctx, `DELETE FROM docs; DELETE FROM fts; DELETE FROM vault_files; DELETE FROM vault_ids;`); err != nil {
			return err
		}
	}
	// Small transactions, so searches see progress during a big first
	// build (a search waits only briefly for a sync to finish).
	const batch = 25
	for i := 0; i < len(files); i += batch {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(i+batch, len(files))
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, f := range files[i:end] {
			if err := m.apply(ctx, tx, f); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("index %q: %w", f.Path, err)
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	_, err = d.ExecContext(ctx, `UPDATE meta SET value = ? WHERE key = 'version'`, strconv.FormatInt(current, 10))
	return err
}

func uidOf(f meta.File) string {
	if f.UID != "" {
		return f.UID
	}
	return "row-" + strconv.FormatInt(f.ID, 10)
}

// vaultRoot returns the vault folder a path under ".nullnotes/" belongs to.
func vaultRoot(p string) (root string, ok bool) {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if s == ".nullnotes" {
			return strings.Join(segs[:i], "/"), true
		}
	}
	return "", false
}

func deleteDoc(ctx context.Context, tx *sql.Tx, uid string) error {
	var rowid int64
	err := tx.QueryRowContext(ctx, `SELECT rowid FROM docs WHERE uid = ?`, uid).Scan(&rowid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fts WHERE rowid = ?`, rowid); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM docs WHERE rowid = ?`, rowid)
	return err
}

// apply indexes one change-feed row.
func (m *Manager) apply(ctx context.Context, tx *sql.Tx, f meta.File) error {
	if f.IsDir {
		return nil // folders aren't results; their files have their own rows
	}
	uid := uidOf(f)
	name := path.Base(f.Path)

	if root, ok := vaultRoot(f.Path); ok {
		// Null Notes vault data: not a document, but it marks the vault
		// and, for ids.json, carries the ids of non-Markdown notes.
		if err := deleteDoc(ctx, tx, uid); err != nil {
			return err
		}
		if !f.Live() {
			if _, err := tx.ExecContext(ctx, `DELETE FROM vault_files WHERE path = ?`, f.Path); err != nil {
				return err
			}
			if name == "ids.json" && path.Dir(f.Path) == path.Join(root, ".nullnotes") {
				_, err := tx.ExecContext(ctx, `DELETE FROM vault_ids WHERE root = ?`, root)
				return err
			}
			return nil
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO vault_files (path, root) VALUES (?, ?) ON CONFLICT (path) DO UPDATE SET root = excluded.root`,
			f.Path, root); err != nil {
			return err
		}
		if name == "ids.json" && path.Dir(f.Path) == path.Join(root, ".nullnotes") {
			return m.loadVaultIDs(ctx, tx, root, f)
		}
		return nil
	}
	if !f.Live() || strings.HasSuffix(name, ".nntmp") {
		return deleteDoc(ctx, tx, uid)
	}

	var cur struct {
		rowid int64
		sha   string
	}
	err := tx.QueryRowContext(ctx, `SELECT rowid, sha256 FROM docs WHERE uid = ?`, uid).Scan(&cur.rowid, &cur.sha)
	switch {
	case err == nil && cur.sha == f.SHA256:
		// Renamed or moved, content unchanged: keep the extracted text.
		if _, err := tx.ExecContext(ctx, `UPDATE docs SET path = ?, name = ?, modified = ? WHERE rowid = ?`,
			f.Path, name, f.ModifiedAt.UTC().Format(time.RFC3339), cur.rowid); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE fts SET name = ?, key = ? WHERE rowid = ?`, name, squash(name), cur.rowid)
		return err
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	}

	ex, xerr := extract(ctx, name, f.Size, func(n int64) ([]byte, error) { return m.read(ctx, f, n) })
	if xerr != nil {
		// Unreadable content (a broken PDF, a missing chunk): the file is
		// still findable by name.
		m.log.Warn("search: can't extract text; indexing the name only", "path", f.Path, "err", xerr)
		ex = extracted{}
	}
	if err := deleteDoc(ctx, tx, uid); err != nil {
		return err
	}
	if ex.locked {
		return nil
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO docs (uid, path, name, modified, sha256, fm_id) VALUES (?, ?, ?, ?, ?, ?)`,
		uid, f.Path, name, f.ModifiedAt.UTC().Format(time.RFC3339), f.SHA256, ex.fmID)
	if err != nil {
		return err
	}
	rowid, err := res.LastInsertId()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO fts (rowid, name, key, body) VALUES (?, ?, ?, ?)`,
		rowid, name, squash(name), ex.body)
	return err
}

// read returns up to n bytes of a file's current content.
func (m *Manager) read(ctx context.Context, f meta.File, n int64) ([]byte, error) {
	if f.VersionID == 0 || f.Size == 0 {
		return nil, nil
	}
	chunks, err := m.store.Manifest(ctx, f.VersionID)
	if err != nil {
		return nil, err
	}
	rc := m.chunks.Open(chunks)
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, n))
}

func (m *Manager) loadVaultIDs(ctx context.Context, tx *sql.Tx, root string, f meta.File) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM vault_ids WHERE root = ?`, root); err != nil {
		return err
	}
	b, err := m.read(ctx, f, 4<<20)
	if err != nil {
		m.log.Warn("search: can't read Null Notes ids.json", "path", f.Path, "err", err)
		return nil
	}
	var ids map[string]string
	if err := json.Unmarshal(b, &ids); err != nil {
		m.log.Warn("search: invalid Null Notes ids.json", "path", f.Path, "err", err)
		return nil
	}
	for rel, id := range ids {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO vault_ids (root, rel, id) VALUES (?, ?, ?)`,
			root, rel, strings.ToLower(id)); err != nil {
			return err
		}
	}
	return nil
}

// ---- searching ----

// Limits for GET /search (the same as the Null Notes agent's).
const (
	DefaultLimit = 20
	MaxLimit     = 100
	// syncWait is how long a search waits for the index to catch up
	// before answering from what's indexed so far.
	syncWait = 700 * time.Millisecond
)

// Search returns userID's matches for q. It first lets the index catch up
// with recent changes (waiting at most syncWait).
func (m *Manager) Search(ctx context.Context, userID int64, q string, limit int) ([]api.SearchResult, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	pq := parseQuery(q)

	done := make(chan error, 1)
	go func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
		defer cancel()
		done <- m.Sync(sctx, userID)
	}()
	select {
	case err := <-done:
		if errors.Is(err, ErrEncrypted) {
			return []api.SearchResult{}, ErrEncrypted
		}
		if err != nil {
			return nil, err
		}
	case <-time.After(syncWait):
		// Still indexing (a large first build): answer with what's there.
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if pq.empty() {
		return []api.SearchResult{}, nil
	}

	u := m.user(userID)
	u.dbMu.RLock()
	defer u.dbMu.RUnlock()
	if u.db == nil {
		return []api.SearchResult{}, nil
	}
	return m.query(ctx, u.db, pq, limit)
}

type hit struct {
	rowid    int64
	rank     int // position in the term query's relevance order
	nameHit  bool
	uid      string
	path     string
	name     string
	modified string
	fmID     string
	body     string
}

func (m *Manager) query(ctx context.Context, d *sql.DB, q query, limit int) ([]api.SearchResult, error) {
	order := map[int64]int{}
	add := func(rowid int64) {
		if _, ok := order[rowid]; !ok {
			order[rowid] = len(order)
		}
	}

	// Title matches via the squashed name ("libreai" → "Libre AI.md").
	if sm := q.titleSquashMatch(); sm != "" {
		rows, err := d.QueryContext(ctx, `SELECT rowid FROM fts WHERE fts MATCH ? LIMIT ?`, sm, limit)
		if err != nil {
			return nil, err
		}
		if err := scanIDs(rows, add); err != nil {
			return nil, err
		}
	}
	// Every term in the name or the text.
	match, likes, args := q.termsMatch()
	var sqlq string
	if match != "" {
		sqlq = `SELECT rowid FROM fts WHERE fts MATCH ?`
		args = append([]any{match}, args...)
		for _, l := range likes {
			sqlq += " AND " + l
		}
		sqlq += ` ORDER BY bm25(fts, 10.0, 0.0, 1.0) LIMIT ?`
	} else {
		// Only short terms: no trigrams to match, so LIKE alone, newest first.
		for i, l := range likes {
			likes[i] = strings.ReplaceAll(strings.ReplaceAll(l, "name LIKE", "f.name LIKE"), "body LIKE", "f.body LIKE")
		}
		sqlq = `SELECT f.rowid FROM fts f JOIN docs d ON d.rowid = f.rowid WHERE ` + strings.Join(likes, " AND ") +
			` ORDER BY d.modified DESC LIMIT ?`
	}
	args = append(args, limit*3)
	rows, err := d.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	if err := scanIDs(rows, add); err != nil {
		return nil, err
	}
	if len(order) == 0 {
		return []api.SearchResult{}, nil
	}

	hits := make([]hit, 0, len(order))
	for rowid, rank := range order {
		h := hit{rowid: rowid, rank: rank}
		err := d.QueryRowContext(ctx,
			`SELECT d.uid, d.path, d.name, d.modified, d.fm_id, f.body FROM docs d JOIN fts f ON f.rowid = d.rowid WHERE d.rowid = ?`,
			rowid).Scan(&h.uid, &h.path, &h.name, &h.modified, &h.fmID, &h.body)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		h.nameHit = q.nameHit(h.name)
		hits = append(hits, h)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].nameHit != hits[j].nameHit {
			return hits[i].nameHit
		}
		return hits[i].rank < hits[j].rank
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}

	roots, err := vaultRoots(ctx, d)
	if err != nil {
		return nil, err
	}
	out := make([]api.SearchResult, 0, len(hits))
	for _, h := range hits {
		r := api.SearchResult{
			ID: h.uid, Source: "drive", Title: h.name, Snippet: q.snippet(h.body), Path: h.path,
			Open: "nulldrive://open?id=" + h.uid, Modified: h.modified,
		}
		if id := syncedID(ctx, d, roots, h); id != "" {
			r.SyncedID = &id
		}
		out = append(out, r)
	}
	return out, nil
}

func scanIDs(rows *sql.Rows, add func(int64)) error {
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		add(id)
	}
	return rows.Err()
}

func vaultRoots(ctx context.Context, d *sql.DB) ([]string, error) {
	rows, err := d.QueryContext(ctx, `SELECT DISTINCT root FROM vault_files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// syncedID: the Null Notes note id of a file. For Markdown it's the
// frontmatter id (stored for every .md, wherever it is: a backup may put a
// vault anywhere, and a UUID equal to a note's id means it is that note).
// Other notes are looked up in their vault's .nullnotes/ids.json, so they
// need the vault (a folder with .nullnotes/) to have been synced too.
func syncedID(ctx context.Context, d *sql.DB, roots []string, h hit) string {
	if kindOf(h.name) == kindMarkdown {
		return h.fmID
	}
	root, ok := "", false
	for _, r := range roots {
		if (r == "" || strings.HasPrefix(h.path, r+"/")) && (!ok || len(r) > len(root)) {
			root, ok = r, true
		}
	}
	if !ok {
		return ""
	}
	rel := h.path
	if root != "" {
		rel = strings.TrimPrefix(h.path, root+"/")
	}
	var id string
	if err := d.QueryRowContext(ctx, `SELECT id FROM vault_ids WHERE root = ? AND rel = ?`, root, rel).Scan(&id); err != nil {
		return ""
	}
	return id
}
