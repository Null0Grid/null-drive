-- 0004_chunks_versions: content-defined chunks, file versions, retention (Phase 6).
--
-- Model:
--   chunks          one row per stored chunk (content-addressed, the blob
--                   file at blobs/ab/cd/<hash>), with a reference count.
--   file_versions   every content a path has had: the history.
--   version_chunks  a version's manifest: its ordered chunk list.
--   files           unchanged role (current state + change feed), plus
--                   version_id pointing at the current content. Tombstones
--                   keep version_id so a deleted file can be restored.
--   chunk_uploads   chunks a user uploaded that are not committed yet: what
--                   lets that user reference them (and resume an upload).

CREATE TABLE chunks (
    hash       TEXT    PRIMARY KEY,          -- lowercase hex SHA-256 of the chunk
    size       INTEGER NOT NULL,
    refcount   INTEGER NOT NULL DEFAULT 0,   -- number of version_chunks rows
    created_at TEXT    NOT NULL,
    touched_at TEXT    NOT NULL              -- last upload/check; the GC grace period starts here
) WITHOUT ROWID;
CREATE INDEX chunks_gc ON chunks (refcount, touched_at);

CREATE TABLE file_versions (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    path       TEXT    NOT NULL,             -- follows renames (see MoveFile)
    version    INTEGER NOT NULL,             -- change counter value that created it
    size       INTEGER NOT NULL,
    sha256     TEXT    NOT NULL,             -- whole-file hash
    device_id  INTEGER REFERENCES devices(id) ON DELETE SET NULL,  -- NULL = web/API session
    origin     TEXT    NOT NULL DEFAULT 'upload',  -- upload | restore | migrated
    created_at TEXT    NOT NULL
);
CREATE INDEX file_versions_path ON file_versions (user_id, path, version);

CREATE TABLE version_chunks (
    version_id INTEGER NOT NULL REFERENCES file_versions(id),
    seq        INTEGER NOT NULL,
    hash       TEXT    NOT NULL REFERENCES chunks(hash),
    size       INTEGER NOT NULL,
    PRIMARY KEY (version_id, seq)
) WITHOUT ROWID;
CREATE INDEX version_chunks_hash ON version_chunks (hash);

CREATE TABLE chunk_uploads (
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    hash        TEXT    NOT NULL,
    uploaded_at TEXT    NOT NULL,
    PRIMARY KEY (user_id, hash)
) WITHOUT ROWID;

ALTER TABLE files ADD COLUMN version_id INTEGER REFERENCES file_versions(id);

-- ---- backfill: every existing blob becomes a one-chunk version ----------
-- The blob store is already content-addressed by SHA-256, which is exactly
-- a chunk's storage key, so no data moves: a whole Phase 1-5 file is simply
-- a manifest with one (possibly large) chunk. New uploads are chunked.

INSERT INTO chunks (hash, size, created_at, touched_at)
SELECT sha256, MAX(size), MIN(modified_at), MIN(modified_at)
  FROM files WHERE is_dir = 0 AND sha256 <> '' AND size > 0
 GROUP BY sha256;

INSERT INTO file_versions (user_id, path, version, size, sha256, origin, created_at)
SELECT user_id, path, version, size, sha256, 'migrated', modified_at
  FROM files WHERE is_dir = 0 AND sha256 <> '';

INSERT INTO version_chunks (version_id, seq, hash, size)
SELECT id, 0, sha256, size FROM file_versions WHERE size > 0;

UPDATE files SET version_id = (
    SELECT fv.id FROM file_versions fv
     WHERE fv.user_id = files.user_id AND fv.path = files.path AND fv.version = files.version)
 WHERE is_dir = 0 AND sha256 <> '';

UPDATE chunks SET refcount = (SELECT COUNT(*) FROM version_chunks vc WHERE vc.hash = chunks.hash);

-- ---- reference counting --------------------------------------------------
-- Maintained by triggers so no code path can forget it. Every insert or
-- delete of a manifest entry adjusts its chunk's count in the same
-- transaction; GC only ever deletes chunks at zero.
CREATE TRIGGER version_chunks_ref AFTER INSERT ON version_chunks BEGIN
    UPDATE chunks SET refcount = refcount + 1 WHERE hash = NEW.hash;
END;
CREATE TRIGGER version_chunks_unref AFTER DELETE ON version_chunks BEGIN
    UPDATE chunks SET refcount = refcount - 1 WHERE hash = OLD.hash;
END;

-- Retention defaults: keep every version for 30 days, then one per day
-- for 90 more days. Deleted files stay in the bin for 30 + 90 days.
INSERT INTO settings (key, value) VALUES ('retention_full_days', '30');
INSERT INTO settings (key, value) VALUES ('retention_daily_days', '90');
