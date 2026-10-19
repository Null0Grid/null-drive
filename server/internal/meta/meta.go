// Package meta is the metadata repository: users, sessions, devices, files
// and settings, on top of SQLite.
//
// All multi-step writes run in a transaction that takes the SQLite write
// lock at BEGIN (see db.Open, _txlock=immediate). That serializes writers,
// which makes read-check-write sequences such as "compare If-Match version,
// then update" race-free without any application-level locking.
package meta

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrSetupComplete = errors.New("setup already completed")
)

// timeFormat is UTC RFC 3339 with fixed millisecond precision, so stored
// timestamps sort correctly as strings.
const timeFormat = "2006-01-02T15:04:05.000Z"

func fmtTime(t time.Time) string { return t.UTC().Format(timeFormat) }

func parseTime(s string) time.Time {
	t, err := time.Parse(timeFormat, s)
	if err != nil {
		// Fall back to full RFC 3339 for rows written by other tools.
		t, _ = time.Parse(time.RFC3339Nano, s)
	}
	return t.UTC()
}

func parseNullTime(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t := parseTime(s.String)
	return &t
}

// querier is satisfied by *sql.DB and *sql.Tx.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store is the metadata repository.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// New returns a Store. now may be nil (defaults to time.Now) and exists so
// tests can control the clock.
func New(db *sql.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now}
}

// withTx runs fn in a write transaction, committing on success.
func (s *Store) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ---- settings ----

// Setting returns a settings value ("" and ErrNotFound if unset).
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

// SetSetting inserts or replaces a settings value.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *Store) SetupComplete(ctx context.Context) (bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'setup_complete'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return v == "true", err
}

// ---- users ----

type User struct {
	ID           int64
	Username     string
	PasswordHash string
	IsAdmin      bool
	CreatedAt    time.Time
	TOTPEnabled  bool
	// E2EE is "off", "migrating" or "on" (see user_keys).
	E2EE string
}

// userColsOf selects a user's columns from table alias t.
func userColsOf(t string) string {
	return t + `.id, ` + t + `.username, ` + t + `.password_hash, ` + t + `.is_admin, ` + t + `.created_at, ` +
		t + `.totp_enabled_at IS NOT NULL, COALESCE((SELECT k.state FROM user_keys k WHERE k.user_id = ` + t + `.id), 'off')`
}

var userCols = userColsOf("users")

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var created string
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.IsAdmin, &created, &u.TOTPEnabled, &u.E2EE); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, err
	}
	u.CreatedAt = parseTime(created)
	return u, nil
}

// CompleteSetup creates the first admin and flips setup_complete in one
// transaction. Because the transaction holds the write lock from BEGIN, two
// concurrent setup requests cannot both create an admin: the second one sees
// setup_complete=true and gets ErrSetupComplete.
func (s *Store) CompleteSetup(ctx context.Context, username, passwordHash string) (User, error) {
	var u User
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var done string
		if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'setup_complete'`).Scan(&done); err != nil {
			return err
		}
		if done == "true" {
			return ErrSetupComplete
		}
		now := fmtTime(s.now())
		var id int64
		if err := tx.QueryRowContext(ctx,
			`INSERT INTO users (username, password_hash, is_admin, created_at) VALUES (?, ?, 1, ?) RETURNING id`,
			username, passwordHash, now).Scan(&id); err != nil {
			return err
		}
		var err error
		if u, err = scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id)); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE settings SET value = 'true' WHERE key = 'setup_complete'`)
		return err
	})
	return u, err
}

// ErrUserExists: the username is taken (usernames are case-insensitive).
var ErrUserExists = errors.New("a user with that name already exists")

// CreateUser adds a regular (non-admin) account. There's no sign-up: the
// administrator runs `nulldrive add-user`.
func (s *Store) CreateUser(ctx context.Context, username, passwordHash string) (User, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO users (username, password_hash, is_admin, created_at) VALUES (?, ?, 0, ?) RETURNING id`,
		username, passwordHash, fmtTime(s.now())).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return User{}, ErrUserExists
		}
		return User{}, err
	}
	return s.UserByID(ctx, id)
}

func (s *Store) UserByUsername(ctx context.Context, username string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = ?`, username))
}

func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// ---- sessions ----

// CreateSession stores a web session and opportunistically purges expired
// ones, which keeps the table small without a background job.
func (s *Store) CreateSession(ctx context.Context, userID int64, tokenHash string, ttl time.Duration) (time.Time, error) {
	now := s.now().UTC()
	expires := now.Add(ttl)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, fmtTime(now)); err != nil {
		return time.Time{}, err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (user_id, token_hash, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		userID, tokenHash, fmtTime(now), fmtTime(expires))
	return parseTime(fmtTime(expires)), err
}

// SessionUser resolves an unexpired session token hash to its user.
func (s *Store) SessionUser(ctx context.Context, tokenHash string) (User, int64, error) {
	var sessionID int64
	var u User
	var created string
	err := s.db.QueryRowContext(ctx,
		`SELECT s.id, `+userColsOf("u")+`
		   FROM sessions s JOIN users u ON u.id = s.user_id
		  WHERE s.token_hash = ? AND s.expires_at > ?`,
		tokenHash, fmtTime(s.now())).
		Scan(&sessionID, &u.ID, &u.Username, &u.PasswordHash, &u.IsAdmin, &created, &u.TOTPEnabled, &u.E2EE)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, 0, ErrNotFound
	}
	if err != nil {
		return User{}, 0, err
	}
	u.CreatedAt = parseTime(created)
	return u, sessionID, nil
}

func (s *Store) DeleteSession(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// ---- devices ----

type Device struct {
	ID            int64
	UserID        int64
	Name          string
	CreatedAt     time.Time
	LastSeenAt    *time.Time
	RevokedAt     *time.Time
	LastIP        string
	ClientVersion string
	AddedVia      string // "password" or "pairing"
	// Scope is what the token may do: ScopeFull or ScopeSearch.
	Scope string
}

// Token scopes (devices.scope).
const (
	// ScopeFull: a synced device or an API token; everything the user can do.
	ScopeFull = "full"
	// ScopeSearch: GET /search only. For Null Search, which keeps the token
	// in a browser: if it leaks, it can find files but not read, change or
	// delete them.
	ScopeSearch = "search"
)

// Ways a device can be added.
const (
	AddedViaPassword = "password"
	AddedViaPairing  = "pairing"
)

const deviceCols = `id, user_id, name, created_at, last_seen_at, revoked_at, last_ip, client_version, added_via, scope`

func scanDevice(row interface{ Scan(...any) error }) (Device, error) {
	var d Device
	var created string
	var seen, revoked, ip, ver sql.NullString
	if err := row.Scan(&d.ID, &d.UserID, &d.Name, &created, &seen, &revoked, &ip, &ver, &d.AddedVia, &d.Scope); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Device{}, ErrNotFound
		}
		return Device{}, err
	}
	d.CreatedAt = parseTime(created)
	d.LastSeenAt = parseNullTime(seen)
	d.RevokedAt = parseNullTime(revoked)
	d.LastIP, d.ClientVersion = ip.String, ver.String
	return d, nil
}

func (s *Store) CreateDevice(ctx context.Context, userID int64, name, tokenHash string) (Device, error) {
	return createDevice(ctx, s.db, userID, name, tokenHash, AddedViaPassword, ScopeFull, s.now())
}

// CreateScopedDevice creates a token limited to scope (ScopeSearch).
func (s *Store) CreateScopedDevice(ctx context.Context, userID int64, name, tokenHash, scope string) (Device, error) {
	return createDevice(ctx, s.db, userID, name, tokenHash, AddedViaPassword, scope, s.now())
}

func createDevice(ctx context.Context, q querier, userID int64, name, tokenHash, via, scope string, now time.Time) (Device, error) {
	return scanDevice(q.QueryRowContext(ctx,
		`INSERT INTO devices (user_id, name, token_hash, created_at, added_via, scope) VALUES (?, ?, ?, ?, ?, ?)
		 RETURNING `+deviceCols, userID, name, tokenHash, fmtTime(now), via, scope))
}

// DeviceByToken resolves an unrevoked device token hash to device and user.
func (s *Store) DeviceByToken(ctx context.Context, tokenHash string) (Device, User, error) {
	d, err := scanDevice(s.db.QueryRowContext(ctx,
		`SELECT `+deviceCols+` FROM devices WHERE token_hash = ? AND revoked_at IS NULL`, tokenHash))
	if err != nil {
		return Device{}, User{}, err
	}
	u, err := s.UserByID(ctx, d.UserID)
	return d, u, err
}

// TouchDevice records activity: when the device was last seen, from which
// address and with which client build. It only writes if last_seen_at is
// more than a minute old or the address/version changed, so an actively
// syncing client does not turn every read request into a database write.
func (s *Store) TouchDevice(ctx context.Context, id int64, ip, clientVersion string) error {
	now := s.now()
	_, err := s.db.ExecContext(ctx,
		`UPDATE devices SET last_seen_at = ?, last_ip = ?, client_version = COALESCE(NULLIF(?, ''), client_version)
		  WHERE id = ? AND (last_seen_at IS NULL OR last_seen_at < ?
		        OR COALESCE(last_ip, '') <> ? OR (? <> '' AND COALESCE(client_version, '') <> ?))`,
		fmtTime(now), ip, clientVersion, id, fmtTime(now.Add(-time.Minute)), ip, clientVersion, clientVersion)
	return err
}

func (s *Store) ListDevices(ctx context.Context, userID int64) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+deviceCols+` FROM devices WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RevokeDevice revokes one of the user's devices. Revoking twice is a no-op.
func (s *Store) RevokeDevice(ctx context.Context, userID, id int64) (Device, error) {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE devices SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL`,
		fmtTime(s.now()), id, userID); err != nil {
		return Device{}, err
	}
	return scanDevice(s.db.QueryRowContext(ctx,
		`SELECT `+deviceCols+` FROM devices WHERE id = ? AND user_id = ?`, id, userID))
}

// UpdatePassword replaces a user's password hash and ends every other
// session, so a password change also signs out a thief who had a session.
// keepSessionID (the session making the change) stays valid. Device tokens
// are not affected: they are revoked individually.
func (s *Store) UpdatePassword(ctx context.Context, userID int64, hash string, keepSessionID int64) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keepSessionID)
		return err
	})
}
