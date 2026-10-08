-- A bookmark joins a unit to one of the files it may reach: the org_image
-- analog at the file grain. The foreign key has no cascading action, so a
-- file delete that meets a bookmark fails with a classifiable foreign-key
-- error. The partial unique index allows one active bookmark per unit: the
-- guarded single-active-row shape. This migration is two statements in one
-- transaction.
CREATE TABLE bookmark (
  unit_id uuid NOT NULL,
  file_id uuid NOT NULL,
  active boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT pk_bookmark PRIMARY KEY (unit_id, file_id),
  CONSTRAINT fk_bookmark_file FOREIGN KEY (file_id) REFERENCES blobfs_file (id)
);

CREATE UNIQUE INDEX uq_bookmark_active ON bookmark (unit_id) WHERE active;
