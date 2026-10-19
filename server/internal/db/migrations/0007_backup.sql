-- Phase 6 (Null Notes backup into Null Drive).
--
-- mtime: the file's modification time on the device that uploaded it
-- (optional, sent by the client), so a backup tool can compare times
-- without hashing. modified_at stays the server's time of the change.
-- synced_id: the Null Notes note id from a Markdown file's frontmatter
-- (`id: <uuid>`), extracted by the server on upload.
-- Both live on the version too, so a restore brings them back.
ALTER TABLE files ADD COLUMN mtime TEXT;
ALTER TABLE files ADD COLUMN synced_id TEXT;
ALTER TABLE file_versions ADD COLUMN mtime TEXT;
ALTER TABLE file_versions ADD COLUMN synced_id TEXT;
