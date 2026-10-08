--| tier: standard
-- Inserts a unit's bookmark of a file, active or not. The primary key
-- pk_bookmark refuses a second bookmark of the same file by the same unit,
-- the partial unique index uq_bookmark_active a second active bookmark for
-- the unit, and the foreign key fk_bookmark_file a file that does not
-- exist; database.go maps each name to its sentinel. The timestamps take
-- the table's defaults. bookmark add runs it in the transaction that
-- resolved and held the file, blobfs's reference-then-delete rule, so no
-- delete of the file begins before the bookmark commits.
INSERT INTO bookmark (unit_id, file_id, active)
VALUES ({{unit_id:uuid}}, {{file_id:uuid}}, {{active:boolean}})
