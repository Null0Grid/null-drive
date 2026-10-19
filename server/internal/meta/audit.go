package meta

import (
	"context"
	"encoding/json"
	"time"
)

// Audit events. The log records security-relevant actions for the
// administrator. It never contains file names or contents (on an
// end-to-end encrypted account the server doesn't know them anyway).
const (
	AuditSetup            = "setup_completed"
	AuditLogin            = "login"
	AuditLoginFailed      = "login_failed"
	AuditLoginLocked      = "login_locked"
	AuditLogout           = "logout"
	AuditPasswordChanged  = "password_changed"
	AuditUserAdded        = "user_added"
	AuditTOTPEnabled      = "totp_enabled"
	AuditTOTPDisabled     = "totp_disabled"
	AuditTOTPRecoveryUsed = "totp_recovery_code_used"
	AuditDeviceAdded      = "device_added"
	AuditDeviceRevoked    = "device_revoked"
	AuditPairingCreated   = "pairing_created"
	AuditPairingFailed    = "pairing_claim_failed"
	AuditSettingsChanged  = "settings_changed"
	AuditE2EEEnabled      = "e2ee_enabled"
	AuditE2EEFinished     = "e2ee_finished"
	AuditE2EEKeysChanged  = "e2ee_keys_changed"
	AuditGC               = "gc_run"
)

// AuditEvent is one audit_log row.
type AuditEvent struct {
	ID       int64
	At       time.Time
	Event    string
	UserID   int64
	Username string
	DeviceID int64
	IP       string
	Detail   map[string]string
}

// AddAudit appends an event.
func (s *Store) AddAudit(ctx context.Context, e AuditEvent) error {
	var detail any
	if len(e.Detail) > 0 {
		b, err := json.Marshal(e.Detail)
		if err != nil {
			return err
		}
		detail = string(b)
	}
	at := e.At
	if at.IsZero() {
		at = s.now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_log (at, event, user_id, username, device_id, ip, detail) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		fmtTime(at), e.Event, nullID(e.UserID), nullStr(e.Username), nullID(e.DeviceID), nullStr(e.IP), detail)
	return err
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ListAudit returns events newest first, with id < before (0 = from the
// newest), at most limit.
func (s *Store) ListAudit(ctx context.Context, before int64, limit int) ([]AuditEvent, error) {
	if before <= 0 {
		before = 1<<63 - 1
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, at, event, COALESCE(user_id, 0), COALESCE(username, ''), COALESCE(device_id, 0), COALESCE(ip, ''), COALESCE(detail, '')
		   FROM audit_log WHERE id < ? ORDER BY id DESC LIMIT ?`, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEvent{}
	for rows.Next() {
		var e AuditEvent
		var at, detail string
		if err := rows.Scan(&e.ID, &at, &e.Event, &e.UserID, &e.Username, &e.DeviceID, &e.IP, &detail); err != nil {
			return nil, err
		}
		e.At = parseTime(at)
		if detail != "" {
			_ = json.Unmarshal([]byte(detail), &e.Detail)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneAudit deletes events older than maxAge.
func (s *Store) PruneAudit(ctx context.Context, maxAge time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM audit_log WHERE at < ?`, fmtTime(s.now().Add(-maxAge)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
