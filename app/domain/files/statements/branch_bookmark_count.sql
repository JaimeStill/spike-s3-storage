--| tier: standard
-- How many bookmarks hold files in one branch: the directory with id, the
-- directories beneath it, and the files in them. rm --recursive reads it
-- in the transaction that marks the branch deleting, after the mark, and
-- refuses the mark while the count is not zero. The mark takes each file's
-- row lock, as a file's delete does, so it waits on a bookmark add's hold,
-- and once it returns every bookmark a hold admitted has committed.
SELECT COUNT(*) AS n
FROM bookmark b
JOIN blobfs_file f ON f.id = b.file_id
WHERE f.directory_id IN (
  WITH RECURSIVE branch (id) AS (
      SELECT d.id
      FROM blobfs_directory d
      WHERE d.id = {{id:uuid}}
    UNION
      SELECT d.id
      FROM blobfs_directory d
      JOIN branch r ON d.parent_id = r.id
  )
  SELECT r.id
  FROM branch r
)
