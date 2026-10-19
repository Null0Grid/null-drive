package meta

import (
	"context"
	"database/sql"
	"errors"
)

// Two-factor authentication storage (Phase 7). The protocol lives in
// auth/totp.go; this is the bookkeeping.

// TOTPState is a user's two-factor configuration.
type TOTPState struct {
	Secret   string // set once enabled
	Pending  string // set during enrolment
	LastStep int64
}

// TOTP returns the user's two-factor state.
func (s *Store) TOTP(ctx context.Context, userID int64) (TOTPState, error) {
	var st TOTPState
	var secret, pending sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT totp_secret, totp_pending, totp_last_step FROM users WHERE id = ?`, userID).
		Scan(&secret, &pending, &st.LastStep)
	if errors.Is(err, sql.ErrNoRows) {
		return st, ErrNotFound
	}
	st.Secret, st.Pending = secret.String, pending.String
	return st, err
}

// SetTOTPPending stores a secret awaiting confirmation (replacing any
// earlier unconfirmed one). An enabled secret is untouched.
func (s *Store) SetTOTPPending(ctx context.Context, userID int64, secret string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET totp_pending = ? WHERE id = ?`, secret, userID)
	return err
}

// EnableTOTP promotes the pending secret, records the step of the code
// that confirmed it (so that code can't be reused to log in) and replaces
// the recovery codes.
func (s *Store) EnableTOTP(ctx context.Context, userID int64, step int64, recoveryHashes []string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE users SET totp_secret = totp_pending, totp_pending = NULL, totp_enabled_at = ?, totp_last_step = ?
			  WHERE id = ? AND totp_pending IS NOT NULL`, fmtTime(s.now()), step, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM totp_recovery_codes WHERE user_id = ?`, userID); err != nil {
			return err
		}
		for _, h := range recoveryHashes {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO totp_recovery_codes (user_id, code_hash) VALUES (?, ?)`, userID, h); err != nil {
				return err
			}
		}
		return nil
	})
}

// DisableTOTP turns two-factor authentication off.
func (s *Store) DisableTOTP(ctx context.Context, userID int64) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET totp_secret = NULL, totp_pending = NULL, totp_enabled_at = NULL, totp_last_step = 0 WHERE id = ?`,
			userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM totp_recovery_codes WHERE user_id = ?`, userID)
		return err
	})
}

// UseTOTPStep records a successfully used step, failing if a concurrent
// request already used this or a later one (replay protection holds even
// for two simultaneous logins with the same code).
func (s *Store) UseTOTPStep(ctx context.Context, userID, step int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_last_step = ? WHERE id = ? AND totp_last_step < ?`, step, userID, step)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// UseRecoveryCode marks an unused recovery code as used.
func (s *Store) UseRecoveryCode(ctx context.Context, userID int64, codeHash string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE totp_recovery_codes SET used_at = ? WHERE user_id = ? AND code_hash = ? AND used_at IS NULL`,
		fmtTime(s.now()), userID, codeHash)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// RecoveryCodesLeft counts unused recovery codes.
func (s *Store) RecoveryCodesLeft(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM totp_recovery_codes WHERE user_id = ? AND used_at IS NULL`, userID).Scan(&n)
	return n, err
}
