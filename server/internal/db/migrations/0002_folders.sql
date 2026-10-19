-- 0002_folders: explicit folder rows.
--
-- Phase 1 folders were implicit (a folder existed while some file path
-- started with it). That cannot represent an empty folder, so the web UI's
-- "New folder" and, later, syncing empty folders were impossible. Folders
-- are now rows in `files` with is_dir = 1 (size 0, sha256 '').
--
-- Invariant from now on: every live file's ancestor folders exist as live
-- folder rows. The server creates missing parents on upload, folder
-- creation and move; the backfill below establishes it for existing data.

ALTER TABLE files ADD COLUMN is_dir INTEGER NOT NULL DEFAULT 0;

-- One version for the whole backfill (harmless if nothing is inserted:
-- versions are opaque cursors and gaps are allowed).
UPDATE change_counter SET value = value + 1 WHERE id = 1;

-- Walk every live file path segment by segment and collect each prefix,
-- e.g. "a/b/c.txt" yields "a" and "a/b".
WITH RECURSIVE prefixes(user_id, done, rest) AS (
    SELECT user_id, '', path FROM files WHERE deleted_at IS NULL
    UNION
    SELECT user_id,
           CASE WHEN done = '' THEN '' ELSE done || '/' END || substr(rest, 1, instr(rest, '/') - 1),
           substr(rest, instr(rest, '/') + 1)
      FROM prefixes
     WHERE instr(rest, '/') > 0
)
INSERT INTO files (user_id, path, size, sha256, version, modified_at, deleted_at, is_dir)
SELECT DISTINCT user_id, done, 0, '',
       (SELECT value FROM change_counter WHERE id = 1),
       strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), NULL, 1
  FROM prefixes
 WHERE done <> ''
ON CONFLICT (user_id, path) DO UPDATE SET
    is_dir = 1, size = 0, sha256 = '', deleted_at = NULL,
    version = excluded.version, modified_at = excluded.modified_at
 WHERE files.deleted_at IS NOT NULL;
