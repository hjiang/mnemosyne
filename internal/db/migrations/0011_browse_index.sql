-- Speed up the browse-folder page. Without this index, listing one page of
-- a folder forces SQLite to materialize every message_locations row for the
-- folder, look up each message's date by PK, sort, then apply LIMIT/OFFSET --
-- O(N log N) per page load on a folder with hundreds of thousands of rows.
--
-- The index covers the (folder_id, internal_date DESC) ordering used by
-- ListByFolderPaged, letting the planner walk the index and stop after
-- LIMIT+OFFSET rows.
CREATE INDEX IF NOT EXISTS idx_locations_by_folder_date
    ON message_locations(folder_id, internal_date DESC);
