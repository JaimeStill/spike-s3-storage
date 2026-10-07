// Package migrations is the app's own migration set: the directory_owner
// table, which binds a blobfs directory to the unit that owns it, and the
// bookmark table, which joins a unit to one of the files it may reach, with
// one active bookmark per unit. The migrations reference blobfs's tables,
// so the set runs above blobfs's set and under sqlate's default history
// table. [Migrations], the package's one export, returns the set.
//
// Table, constraint, and index names carry the workspace's prefixes (pk_,
// fk_, uq_, ix_) and never blobfs_, so an app object reads apart from one
// blobfs owns. The SQL is spike-blobfs's consumer set, copied unchanged.
package migrations
