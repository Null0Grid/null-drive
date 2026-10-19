-- 0006_search: what cross-product search needs from the metadata DB.
-- (The search indexes themselves are separate files, one per user, under
-- <data>/indexes/; see internal/search.)

-- A stable id per file. files.id can't serve: a move tombstones the old
-- row and writes the new path into another row. uid travels with the file
-- across renames, moves and restores; a new file at a previously used path
-- gets a new one. 32 hex characters (128 random bits).
ALTER TABLE files ADD COLUMN uid TEXT;
UPDATE files SET uid = lower(hex(randomblob(16))) WHERE uid IS NULL;
CREATE UNIQUE INDEX files_uid ON files (uid);

-- Token scope: "full" (a synced device, an API token) or "search" (may
-- only call GET /search; for Null Search, which keeps it in a browser).
ALTER TABLE devices ADD COLUMN scope TEXT NOT NULL DEFAULT 'full' CHECK (scope IN ('full', 'search'));
