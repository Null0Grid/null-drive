-- 0003_realtime_pairing: device details and pairing codes (Phase 5).

-- Shown on the devices page: where a device last connected from and which
-- client build it runs. Both are informational (the IP can be a proxy's if
-- NULLDRIVE_TRUST_PROXY is off) and never used for authorization.
ALTER TABLE devices ADD COLUMN last_ip TEXT;
ALTER TABLE devices ADD COLUMN client_version TEXT;
-- How the device was added: 'password' (signed in on the device) or
-- 'pairing' (claimed a pairing code issued from the web UI).
ALTER TABLE devices ADD COLUMN added_via TEXT NOT NULL DEFAULT 'password';

-- Short-lived, single-use codes for adding a device without typing the
-- account password on it. Only the SHA-256 of the normalized code is
-- stored. Rows are kept after use/expiry (small, useful for the audit
-- trail) and pruned after a day.
CREATE TABLE pairing_codes (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash   TEXT    NOT NULL UNIQUE,
    created_at  TEXT    NOT NULL,
    expires_at  TEXT    NOT NULL,
    claimed_at  TEXT,
    cancelled_at TEXT,
    device_id   INTEGER REFERENCES devices(id) ON DELETE SET NULL
);
CREATE INDEX pairing_codes_user ON pairing_codes (user_id, expires_at);

-- LAN discovery (mDNS) is on by default; the admin can turn it off in
-- Settings. NULLDRIVE_MDNS=off overrides this at the environment level.
INSERT INTO settings (key, value) VALUES ('mdns_enabled', 'true');
