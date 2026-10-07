--| tier: standard
-- Whether a unit owns a directory: 1 when the directory's ownership row
-- names the unit, 0 when the directory has no owner row or another unit
-- owns it. ls --unit reads it once, for the top-level directory of the
-- listed path.
SELECT COUNT(*) AS n
FROM directory_owner o
WHERE o.directory_id = {{directory_id:uuid}} AND o.unit_id = {{unit_id:uuid}}
