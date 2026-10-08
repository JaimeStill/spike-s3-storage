// Package schema builds the schema command group, which reports, applies,
// reverts, and resets the two migration sets blobfs's database holds. It is
// a sibling of internal/app, which mounts it, and of the domain packages,
// such as domain/files, and it imports no domain package.
//
// The package has one file per role. database.go builds sqlate's
// multi-set migrator over the two sets: blobfs's set first, under its own
// history table, and the app's set last, under sqlate's default table, so
// blobfs's schema is at its head before the app's migrations reference it
// and a revert runs in reverse; beside it is revert, which reverts every
// set and keeps the history tables, which the migrator does not offer.
// commands.go builds the schema command and its status, up, down, and
// reset subcommands, each a function of the migrator's node, which run the
// migrator's own Status, Up, and Reset and the package's revert. output.go
// writes the status table.
//
// The group's boundary with the composition root is one graph node: the
// root defines the node that constructs the migrator over its database and
// passes it to [Commands], whose slice it mounts as it mounts every
// package's. The group declares that node with Use, so the dispatcher
// builds and starts the database before a verb's body runs and shuts it
// down after, and each body reads the migrator with the Invocation's Get.
// This package never reads configuration, names a driver, or imports the
// composition root.
//
// The package exports:
//
//   - [Commands], which builds the package's command surface over the
//     migrator's node, the schema command and its subcommands, as a slice
//     the root mounts with root.Add(schema.Commands(...)...)
//   - [NewMigrator], which builds the migrator over a database
package schema
