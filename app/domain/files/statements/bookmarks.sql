--| tier: standard
--| key: file_id
--| field: file_id uuid not null
--| field: directory_id uuid not null
--| field: active boolean not null
--| field: path text not null
--| field: name text not null
--| field: status text not null
--| field: size bigint
--| field: content_type text not null
--| field: created_at timestamp with time zone not null
--| field: updated_at timestamp with time zone not null
-- One unit's bookmarks, each joined to its file and with the file's full
-- path, in the order Bookmark scans them: the projection base bookmark ls
-- lists through. The unit binds as the base's own parameter, so its filter
-- reaches the bookmark table's key first.
--
-- The path is computed per row by a recursion anchored on the file's
-- directory and walking upward to the root, the row whose parent is NULL;
-- the names are joined with slashes as the walk climbs, and the root's own
-- name, /, contributes nothing, so a file in the root has the path /name.
-- The recursion is a scalar subquery correlated on the file's directory,
-- so it runs once per row the unit's filter keeps: the cost is the unit's
-- bookmark count times the depth, never the size of the tree.
--
-- Within one unit a file is bookmarked at most once, so file_id is the
-- key. created_at and updated_at are the bookmark's, not the file's.
SELECT b.file_id, f.directory_id, b.active,
  (WITH RECURSIVE up (directory_id, path) AS (
      SELECT d.parent_id, CASE WHEN d.parent_id IS NULL THEN '' ELSE '/' || d.name END
      FROM blobfs_directory d
      WHERE d.id = f.directory_id
    UNION ALL
      SELECT d.parent_id, CASE WHEN d.parent_id IS NULL THEN '' ELSE '/' || d.name END || up.path
      FROM up
      JOIN blobfs_directory d ON d.id = up.directory_id
  )
  SELECT up.path FROM up WHERE up.directory_id IS NULL) || '/' || f.name AS path,
  f.name, f.status, f.size, f.content_type, b.created_at, b.updated_at
FROM bookmark b
JOIN blobfs_file f ON f.id = b.file_id
WHERE b.unit_id = {{unit_id:uuid}}
