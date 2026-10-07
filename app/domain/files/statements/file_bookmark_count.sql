--| tier: standard
-- How many units bookmark one file. rm reads it in the transaction that
-- begins the file's delete, after it has held the file's row, and refuses
-- the delete while the count is not zero, so a bookmarked file's object is
-- never deleted. The hold is what makes the count reliable: bookmark add
-- holds the row before it inserts, so an add that held first has committed
-- before this reads, and one that arrives later waits and then refuses.
SELECT COUNT(*) AS n
FROM bookmark b
WHERE b.file_id = {{file_id:uuid}}
