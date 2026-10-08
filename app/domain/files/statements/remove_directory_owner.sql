--| tier: standard
--| transaction: required
-- Removes the ownership row of a directory, if it has one. It requires a
-- transaction because the row goes in the transaction that removes the
-- directory: rmdir and the sweep remove the owner row and then the
-- directory, and a removal blobfs refuses rolls the owner row back with
-- it. No row affected means the directory had no owner, which is not an
-- error.
DELETE FROM directory_owner
WHERE directory_id = {{directory_id:uuid}}
