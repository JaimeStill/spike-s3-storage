--| tier: standard
-- Removes a unit's bookmark of a file, active or not. The primary key
-- makes the pair unique, so at most one row is affected, and none affected
-- means the unit had no bookmark of the file.
DELETE FROM bookmark
WHERE unit_id = {{unit_id:uuid}} AND file_id = {{file_id:uuid}}
