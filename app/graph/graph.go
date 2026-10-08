package graph

import (
	"fmt"
	"slices"
)

// Graph describes a set of nodes and their constructors. Describing is
// inert: [Graph.Define] and [Graph.Replace] record constructors and run
// none, and only [Graph.Build] constructs anything. The first Build freezes
// the description: Define and Replace panic after it. A Graph is not safe
// for concurrent use.
type Graph struct {
	nodes []*node
	// built is set by the first Build, which freezes the description:
	// Define and Replace panic after it.
	built bool
	// observers are called, in the order added, as a Build begins each
	// node.
	observers []func(name string)
}

// New returns an empty Graph.
func New() *Graph {
	return &Graph{}
}

// erase wraps a typed constructor to return its value as any.
func erase[T any](ctor func(*Scope) (T, error)) func(*Scope) (any, error) {
	return func(s *Scope) (any, error) {
		v, err := ctor(s)
		return v, err
	}
}

// Define adds a node named name, constructed by ctor, and returns its
// handle. It runs nothing: ctor runs only when a [Graph.Build] reaches the
// node. Define panics once g has run a Build, when name is empty or already
// defined on g, or when ctor is nil.
func (g *Graph) Define[T any](name string, ctor func(*Scope) (T, error)) *Node[T] {
	if g.built {
		panic(fmt.Sprintf("graph: Define of %q after Build", name))
	}
	if name == "" {
		panic("graph: Define with an empty name")
	}
	if slices.ContainsFunc(g.nodes, func(n *node) bool { return n.name == name }) {
		panic(fmt.Sprintf("graph: Define of duplicate name %q", name))
	}
	if ctor == nil {
		panic(fmt.Sprintf("graph: Define %q with a nil constructor", name))
	}
	core := &node{graph: g, name: name, index: len(g.nodes), ctor: erase(ctor)}
	g.nodes = append(g.nodes, core)
	return &Node[T]{core: core}
}

// Replace swaps n's constructor for ctor, which every later Build runs in
// its place exactly as it would a defined one. Replace panics once g has
// run a Build, when n was defined on another Graph, or when ctor is nil.
func (g *Graph) Replace[T any](n *Node[T], ctor func(*Scope) (T, error)) {
	if g.built {
		panic(fmt.Sprintf("graph: Replace of %q after Build", n.core.name))
	}
	if n.core.graph != g {
		panic(fmt.Sprintf("graph: Replace of %q, a node defined on another Graph", n.core.name))
	}
	if ctor == nil {
		panic(fmt.Sprintf("graph: Replace of %q with a nil constructor", n.core.name))
	}
	n.core.ctor = erase(ctor)
}

// Observe adds fn, which every later Build calls with a node's name as it
// begins to construct the node, before the constructor runs: each node a
// Build reaches, once, in the order it reaches them depth-first, the one
// whose constructor fails included. It is for tracing what a Build
// constructs, such as a test asserting which nodes a run brought up; fn
// sees only the name and cannot change the Build. Observe panics on a nil
// fn.
func (g *Graph) Observe(fn func(name string)) {
	if fn == nil {
		panic("graph: Observe with a nil function")
	}
	g.observers = append(g.observers, fn)
}
