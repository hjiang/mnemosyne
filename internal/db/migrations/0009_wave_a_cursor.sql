-- Track Wave A (mass-deletion) progress so that connection drops mid-sweep
-- can resume without re-issuing STORE \Deleted on already-marked UIDs.
-- The cursor is the highest UID whose mark+expunge has durably completed.
-- Reset to 0 when Wave A finishes a full sweep or when UIDVALIDITY changes.
ALTER TABLE imap_folders ADD COLUMN wave_a_cursor INTEGER NOT NULL DEFAULT 0;
