package meta

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Null0Grid/null-drive/shared/e2ee"
)

// End-to-end encryption state (Phase 7). The server's whole job here is
// bookkeeping: store the client's opaque key bundle, refuse plaintext
// writes once encryption is on, and delete the plaintext when the client
// has finished re-uploading everything encrypted.

// E2EE states (api.E2EE* mirror these).
const (
	E2EEOff       = "off"
	E2EEMigrating = "migrating"
	E2EEOn        = "on"
)

// Errors.
var (
	ErrE2EEAlreadyOn = errors.New("end-to-end encryption is already on for this account")
	ErrE2EEOff       = errors.New("end-to-end encryption is not on for this account")
	ErrE2EEAuth      = errors.New("the key proof does not match this account's encryption key")
)

// E2EEKeys is an account's user_keys row.
type E2EEKeys struct {
	State       string
	Bundle      string
	Fingerprint string
	AuthHash    string
	EnabledAt   time.Time
	FinishedAt  *time.Time
}

// E2EEKeys returns the account's encryption row (State "off" and no error
// if encryption was never turned on).
func (s *Store) E2EEKeys(ctx context.Context, userID int64) (E2EEKeys, error) {
	var k E2EEKeys
	var enabled string
	var finished sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT state, bundle, fingerprint, auth_hash, enabled_at, finished_at FROM user_keys WHERE user_id = ?`, userID).
		Scan(&k.State, &k.Bundle, &k.Fingerprint, &k.AuthHash, &enabled, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return E2EEKeys{State: E2EEOff}, nil
	}
	if err != nil {
		return k, err
	}
	k.EnabledAt = parseTime(enabled)
	k.FinishedAt = parseNullTime(finished)
	return k, nil
}

// EnableE2EE stores the bundle. If the account already has files or
// folders, it goes to "migrating" (the client re-uploads them encrypted,
// then calls FinishE2EE); an empty account is "on" immediately.
func (s *Store) EnableE2EE(ctx context.Context, userID int64, bundle json.RawMessage, fingerprint, authHash string) (string, error) {
	state := E2EEOn
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_keys WHERE user_id = ?`, userID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrE2EEAlreadyOn
		}
		var files int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM files WHERE user_id = ?`, userID).Scan(&files); err != nil {
			return err
		}
		var versions int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM file_versions WHERE user_id = ?`, userID).Scan(&versions); err != nil {
			return err
		}
		now := fmtTime(s.now())
		var finished any
		if files+versions > 0 {
			state = E2EEMigrating
		} else {
			finished = now
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO user_keys (user_id, state, bundle, fingerprint, auth_hash, enabled_at, updated_at, finished_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, userID, state, string(bundle), fingerprint, authHash, now, now, finished)
		return err
	})
	return state, err
}

func checkAuth(k E2EEKeys, authKey string) error {
	if k.State == E2EEOff {
		return ErrE2EEOff
	}
	if subtle.ConstantTimeCompare([]byte(k.AuthHash), []byte(e2ee.AuthHash(authKey))) != 1 {
		return ErrE2EEAuth
	}
	return nil
}

// UpdateE2EEBundle replaces the bundle (new passphrase or recovery key).
// The caller must present the auth key, and the fingerprint must stay the
// same: this can re-wrap the master key, never swap it.
func (s *Store) UpdateE2EEBundle(ctx context.Context, userID int64, authKey string, bundle json.RawMessage, fingerprint string) error {
	k, err := s.E2EEKeys(ctx, userID)
	if err != nil {
		return err
	}
	if err := checkAuth(k, authKey); err != nil {
		return err
	}
	if fingerprint != k.Fingerprint {
		return ErrE2EEAuth
	}
	_, err = s.db.ExecContext(ctx, `UPDATE user_keys SET bundle = ?, updated_at = ? WHERE user_id = ?`,
		string(bundle), fmtTime(s.now()), userID)
	return err
}

// CountPlaintext counts files, folders and versions that are not stored
// under encrypted paths.
func (s *Store) CountPlaintext(ctx context.Context, userID int64) (int64, error) {
	ids, _, err := s.plaintextRows(ctx, s.db, userID)
	return int64(len(ids)), err
}

func (s *Store) plaintextRows(ctx context.Context, q querier, userID int64) (files, versions []int64, err error) {
	scan := func(query string) ([]int64, error) {
		rows, err := q.QueryContext(ctx, query, userID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []int64
		for rows.Next() {
			var id int64
			var p string
			if err := rows.Scan(&id, &p); err != nil {
				return nil, err
			}
			if !e2ee.IsEncryptedPath(p) {
				out = append(out, id)
			}
		}
		return out, rows.Err()
	}
	if files, err = scan(`SELECT id, path FROM files WHERE user_id = ?`); err != nil {
		return nil, nil, err
	}
	versions, err = scan(`SELECT id, path FROM file_versions WHERE user_id = ?`)
	return files, versions, err
}

// PurgeResult reports what FinishE2EE removed.
type PurgeResult struct {
	Paths  int64
	Chunks []string // candidates for immediate deletion (now unreferenced)
}

// FinishE2EE deletes every row of the account that is not under an
// encrypted path (files, tombstones, folders and their whole history) and
// the account's pending chunk uploads, then marks encryption "on". The
// returned chunk hashes lost their last reference here; chunkstore.Purge
// deletes them right away instead of after the usual GC grace period, so
// no plaintext stays on disk for a day.
//
// Deleted rows are overwritten in the database file (PRAGMA secure_delete,
// see db.Open) and the WAL is truncated afterwards by the caller.
func (s *Store) FinishE2EE(ctx context.Context, userID int64, authKey string) (PurgeResult, error) {
	var res PurgeResult
	k, err := s.E2EEKeys(ctx, userID)
	if err != nil {
		return res, err
	}
	if err := checkAuth(k, authKey); err != nil {
		return res, err
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		files, versions, err := s.plaintextRows(ctx, tx, userID)
		if err != nil {
			return err
		}
		res.Paths = int64(len(files))
		fa, _ := json.Marshal(files)
		va, _ := json.Marshal(versions)
		// Chunks referenced by the doomed versions, and pending uploads.
		rows, err := tx.QueryContext(ctx, `
			SELECT DISTINCT hash FROM version_chunks WHERE version_id IN (SELECT value FROM json_each(?))
			UNION SELECT hash FROM chunk_uploads WHERE user_id = ?`, string(va), userID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				_ = rows.Close()
				return err
			}
			res.Chunks = append(res.Chunks, h)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		stmts := []struct {
			q    string
			args []any
		}{
			{`DELETE FROM files WHERE id IN (SELECT value FROM json_each(?))`, []any{string(fa)}},
			// Encrypted files can't point at a plaintext version, but be
			// safe: no dangling version_id after the delete below.
			{`UPDATE files SET version_id = NULL WHERE user_id = ? AND version_id IN (SELECT value FROM json_each(?))`, []any{userID, string(va)}},
			{`DELETE FROM version_chunks WHERE version_id IN (SELECT value FROM json_each(?))`, []any{string(va)}},
			{`DELETE FROM file_versions WHERE id IN (SELECT value FROM json_each(?))`, []any{string(va)}},
			{`DELETE FROM chunk_uploads WHERE user_id = ?`, []any{userID}},
			{`UPDATE user_keys SET state = 'on', finished_at = COALESCE(finished_at, ?), updated_at = ? WHERE user_id = ?`,
				[]any{fmtTime(s.now()), fmtTime(s.now()), userID}},
		}
		for _, st := range stmts {
			if _, err := tx.ExecContext(ctx, st.q, st.args...); err != nil {
				return err
			}
		}
		if len(files) > 0 {
			// Tell other devices to refetch the listing.
			if _, err := bump(ctx, tx); err != nil {
				return err
			}
		}
		return nil
	})
	return res, err
}

// DeleteChunkIfOrphaned deletes a chunk row right away if nothing
// references it and no user has a pending upload of it (another user who
// uploaded the same plaintext chunk a moment ago keeps it). Unlike the
// regular GC there is no grace period: used for purging plaintext.
func (s *Store) DeleteChunkIfOrphaned(ctx context.Context, hash string) (bool, int64, error) {
	var size int64
	err := s.db.QueryRowContext(ctx,
		`DELETE FROM chunks WHERE hash = ? AND refcount <= 0
		   AND NOT EXISTS (SELECT 1 FROM chunk_uploads WHERE hash = ?)
		 RETURNING size`, hash, hash).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, nil
	}
	return err == nil, size, err
}

// Compact rebuilds the database file (VACUUM) and truncates the WAL.
// secure_delete already zeroes deleted rows, but copies of index keys can
// survive in interior b-tree pages until those pages are rebalanced;
// rebuilding the file leaves only live data. It runs once per account,
// when encryption is turned on, and the metadata database is small.
func (s *Store) Compact(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `VACUUM`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}
