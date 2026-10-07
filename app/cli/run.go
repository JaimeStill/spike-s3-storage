package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/JaimeStill/spike-s3-storage/app/graph"
	"github.com/JaimeStill/spike-s3-storage/app/lifecycle"
	"github.com/standards-lab/go-core/process"
)

// Option configures one [Run].
type Option func(*options)

// options is what Run's Options set.
type options struct {
	graph           *graph.Graph
	lifecycleConfig *graph.Node[lifecycle.Config]
}

// WithGraph gives Run the graph that the nodes a leaf's path declares with
// [Command.Use] are built from, and the node whose value configures the
// [lifecycle.Coordinator] that runs what was built. That node is added to
// every Build, so its constructor supplies the shutdown timeout, and it
// must return a finalized Config: lifecycle.New panics on one that is not.
// WithGraph panics on a nil g or lifecycleConfig.
func WithGraph(g *graph.Graph, lifecycleConfig *graph.Node[lifecycle.Config]) Option {
	if g == nil || lifecycleConfig == nil {
		panic("cli: WithGraph with a nil graph or lifecycle config node")
	}
	return func(o *options) {
		o.graph = g
		o.lifecycleConfig = lifecycleConfig
	}
}

// Run dispatches args, the program arguments without the program name, over
// the tree rooted at root, and returns the process exit code. streams are
// the process's streams, or a test's buffers: a running command reads Stdin
// and writes Stdout and Stderr through its [Invocation], which embeds them,
// and the dispatcher prints to Stdout and Stderr itself but never reads
// Stdin.
//
// A dispatch to a leaf goes in this order, stopping at the first failure:
//
//  1. parse each level's flags and select the leaf
//  2. count its positional arguments with [Command.Args]
//  3. check its required flags, [Command.Require]
//  4. check its exclusive groups, [Command.Exclusive]
//  5. validate its input with [Command.Validate]
//  6. run the root's [Command.PreRun]
//  7. build the nodes its path declares with [Command.Use]
//  8. run the leaf with [Command.Run]
//
// When the leaf's path has nodes declared with Use, the Build constructs
// their union and the lifecycle configuration node from the [WithGraph]
// graph; the System is started with a [lifecycle.Coordinator], the leaf
// runs under it, reading each declared node's value with
// [Invocation.Get], and the System is shut down. Nothing is built when the
// dispatch ends before the Build.
//
// Run owns every line the dispatcher prints, and returns go-core's process
// exit codes:
//
//   - a parent command run with no subcommand, or any command given -h or
//     --help, prints its generated help to stdout and returns ExitUsage
//   - an unknown subcommand, an unknown flag, a malformed flag value, a
//     rejected argument count, a missing required flag, a broken exclusive
//     group, any error Validate returns, or a [UsageError] that PreRun or
//     the command returns is reported on stderr with the command's usage
//     and returns ExitUsage
//   - any other error PreRun or the command returns, or a Build, start, or
//     shutdown error, is reported once on stderr and returns ExitFailure
//   - a command that succeeds returns ExitOK
//
// Run panics at the start of the dispatch when any command in the tree has
// declared [Command.Use] and no [WithGraph] option was given, whichever
// command is selected.
func Run(ctx context.Context, root *Command, args []string, streams Streams, opts ...Option) int {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	prepareTree(root, o.graph != nil)
	cmd := root
	var path []*Command
	for {
		path = append(path, cmd)
		rest, code, ok := parse(cmd, args, streams)
		if !ok {
			return code
		}
		if !cmd.isParent() {
			return execute(ctx, &o, path, rest, streams)
		}
		if len(rest) == 0 {
			return process.Usage(streams.Stdout, help(cmd))
		}
		sub := cmd.child(rest[0])
		if sub == nil {
			msg := fmt.Sprintf("%s: unknown command %q\n\n%s", cmd.path(), rest[0], help(cmd))
			return process.Usage(streams.Stderr, msg)
		}
		cmd, args = sub, rest[1:]
	}
}

// parse parses args against cmd's flags and returns the positional
// arguments left. A parent's flags precede its subcommand, so parsing stops
// at the first non-flag argument, which names it; a leaf's flags may come
// anywhere among its positional arguments, up to a "--". When parsing ends
// the dispatch, because help was asked for or a flag was wrong, it reports
// that and returns ok false with the exit code.
func parse(cmd *Command, args []string, streams Streams) (rest []string, code int, ok bool) {
	fs := cmd.Flags()
	var positional []string
	if !cmd.isParent() {
		args, positional = interleave(fs, args)
	}
	err := fs.Parse(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return nil, process.Usage(streams.Stdout, help(cmd)), false
	case err != nil:
		return nil, usageError(cmd, streams.Stderr, err), false
	}
	if cmd.isParent() {
		return fs.Args(), process.ExitOK, true
	}
	// interleave left only flags and their values, so fs.Args() is empty.
	return positional, process.ExitOK, true
}

// execute checks the leaf's positional argument count, its flag groups,
// and its input with Validate, runs the root's PreRun and then the leaf,
// under a lifecycle when the path declares nodes with Use, and maps the
// first error to an exit code. path holds the commands from the root to
// the leaf.
func execute(ctx context.Context, o *options, path []*Command, args []string, streams Streams) int {
	root, cmd := path[0], path[len(path)-1]
	if cmd.Args != nil {
		if err := cmd.Args(args); err != nil {
			return usageError(cmd, streams.Stderr, err)
		}
	}
	inv := &Invocation{Streams: streams, Args: args, cmd: cmd, uses: uses(path), changed: changed(path)}
	if err := cmd.checkFlags(inv.changed); err != nil {
		return usageError(cmd, streams.Stderr, err)
	}
	if cmd.Validate != nil {
		// Validate's error is a usage error whatever its type.
		if err := cmd.Validate(inv); err != nil {
			return usageError(cmd, streams.Stderr, err)
		}
	}
	var err error
	if root.PreRun != nil {
		err = root.PreRun(ctx, inv)
	}
	if err == nil {
		err = o.run(ctx, cmd, inv)
	}
	if err == nil {
		return process.ExitOK
	}
	if _, ok := errors.AsType[*UsageError](err); ok {
		return usageError(cmd, streams.Stderr, err)
	}
	return process.Fail(streams.Stderr, cmd.path(), err)
}

// run runs the leaf cmd with inv. When inv's path declares no nodes it
// calls Run directly; otherwise it builds them and the lifecycle
// configuration node, and runs cmd under a Coordinator for the System,
// with the System set on inv for [Invocation.Get]. The error is the
// Build's, or the Coordinator's: startup's or the leaf's, joined with the
// shutdown's.
func (o *options) run(ctx context.Context, cmd *Command, inv *Invocation) error {
	if len(inv.uses) == 0 {
		return cmd.Run(ctx, inv)
	}
	sys, err := o.graph.Build(append(slices.Clone(inv.uses), o.lifecycleConfig)...)
	if err != nil {
		return err
	}
	return lifecycle.New(sys, sys.Get(o.lifecycleConfig)).Exec(ctx, func(ctx context.Context) error {
		inv.system = sys
		return cmd.Run(ctx, inv)
	})
}

// uses returns the union of the nodes declared with Use along path, root
// first, each node once in the order first declared.
func uses(path []*Command) []graph.Ref {
	var refs []graph.Ref
	for _, cmd := range path {
		for _, r := range cmd.uses {
			if !slices.Contains(refs, r) {
				refs = append(refs, r)
			}
		}
	}
	return refs
}

// changed returns the names of the flags set on the command line that the
// leaf at the end of path can see: its own, and the root flags, given at
// whichever level they were parsed. A nested parent's local flags are left
// out, so a leaf flag of the same name is not shadowed.
func changed(path []*Command) map[string]bool {
	root, leaf := path[0], path[len(path)-1]
	set := make(map[string]bool)
	for _, cmd := range path {
		cmd.Flags().Visit(func(f *flag.Flag) {
			if cmd == leaf || root.Flags().Lookup(f.Name) != nil {
				set[f.Name] = true
			}
		})
	}
	return set
}

// usageError reports err with cmd's short usage on stderr and returns
// ExitUsage.
func usageError(cmd *Command, stderr io.Writer, err error) int {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %v\n", cmd.path(), err)
	fmt.Fprintf(&b, "Usage: %s\n", usageLine(cmd))
	fmt.Fprintf(&b, "Run '%s --help' for details.", cmd.path())
	return process.Usage(stderr, b.String())
}
