package graph

import (
	"fmt"
	"slices"
)

// Scope is what a constructor receives: the means to reach its
// dependencies and to order it after other nodes. It is valid only while the
// constructor runs; every method panics once the constructor has returned.
type Scope struct {
	build *build
	entry *entry
	done  bool
	// aborted names the node whose Use did not return, set as the abort
	// unwinds through Use, whether the failure was that node's or one
	// further down; construct panics when the constructor returns anyway.
	aborted string
}

// check panics when the constructor that received s has returned.
func (s *Scope) check(op string) {
	if s.done {
		panic(fmt.Sprintf("graph: %s called after the constructor of %q returned", op, s.entry.node.name))
	}
}

// Use returns n's value, building n first when this Build has not, and
// records that the node under construction depends on n. When n's
// constructor fails, or a dependency's does as n is built, Use does not
// return: it aborts the calling constructor by panicking, and Build
// recovers the panic and reports the failing node's error. A constructor
// must not recover that panic: when one returns after a Use it made
// aborted, Build panics naming it. Use panics when n was defined on another
// Graph, when n is under construction (a cycle, named by its path), after a
// Use in the same constructor aborted, or after the constructor has
// returned.
func (s *Scope) Use[T any](n *Node[T]) T {
	s.check("Use")
	if s.aborted != "" {
		panic(s.recovered())
	}
	core := s.build.resolve(n, "Use")
	// Any panic leaving construct, the abort above all, marks the scope,
	// so construct can tell a constructor that returned after recovering
	// it.
	returned := false
	defer func() {
		if !returned {
			s.aborted = core.name
		}
	}()
	if err := s.build.construct(core); err != nil {
		panic(abort{err: err})
	}
	returned = true
	if !slices.Contains(s.entry.uses, core) {
		s.entry.uses = append(s.entry.uses, core)
	}
	// The comma-ok form returns the zero T for a nil value of an interface
	// type, which a plain assertion would panic on.
	v, _ := s.build.entries[core].value.(T)
	return v
}

// After orders the node under construction after r: it starts after r and
// stops before it. It passes no value and never builds r; the edge holds
// only when r is in the System, as a root or a node some Use reached, and
// is dropped otherwise. After panics when r is nil or was defined on
// another Graph, or after the constructor has returned.
func (s *Scope) After(r Ref) {
	s.check("After")
	core := s.build.resolve(r, "After")
	if !slices.Contains(s.entry.after, core) {
		s.entry.after = append(s.entry.after, core)
	}
}

// recovered is the panic message for a constructor that carried on after a
// Use it made aborted, which only a recover in the constructor allows.
func (s *Scope) recovered() string {
	return fmt.Sprintf(
		"graph: the constructor of %q carried on after its Use of %q aborted; a constructor must not recover",
		s.entry.node.name, s.aborted,
	)
}
