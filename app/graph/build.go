package graph

import (
	"fmt"
	"slices"
	"strings"
)

// entry is one node in one Build: its state, its value once built, and the
// edges its constructor recorded.
type entry struct {
	node     *node
	building bool
	value    any
	// uses are the nodes the constructor reached through Use, deduplicated,
	// in the order it first reached them.
	uses []*node
	// after are the order-only targets the constructor named; the ones not
	// in the System are dropped when the layers are computed.
	after []*node
}

// build is the state of one [Graph.Build].
type build struct {
	graph   *Graph
	entries map[*node]*entry
	// stack is the chain of nodes under construction, outermost first,
	// which names the path of a cycle.
	stack []*node
}

// abort carries a constructor's labelled error out of the dependents whose
// Use reached it, up to Build, which recovers it and returns the error. A
// constructor that recovers it instead breaks that unwinding, which
// construct detects and panics on.
type abort struct {
	err error
}

// Build constructs every node the roots reach through [Scope.Use], each at
// most once, depth-first, and returns them as a [System] in computed
// layers. Each Build is independent and constructs fresh values. A
// constructor's error fails the Build, labelled with its node's name and
// wrapping the error, whether the node is a root or a dependency some Use
// reached. Build panics on a nil root, a root defined on another Graph, or a
// dependency cycle, among the nodes Use reaches or the ordering edges After
// adds among them. It checks every root before it constructs any, so a
// wiring mistake in one root panics before another root's constructor runs.
// It also panics when a constructor returns after a Use it made aborted,
// which only a recover in the constructor allows: a constructor must not
// recover. The first Build freezes g: [Graph.Define] and [Graph.Replace]
// panic after it.
func (g *Graph) Build(roots ...Ref) (sys *System, err error) {
	g.built = true
	b := &build{graph: g, entries: make(map[*node]*entry)}
	defer func() {
		if r := recover(); r != nil {
			a, ok := r.(abort)
			if !ok {
				panic(r)
			}
			sys, err = nil, a.err
		}
	}()
	// Resolve every root first, so a nil or foreign root anywhere in the
	// list panics before any constructor runs.
	nodes := make([]*node, len(roots))
	for i, r := range roots {
		nodes[i] = b.resolve(r, "Build")
	}
	for _, n := range nodes {
		if err := b.construct(n); err != nil {
			return nil, err
		}
	}
	return b.system(), nil
}

// resolve returns r's node, panicking when r is nil or defined on another
// Graph. op names the call for the message.
func (b *build) resolve(r Ref, op string) *node {
	var n *node
	if r != nil {
		n = r.ref()
	}
	if n == nil {
		panic(fmt.Sprintf("graph: %s of a nil node", op))
	}
	if n.graph != b.graph {
		panic(fmt.Sprintf("graph: %s of %q, a node defined on another Graph", op, n.name))
	}
	return n
}

// construct builds n unless it is built already, and returns its
// constructor's labelled error. Reaching a node under construction is a
// cycle, and panics with its path. A constructor that returns, with a value
// or an error, after a Use it made aborted has recovered the abort, and
// construct panics naming it, since the failure that aborted the Use would
// otherwise be lost.
func (b *build) construct(n *node) error {
	if e, ok := b.entries[n]; ok {
		if e.building {
			panic("graph: dependency cycle: " + cyclePath(b.stack, n))
		}
		return nil
	}
	e := &entry{node: n, building: true}
	b.entries[n] = e
	b.stack = append(b.stack, n)
	for _, observe := range b.graph.observers {
		observe(n.name)
	}
	s := &Scope{build: b, entry: e}
	defer func() {
		s.done = true
		b.stack = b.stack[:len(b.stack)-1]
	}()
	v, err := n.ctor(s)
	if s.aborted != "" {
		panic(s.recovered())
	}
	if err != nil {
		return fmt.Errorf("%s: %w", n.name, err)
	}
	e.value = v
	e.building = false
	return nil
}

// cyclePath names the cycle that reaching n again closes, from n's place in
// path, the chain of nodes being visited outermost first, back to n.
func cyclePath(path []*node, n *node) string {
	names := make([]string, 0, len(path)+1)
	for _, m := range path[max(slices.Index(path, n), 0):] {
		names = append(names, m.name)
	}
	return strings.Join(append(names, n.name), " -> ")
}
