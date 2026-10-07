--| tier: standard
--| transaction: required
-- Inserts the ownership row that binds a top-level directory to a unit. It
-- requires a transaction because the row is written in the transaction
-- that creates the directory: a directory mkdir created with a unit never
-- exists without its owner row. version and the timestamps take the
-- table's defaults.
INSERT INTO directory_owner (directory_id, unit_id)
VALUES ({{directory_id:uuid}}, {{unit_id:uuid}})
