-- 0001_init: initial schema for Null Drive Phase 1.
-- Timestamps are TEXT in UTC RFC 3339 with millisecond precision
-- (e.g. 2026-10-13T06:50:24.125Z): human-readable in the sqlite3 CLI and
-- lexicographically sortable.

CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    username      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT    NOT NULL,  -- argon2id PHC string
    is_admin      INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT    NOT NULL
);

-- Web UI sessions. Only the SHA-256 of the token is stored.
CREATE TABLE sessions (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT    NOT NULL UNIQUE,
    created_at TEXT    NOT NULL,
    expires_at TEXT    NOT NULL
);

-- Long-lived desktop/CLI client credentials. Revoked rows are kept for audit.
CREATE TABLE devices (
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT    NOT NULL,
    token_hash   TEXT    NOT NULL UNIQUE,
    created_at   TEXT    NOT NULL,
    last_seen_at TEXT,
    revoked_at   TEXT
);

-- One row per (user, path). Deletes are tombstones (deleted_at set), never
-- row removals, so clients that were offline still learn about the delete.
-- version is the global change counter value of the row's last change.
CREATE TABLE files (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    path        TEXT    NOT NULL,
    size        INTEGER NOT NULL,
    sha256      TEXT    NOT NULL,   -- key of the content-addressed blob
    version     INTEGER NOT NULL,
    modified_at TEXT    NOT NULL,
    deleted_at  TEXT,
    UNIQUE (user_id, path)
);
CREATE INDEX files_user_version ON files (user_id, version);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
INSERT INTO settings (key, value) VALUES ('setup_complete', 'false');

-- Single-row monotonic counter: every metadata change takes the next value.
CREATE TABLE change_counter (
    id    INTEGER PRIMARY KEY CHECK (id = 1),
    value INTEGER NOT NULL
);
INSERT INTO change_counter (id, value) VALUES (1, 0);
