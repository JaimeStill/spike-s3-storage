// Package app is blobfs's composition root. [New] describes blobfs's
// dependencies as one graph.Graph and builds the command tree on package
// cli over it; [App.Run] dispatches the process arguments over that tree.
//
// [Nodes] is the single description of the graph: one value, which each
// layer file's define function fills its own part of, and whose fields
// its constructors read the lower layers' nodes from. infrastructure.go
// defines the configuration nodes, each finalized under the BLOBFS
// prefix, the database and object store nodes built from them, and the
// sql node, the database's pool in sqlate's Postgres dialect. admin.go
// defines the migrator node, built on the sql node, and mounts the schema
// group over it. domain.go defines the files node, the files domain's
// Service built on the sql node with blobfs's Postgres engine, which the
// root fixes there, and whose start is the Service's statement check; and
// the storage node, the files domain's Storage built on the files node and
// the object store; and it mounts the domain's commands at the root with
// one call, each command declaring its own node. scenario.go mounts the
// scenario parent over the files and storage nodes, with a leaf per tour;
// the parent's help and the root's both end with the listing of each
// scenario and the nodes it declares, through their footers; the root's
// footer opens with the line that explains the id:<uuid> operand. Every
// package's Commands returns a slice, so each layer file's mount function
// is one root.Add(pkg.Commands(...)...) over the nodes it passes. Defining
// the nodes constructs nothing. A command declares the nodes it needs with
// Use: the schema group declares the migrator, and its verbs inherit it,
// so a schema verb builds the migrator, the sql node, the database, and
// their configuration, and never the object store; each directory command,
// mkdir, ls, stat, mv, and rmdir, and each bookmark command declares the
// files node, and likewise never builds the object store; each object
// command, put, cat, cp, and rm, declares the storage node, so it builds
// the database and the object store, and a store that cannot be reached
// fails it at start, naming the store's node, with the database shut down.
// Each tour declares the nodes it reads: scenario directories the files
// node, so it never builds the object store, and scenario files the files
// and storage nodes. version and the scenario parent declare none. [New]
// takes the process's standard input beside its output and error streams,
// and a command reads it through its Invocation, as put - does. The
// dispatcher builds the nodes the leaf's path declares only when the leaf
// runs, starts what was built layer by layer, and shuts it down in reverse
// when the leaf returns; a run that builds nothing, such as help, a usage
// error, or version, reads no configuration.
//
// The App publishes its composition: [App.Graph], [App.Nodes], and
// [App.Root] return the graph, a handle on each of its nodes, and the
// command tree. The program itself never calls them; they are for a
// caller, internal/apptest's fixtures among them, that observes what a
// run builds, Replaces a node's constructor with a substitute, or adds a
// command over the nodes, before the App runs.
//
// The package exports:
//
//   - [App], the blobfs program, and [New], which describes its graph and
//     builds its command tree
//   - [App.Run], which dispatches the process arguments and returns the
//     exit code
//   - [App.Graph], which returns the graph the commands are built from
//   - [Nodes], the graph's description, a handle on each of its nodes,
//     and [App.Nodes], which returns it
//   - [App.Root], which returns the root command
package app
