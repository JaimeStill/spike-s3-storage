package cli

import (
	"fmt"
	"io"
	"slices"

	"github.com/JaimeStill/spike-s3-storage/app/graph"
)

// Streams are the streams a dispatch reads from and writes to: the
// process's, or a test's buffers. [Run] takes them, prints its own output
// to Stdout and Stderr, and never reads Stdin; a running command reaches
// them through its [Invocation], which embeds them.
type Streams struct {
	// Stdin is what a command reads as standard input, such as the content
	// of a command that reads "-" as standard input. The dispatcher never
	// reads it, so a command that does not read it leaves it unconsumed.
	Stdin io.Reader

	// Stdout and Stderr are where a command and the dispatcher write.
	Stdout io.Writer
	Stderr io.Writer
}

// Invocation is what a running command receives: its positional arguments,
// the [Streams] the dispatcher was given, which it embeds so inv.Stdout
// reads as the stream itself, and the values of the graph nodes its path
// declares, read with [Invocation.Get].
type Invocation struct {
	Streams

	// Args holds the positional arguments left after flag parsing.
	Args []string

	cmd     *Command        // the leaf, which names the command in a wiring panic
	uses    []graph.Ref     // the nodes the leaf's path declares with Use
	system  *graph.System   // what the Build constructed; nil until it runs
	changed map[string]bool // the flags set on the command line
}

// Get returns the value of n, a node the command's path declares with
// [Command.Use], from the System the dispatcher built for the leaf. A
// command reads its dependencies only this way, inside Run, after the
// Build.
//
// Get panics on a wiring mistake, with a "cli:" message naming the
// command's path and the node: a nil n; a node the path does not declare,
// even one the Build reached as a declared node's dependency, since a
// command's dependencies are what it declares; and a declared node read
// before the Build, from [Command.Validate] or the root's PreRun.
func (inv *Invocation) Get[T any](n *graph.Node[T]) T {
	path := inv.cmd.path()
	if n == nil {
		panic(fmt.Sprintf("cli: %s: Get of a nil node", path))
	}
	if !slices.Contains(inv.uses, graph.Ref(n)) {
		panic(fmt.Sprintf("cli: %s: Get of node %q, which the command's path does not declare with Use", path, n.Name()))
	}
	if inv.system == nil {
		panic(fmt.Sprintf("cli: %s: Get of node %q before the Build; only Run can read a node", path, n.Name()))
	}
	return inv.system.Get(n)
}

// Changed reports whether the flag called name was set on the command
// line, even to its default value, rather than left at its default. It
// covers the command's own flags and the root flags, wherever in the
// command line a root flag was given; it is false for any other name.
func (inv *Invocation) Changed(name string) bool {
	return inv.changed[name]
}
