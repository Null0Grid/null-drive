-- Null Notes two-way sync (Phase 7). A move leaves a tombstone at the old
-- path so sync clients see the path go away; moved_to marks it as a move
-- (not a delete) and names where the file went, so a client that matches
-- files by id or note id never mistakes a rename for a deletion.
ALTER TABLE files ADD COLUMN moved_to TEXT;
