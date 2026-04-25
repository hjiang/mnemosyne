-- Rename wave_a_cursor to last_swept_uid for readability. The column tracks
-- the retention sweep's progress through the folder's UID space; the new name
-- mirrors last_seen_uid (the fetch-progress cursor) and drops the internal
-- "Wave A" jargon.
ALTER TABLE imap_folders RENAME COLUMN wave_a_cursor TO last_swept_uid;
