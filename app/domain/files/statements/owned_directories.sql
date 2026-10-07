--| tier: standard
--| key: name
--| field: id uuid not null
--| field: parent_id uuid
--| field: name text not null
--| field: status text not null
--| field: version bigint not null
--| field: created_at timestamp with time zone not null
--| field: updated_at timestamp with time zone not null
-- The directories one unit owns, in the order blobfs.Directory scans them:
-- the projection base ls / --unit lists through. The unit binds as the
-- base's own parameter, so its filter runs inside the base, on the owner
-- table's unit index. Owner rows bind top-level directories only, so every
-- row's parent is the root and name is the key. A deleting directory is
-- left out, as blobfs's own listing leaves it out of ls /.
SELECT {{> blobfs.directory_columns}}
FROM blobfs_directory d
JOIN directory_owner o ON o.directory_id = d.id
WHERE o.unit_id = {{unit_id:uuid}} AND d.status <> 'deleting'
