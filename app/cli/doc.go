// Package cli is a command dispatcher on the standard library's flag
// package. A program builds a tree of [Command] values, each owning a
// *flag.FlagSet, and hands the root to [Run] with the process arguments and
// its [Streams], standard input, output, and error; Run walks the tree to the
// selected command, parses its flags, runs it, and returns the exit code the
// program passes to os.Exit, following go-core's process convention. Wiring
// mistakes in the tree panic when it is built or when it is dispatched, as
// each symbol's documentation states.
//
// The package exports:
//
//   - [Command], one node of a command tree, a parent or a leaf; its
//     Footer appends the command's own text to its generated help, its
//     Args counts a leaf's positional arguments, and its Validate checks
//     a leaf's input, arguments and flag values, before anything is built
//   - [Command.Add], which attaches subcommands
//   - [Command.Flags], which returns the command's flag set; flags defined
//     on the root are root flags, accepted at any depth
//   - [Command.Require], which marks a leaf's flags as required
//   - [Command.Exclusive], which declares a mutually exclusive flag group
//   - [Command.Use], which declares the graph nodes a command needs
//   - [Streams], the standard input, output, and error a dispatch uses
//   - [Invocation], what a running command receives: its arguments and
//     the Streams Run was given, which it embeds
//   - [Invocation.Get], which returns the value of a node the command's
//     path declares, and panics naming the command and the node when the
//     path does not declare it
//   - [Invocation.Changed], which reports whether a flag was given
//   - [Run], which dispatches the arguments and returns the exit code
//   - [Option], which configures one Run, and [WithGraph], the Option that
//     gives Run the graph that declared nodes are built from
//   - [StringsVar], which defines a repeatable string flag
//   - [NoArgs] and [ExactArgs], the positional-argument validators
//   - [UsageError], an error reported with the command's usage
//   - [Usagef], which returns a formatted UsageError
//
// # Dependencies
//
// A command's dependencies are part of the command: [Command.Use] declares
// the [graph.Node] values it needs, inherited along the command path, and
// [Run] builds the union a leaf's path declares from the [WithGraph] graph
// and runs the leaf under a [lifecycle.Coordinator] only once the dispatch
// reaches it, so help, a usage error, or a PreRun error builds nothing. The
// leaf reads each value with [Invocation.Get]. A leaf whose path declares
// no nodes runs with no Build and no lifecycle.
//
// # Dispatch order
//
// A dispatch to a leaf parses its flags and then runs, stopping at the
// first failure: [Command.Args], the [Command.Require] and
// [Command.Exclusive] checks, [Command.Validate], the root's
// [Command.PreRun], the Build of the declared nodes, and [Command.Run].
// Every failure before PreRun is a usage error, and all of them come
// before the Build, so input the dispatcher or Validate refuses builds
// nothing.
//
// The package imports the standard library, go-core, and the spike's graph
// and lifecycle packages.
package cli
