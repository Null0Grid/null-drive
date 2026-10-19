-- 0005_security_e2ee: end-to-end encryption, two-factor authentication and
-- the audit log (Phase 7).

-- One row per account that turned end-to-end encryption on. The bundle is
-- produced by the client (shared/e2ee.Bundle as JSON): the master key
-- wrapped under the passphrase and under the recovery key. The server can
-- not open it. auth_hash is the SHA-256 of a key derived from the master
-- key; presenting that key proves possession of the master key, which is
-- required to replace the bundle or finish the migration.
CREATE TABLE user_keys (
    user_id     INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    state       TEXT    NOT NULL CHECK (state IN ('migrating', 'on')),
    bundle      TEXT    NOT NULL,
    fingerprint TEXT    NOT NULL,
    auth_hash   TEXT    NOT NULL,
    enabled_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL,
    finished_at TEXT
);

-- TOTP (RFC 6238). The secret is stored as-is: the server has to compute
-- codes to check them, so encrypting it with a key kept on the same disk
-- would only look safer. totp_pending holds a secret during enrolment
-- until the first code confirms it. totp_last_step blocks replaying a code
-- within its 30-second window.
ALTER TABLE users ADD COLUMN totp_secret     TEXT;
ALTER TABLE users ADD COLUMN totp_pending    TEXT;
ALTER TABLE users ADD COLUMN totp_enabled_at TEXT;
ALTER TABLE users ADD COLUMN totp_last_step  INTEGER NOT NULL DEFAULT 0;

-- Single-use recovery codes for a lost authenticator. Only hashes.
CREATE TABLE totp_recovery_codes (
    id        INTEGER PRIMARY KEY,
    user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash TEXT    NOT NULL,
    used_at   TEXT
);
CREATE INDEX totp_recovery_user ON totp_recovery_codes (user_id);

-- Security events for the administrator: sign-ins, devices, settings,
-- encryption changes. Never file names or contents. Pruned after 180 days.
CREATE TABLE audit_log (
    id        INTEGER PRIMARY KEY,
    at        TEXT    NOT NULL,
    event     TEXT    NOT NULL,
    user_id   INTEGER,
    username  TEXT,
    device_id INTEGER,
    ip        TEXT,
    detail    TEXT     -- JSON object of strings
);
CREATE INDEX audit_log_at ON audit_log (at);
