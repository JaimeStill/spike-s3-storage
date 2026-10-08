-- The consumer's ownership row: the auth strategy's join table at the
-- directory grain. It binds a blobfs directory to the unit that owns it,
-- and unit_id stands in for the scope's unit; only the shape of a filter on
-- it is rehearsed. A consumer writes the row for a depth-one directory and
-- checks the scope once, at that ancestor. The foreign key into
-- blobfs_directory has no cascading action, so a directory delete that
-- meets an owner row fails with a foreign-key error blobfs does not
-- classify. This migration is two statements in one transaction.
CREATE TABLE directory_owner (
  directory_id uuid NOT NULL,
  unit_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT pk_directory_owner PRIMARY KEY (directory_id),
  CONSTRAINT fk_directory_owner_directory FOREIGN KEY (directory_id) REFERENCES blobfs_directory (id)
);

CREATE INDEX ix_directory_owner_unit ON directory_owner (unit_id);
