-- Track retention-sweep progress so connection drops mid-sweep can resume
-- without re-issuing STORE \Deleted on already-marked UIDs. The cursor stores
-- the highest UID the sweep has evaluated this run (mark+expunge'd or skipped
-- via per-chunk backed-up gating); 0 means no sweep is in flight. Reset on
-- full sweep completion and on UIDVALIDITY change.
-- Renamed to last_swept_uid in migration 0010.
ALTER TABLE imap_folders ADD COLUMN wave_a_cursor INTEGER NOT NULL DEFAULT 0;
