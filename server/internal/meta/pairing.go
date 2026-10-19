package meta

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var (
	// ErrInvalidCode covers unknown, expired, cancelled and already-used
	// pairing codes alike. Telling them apart would give a guesser an
	// oracle ("this code exists but expired") for no benefit to a real
	// user, who just needs a fresh code either way.
	ErrInvalidCode = errors.New("invalid or expired pairing code")
	// ErrTooManyCodes limits outstanding codes per user.
	ErrTooManyCodes = errors.New("too many active pairing codes")
)

// Pairing statuses as reported to the web UI.
const (
	PairingPending   = "pending"
	PairingClaimed   = "claimed"
	PairingExpired   = "expired"
	PairingCancelled = "cancelled"
)

type Pairing struct {
	ID          int64
	UserID      int64
	CreatedAt   time.Time
	ExpiresAt   time.Time
	ClaimedAt   *time.Time
	CancelledAt *time.Time
	DeviceID    *int64
}

// Status derives the state at time now.
func (p Pairing) Status(now time.Time) string {
	switch {
	case p.ClaimedAt != nil:
		return PairingClaimed
	case p.CancelledAt != nil:
		return PairingCancelled
	case !now.Before(p.ExpiresAt):
		return PairingExpired
	default:
		return PairingPending
	}
}

const pairingCols = `id, user_id, created_at, expires_at, claimed_at, cancelled_at, device_id`

func scanPairing(row interface{ Scan(...any) error }) (Pairing, error) {
	var p Pairing
	var created, expires string
	var claimed, cancelled sql.NullString
	var dev sql.NullInt64
	if err := row.Scan(&p.ID, &p.UserID, &created, &expires, &claimed, &cancelled, &dev); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Pairing{}, ErrNotFound
		}
		return Pairing{}, err
	}
	p.CreatedAt, p.ExpiresAt = parseTime(created), parseTime(expires)
	p.ClaimedAt, p.CancelledAt = parseNullTime(claimed), parseNullTime(cancelled)
	if dev.Valid {
		p.DeviceID = &dev.Int64
	}
	return p, nil
}

// pairingRetention is how long finished codes are kept before pruning.
const pairingRetention = 24 * time.Hour

// CreatePairing stores a new code (by hash) valid for ttl. At most
// maxActive unexpired, unused codes may exist per user.
func (s *Store) CreatePairing(ctx context.Context, userID int64, codeHash string, ttl time.Duration, maxActive int) (Pairing, error) {
	var p Pairing
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		now := s.now()
		if _, err := tx.ExecContext(ctx, `DELETE FROM pairing_codes WHERE expires_at < ?`,
			fmtTime(now.Add(-pairingRetention))); err != nil {
			return err
		}
		var active int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pairing_codes
			  WHERE user_id = ? AND claimed_at IS NULL AND cancelled_at IS NULL AND expires_at > ?`,
			userID, fmtTime(now)).Scan(&active); err != nil {
			return err
		}
		if active >= maxActive {
			return ErrTooManyCodes
		}
		var err error
		p, err = scanPairing(tx.QueryRowContext(ctx,
			`INSERT INTO pairing_codes (user_id, code_hash, created_at, expires_at) VALUES (?, ?, ?, ?)
			 RETURNING `+pairingCols, userID, codeHash, fmtTime(now), fmtTime(now.Add(ttl))))
		return err
	})
	return p, err
}

// ClaimPairing atomically consumes a pending code and creates the device
// it pairs. The single UPDATE ... WHERE claimed_at IS NULL inside a write
// transaction is what makes a code single-use even if two clients race
// with the same code.
func (s *Store) ClaimPairing(ctx context.Context, codeHash, deviceName, deviceTokenHash string) (Device, User, error) {
	var d Device
	var u User
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		now := s.now()
		var id, userID int64
		err := tx.QueryRowContext(ctx,
			`SELECT id, user_id FROM pairing_codes
			  WHERE code_hash = ? AND claimed_at IS NULL AND cancelled_at IS NULL AND expires_at > ?`,
			codeHash, fmtTime(now)).Scan(&id, &userID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidCode
		}
		if err != nil {
			return err
		}
		d, err = createDevice(ctx, tx, userID, deviceName, deviceTokenHash, AddedViaPairing, ScopeFull, now)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE pairing_codes SET claimed_at = ?, device_id = ? WHERE id = ?`,
			fmtTime(now), d.ID, id); err != nil {
			return err
		}
		u, err = scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, userID))
		return err
	})
	return d, u, err
}

// GetPairing returns one of the user's codes (for the "waiting for the
// device…" screen).
func (s *Store) GetPairing(ctx context.Context, userID, id int64) (Pairing, error) {
	return scanPairing(s.db.QueryRowContext(ctx,
		`SELECT `+pairingCols+` FROM pairing_codes WHERE id = ? AND user_id = ?`, id, userID))
}

// CancelPairing invalidates a pending code. Cancelling a finished code is
// a no-op that returns its final state.
func (s *Store) CancelPairing(ctx context.Context, userID, id int64) (Pairing, error) {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE pairing_codes SET cancelled_at = ?
		  WHERE id = ? AND user_id = ? AND claimed_at IS NULL AND cancelled_at IS NULL`,
		fmtTime(s.now()), id, userID); err != nil {
		return Pairing{}, err
	}
	return s.GetPairing(ctx, userID, id)
}

// Now exposes the store clock so handlers derive statuses consistently.
func (s *Store) Now() time.Time { return s.now() }
