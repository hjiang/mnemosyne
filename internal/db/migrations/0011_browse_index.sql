-- Speed up the browse-folder page. Without this index, listing one page of
-- a folder forces SQLite to materialize every message_locations row for the
-- folder, look up each message's date by PK, sort, then apply LIMIT/OFFSET --
-- O(N log N) per page load on a folder with hundreds of thousands of rows.
--
-- The composite key matches ListByFolderPaged's
-- (ORDER BY ml.internal_date DESC, ml.uid DESC) so the planner walks the
-- index and stops after LIMIT+OFFSET rows. The uid tie-breaker keeps
-- LIMIT/OFFSET pagination stable when many messages share the same
-- INTERNALDATE second (mailing list digests, post-outage delivery flushes).
CREATE INDEX IF NOT EXISTS idx_locations_by_folder_date
    ON message_locations(folder_id, internal_date DESC, uid DESC);
