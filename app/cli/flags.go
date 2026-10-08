package cli

import (
	"flag"
	"fmt"
	"strings"
)

// prepareTree readies the tree rooted at root for dispatch. It shares every
// flag defined on root's flag set into the flag set of each command below
// it, so a root flag is accepted at any depth and sets the one value the
// root defined. Only the root's flags are shared: a flag defined on a
// nested parent stays local to that level.
//
// It runs at the start of every dispatch, over the whole tree, so a wiring
// mistake panics on the first run whatever path the user takes: a command
// that defines a flag with a root flag's name, a parent with an Args or
// Validate function it would never call, a PreRun below the root, a flag
// requirement or group the command cannot honour, or a [Command.Use] on any
// command when Run has no graph, which hasGraph reports. Sharing is
// idempotent, so a tree can be dispatched more than once.
func prepareTree(root *Command, hasGraph bool) {
	var walk func(c *Command)
	walk = func(c *Command) {
		if c.Args != nil && c.isParent() {
			panic(fmt.Sprintf("cli: %s: Args set on a parent command", c.path()))
		}
		if c.Validate != nil && c.isParent() {
			panic(fmt.Sprintf("cli: %s: Validate set on a parent command", c.path()))
		}
		if c.PreRun != nil && c != root {
			panic(fmt.Sprintf("cli: %s: PreRun set below the root", c.path()))
		}
		if len(c.uses) > 0 && !hasGraph {
			panic(fmt.Sprintf("cli: %s: Use declared but Run has no WithGraph option", c.path()))
		}
		c.checkWiring()
		for _, sub := range c.children {
			sub.inherit(root.Flags())
			walk(sub)
		}
	}
	walk(root)
}

// inherit defines each flag of from on c's flag set with the same Value, so
// setting it at c's level sets it everywhere. It panics when c already
// defines a flag of that name itself.
func (c *Command) inherit(from *flag.FlagSet) {
	from.VisitAll(func(f *flag.Flag) {
		if c.inherited[f.Name] {
			return
		}
		if c.Flags().Lookup(f.Name) != nil {
			panic(fmt.Sprintf("cli: %s: flag --%s redefines a root flag", c.path(), f.Name))
		}
		c.Flags().Var(f.Value, f.Name, f.Usage)
		if c.inherited == nil {
			c.inherited = make(map[string]bool)
		}
		c.inherited[f.Name] = true
	})
}

// interleave splits a leaf's arguments into its flag arguments, in order,
// and its positional arguments, so flags may follow positionals. It
// classifies each argument by the flag package's own rules: "--" ends the
// flags and everything after it is positional; "-" and any argument not
// starting with "-" is positional; a flag without "=" whose Value is not a
// boolean takes the next argument as its value, whatever it looks like.
// Unknown and malformed flags are kept as flag arguments, so fs.Parse
// reports them as it would have.
func interleave(fs *flag.FlagSet, args []string) (flags, positional []string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return flags, append(positional, args[i+1:]...)
		case len(arg) < 2 || arg[0] != '-':
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		name := strings.TrimPrefix(arg[1:], "-")
		if strings.Contains(name, "=") {
			continue
		}
		if f := fs.Lookup(name); f != nil && !isBool(f) && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return flags, positional
}

// isBool reports whether f is a boolean flag, which takes no separate
// value argument.
func isBool(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

// StringsVar defines a repeatable string flag on fs: each occurrence
// appends its value to *p, in the order given, wherever it falls among the
// positional arguments. A value is taken whole and never split on commas.
// *p starts as a copy of value; the first occurrence replaces that default
// rather than appending to it. Help lists the flag as "--name string" with
// "(repeatable)" after its usage.
func StringsVar(fs *flag.FlagSet, p *[]string, name string, value []string, usage string) {
	*p = append([]string(nil), value...)
	fs.Var(&stringsValue{p: p}, name, usage)
}

// stringsValue is the flag.Value behind [StringsVar].
type stringsValue struct {
	p   *[]string
	set bool // whether Set has replaced the default
}

// Set appends s, first dropping the default on the flag's first occurrence.
func (v *stringsValue) Set(s string) error {
	if !v.set {
		*v.p = nil
		v.set = true
	}
	*v.p = append(*v.p, s)
	return nil
}

// String renders the values as "[a,b]", and an empty or unbound value as
// "[]", which help treats as no default.
func (v *stringsValue) String() string {
	if v == nil || v.p == nil {
		return "[]"
	}
	return "[" + strings.Join(*v.p, ",") + "]"
}
