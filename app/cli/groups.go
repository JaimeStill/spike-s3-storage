package cli

import (
	"fmt"
	"slices"
	"strings"
)

// Require marks the flags called names as required on the leaf c: a
// dispatch to c that does not set each of them on the command line is a
// usage error naming every one missing, in the order they were required.
// A flag counts as set when [Invocation.Changed] reports it, so one given
// explicitly at its default value satisfies the requirement. names may
// include root flags, which c inherits.
//
// The names are checked when the tree is dispatched, after root flags are
// shared, so flags may be defined before or after Require. Naming a flag c
// neither defines nor inherits, or calling Require on a parent command,
// panics then.
func (c *Command) Require(names ...string) {
	for _, name := range names {
		if !slices.Contains(c.required, name) {
			c.required = append(c.required, name)
		}
	}
}

// Exclusive declares the flags called names a mutually exclusive group on
// the leaf c: a dispatch to c that sets two or more of them on the command
// line is a usage error naming those it set, in the order given here.
// Setting one of them, or none, is accepted; combine Exclusive with
// [Command.Require] for exactly one. Set means as [Invocation.Changed]
// reports it, and names may include root flags, which c inherits. Each call
// declares a separate group, checked in the order declared.
//
// The names are checked when the tree is dispatched, as for Require. A
// group of fewer than two flags, a name c neither defines nor inherits, or
// a group on a parent command panics then.
func (c *Command) Exclusive(names ...string) {
	c.exclusive = append(c.exclusive, slices.Clone(names))
}

// checkWiring panics on a flag group or requirement c cannot honour. It
// runs once c's flag set holds the root flags it inherits.
func (c *Command) checkWiring() {
	if c.isParent() && (len(c.required) > 0 || len(c.exclusive) > 0) {
		panic(fmt.Sprintf("cli: %s: flag requirement or group set on a parent command", c.path()))
	}
	for _, name := range c.required {
		if c.Flags().Lookup(name) == nil {
			panic(fmt.Sprintf("cli: %s: Require names undefined flag --%s", c.path(), name))
		}
	}
	for _, group := range c.exclusive {
		if len(group) < 2 {
			panic(fmt.Sprintf("cli: %s: Exclusive group %s has fewer than two flags", c.path(), flagList(group)))
		}
		for _, name := range group {
			if c.Flags().Lookup(name) == nil {
				panic(fmt.Sprintf("cli: %s: Exclusive names undefined flag --%s", c.path(), name))
			}
		}
	}
}

// checkFlags returns a [UsageError] when the flags set on the command line,
// as changed holds them, break one of c's requirements or groups. Missing
// required flags are reported first, all in one error; otherwise the first
// group, in declaration order, with two or more flags set.
func (c *Command) checkFlags(changed map[string]bool) error {
	var missing []string
	for _, name := range c.required {
		if !changed[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return Usagef("required %s %s not set", plural(len(missing), "flag"), flagList(missing))
	}
	for _, group := range c.exclusive {
		var set []string
		for _, name := range group {
			if changed[name] {
				set = append(set, name)
			}
		}
		if len(set) > 1 {
			return Usagef("flags %s cannot be used together", flagList(set))
		}
	}
	return nil
}

// isRequired reports whether c requires the flag called name.
func (c *Command) isRequired(name string) bool {
	return slices.Contains(c.required, name)
}

// flagList renders names as "--a, --b".
func flagList(names []string) string {
	dashed := make([]string, len(names))
	for i, name := range names {
		dashed[i] = "--" + name
	}
	return strings.Join(dashed, ", ")
}
