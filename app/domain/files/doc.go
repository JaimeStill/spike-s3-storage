// Package files is blobfs's file-system domain over the published blobfs
// library: the directory commands, mkdir, ls, stat, mv, and rmdir, and the
// bookmark command with add, ls, and rm, and the [Service] they run on; and
// the object commands, put, cat, cp, and rm, and the [Storage] they run on.
// It uses blobfs.Directory and blobfs.File as the library defines them and
// does not restate them.
//
// Beside blobfs's tables the domain owns two of its own, which the app's
// migration set creates. An owner row binds a top-level directory to a
// unit: mkdir --unit writes it with the directory, ls --unit checks it at
// the listed directory's top-level ancestor, whether a path or an id names
// the directory, and lists a unit's own top-level directories at the root,
// and rmdir and rm --recursive remove it with the directory. mv keeps a
// move under one top-level directory, so nothing crosses from one owner's
// scope into another's. A bookmark binds a unit to a file, at most one of a
// unit's bookmarks active; rm refuses a file a unit bookmarks, and rm
// --recursive a branch holding one, before anything is deleted.
//
// The package has one file per role:
//
//   - service.go holds the [Service], the domain's API over the database
//     alone, and its operations, composed from blobfs's methods and the
//     domain's statements: the directory operations, which write and
//     remove a directory's owner row with it, and the bookmark operations,
//     whose add holds the file, blobfs's reference-then-delete rule,
//     before it inserts. It holds the request rules, the refusals an
//     operation makes from its request alone, before any I/O, and which
//     the commands run in Validate too: a path's syntax, an id where no id
//     can name the target (a directory's create), a unit below the top
//     level, and a cursor into the owner listing. [Service.Start] prepares
//     every statement against the database.
//   - database.go holds the unexported store, the data access the Service
//     and the Storage share: the domain's pattern catalog, and blobfs's
//     statements, with whatever engine the caller passes, and the domain's
//     own, embedded from statements/, both compiled against it, and the
//     lookups the operations share. It is the only file that imports the
//     query library: it lowers a Listing to its directives and wraps each
//     of the domain's statements in a typed method.
//   - storage.go holds the [Storage], the domain's second API, over the
//     store and the object store, and the blob protocol: a put through
//     blobfs's WriteFile, or EnsureFile when it resumes a pending row; a
//     read; a copy through WriteFile; a file's delete through RemoveFile,
//     whose first transaction holds the file and refuses it while a unit
//     bookmarks it; and a branch's delete, Directories.MarkDeleting,
//     refused while a unit bookmarks a file in the branch, and then the
//     sweep run until no work remains, removing each directory's owner row
//     with it. It holds the adapter that is blobfs's ObjectStore over
//     go-storage's Store.
//   - entities.go holds the shapes the operations take and return: the
//     [Ref] that names an entry, a Listing and its terms, a Page and the
//     Contents of a listed directory, the Entry Stat finds, a move's
//     result, the Located row a removal, a read, or a bookmark reports
//     with its path, the Content a put writes, the results of a put, a
//     copy, and a branch's delete, each with its paths, and a Bookmark as
//     the bookmark listing reads it.
//   - errors.go holds the errors the domain adds to blobfs's: the
//     sentinels, worded in the domain's terms, and the [FormError] the
//     form rules return.
//   - commands.go holds the command functions, one per command over the
//     node it reads, their flag structs, and the CLI-syntax parsers: the
//     path-or-id argument, the filter and sort terms, and --unit. A command
//     that changes state prints its one-line success there, and a command
//     adds its flags' wording to the domain's refusals it reports. Every
//     command that names an existing entry takes an argument written as
//     id:<uuid> in place of a path: ls, with or without --unit, stat, mv,
//     rmdir, put's destination, cat, cp, rm, rm --recursive, and bookmark
//     add and rm. mkdir takes a path alone, since the directory it creates
//     has no id yet; mv and cp take each argument in either form, so a path
//     and an id mix. A success line names an entry by its resolved path,
//     whichever form the argument took. Each bookmark subcommand requires
//     --unit.
//   - output.go renders the listings and the rows, over package output's
//     Table and Record: ls's listing and each half's page, bookmark ls's
//     listing, and stat's file and directory records.
//
// The Service holds the database alone, never the object store, so the
// directory and bookmark commands declare only the Service's node and run
// with the store unreachable. The Storage holds the Service's data access
// and the object store, and only the object commands declare its node, so
// only they build and start the store. The boundary with the composition
// root is those two graph nodes: the root defines the node that builds the
// Service over its database and fixes blobfs's engine there, and the
// Service's own [Service.Start] is the node's start, so a files command
// against a schema that is not applied fails at start, naming the node,
// before its body runs; and the root defines the node that builds the
// Storage over the Service's node and its object store's, whose start
// creates the container and probes it, so an unreachable store fails an
// object command at start, naming the store's node. This package never
// reads configuration, names a driver or an engine, or imports the
// composition root or an admin package.
//
// The package exports:
//
//   - [Commands], which builds the domain's whole command surface over the
//     Service's and the Storage's nodes, each command declaring its own
//   - [Service], [New], and [Service.Start]; [Storage] and [NewStorage]
//   - the Service's operations: [Service.List], [Service.Stat],
//     [Service.Resolve], [Service.Mkdir], [Service.Move],
//     [Service.RemoveDirectory], [Service.AddBookmark],
//     [Service.RemoveBookmark], and [Service.ListBookmarks]
//   - the Storage's operations: [Storage.Put], [Storage.Open],
//     [Storage.Copy], [Storage.Remove], and [Storage.RemoveTree]
//   - the shapes they take and return
//   - [WriteContents] and [WriteDirectoryRecord], which write a listing
//     and a directory's row as ls and stat print them, so the scenario
//     package's tours show a result as the command does
//   - [ErrVerify], [ErrMoveAcrossScopes], [ErrNotAvailable], [ErrUnitDepth],
//     [ErrNotOwned], [ErrNoCursorAtRoot], [ErrBookmarked],
//     [ErrAlreadyBookmarked], [ErrActiveBookmark], and [ErrNoBookmark], and
//     [FormError]
package files
