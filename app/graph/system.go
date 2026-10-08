package graph

import (
	"cmp"
	"fmt"
	"slices"
)

// System is what one [Graph.Build] constructed: every node the roots
// reached, with its value, in computed layers.
type System struct {
	entries map[*node]*entry
	layers  [][]*entry
}

// Dependency is one built node: its name and its constructed value. A
// lifecycle reads what the node takes part in from the value's methods.
type Dependency struct {
	Name  string
	Value any
}

// Get returns n's value in s. It panics when n is not in s: a node the
// roots did not reach, or one defined on another Graph.
func (s *System) Get[T any](n *Node[T]) T {
	core := n.ref()
	e, ok := s.entries[core]
	if !ok {
		name := "<nil>"
		if core != nil {
			name = core.name
		}
		panic(fmt.Sprintf("graph: Get of %q, a node not in the System", name))
	}
	v, _ := e.value.(T)
	return v
}

// Layers returns the System's nodes in layers, layer 0 first: a node
// with no dependencies is in layer 0, and any other node is one layer
// above the highest of its Use and After dependencies in the System, so
// every node's dependencies sit in layers below it. Within a layer, nodes
// are in definition order. Each call returns a fresh copy.
func (s *System) Layers() [][]Dependency {
	out := make([][]Dependency, len(s.layers))
	for i, layer := range s.layers {
		out[i] = make([]Dependency, len(layer))
		for j, e := range layer {
			out[i][j] = Dependency{
				Name:  e.node.name,
				Value: e.value,
			}
		}
	}
	return out
}

// system computes the layers of b's entries by longest path and returns
// them as a System. The Use edges are acyclic, since construct panics on a
// cycle, but an After edge can close one; that panics with its path here.
func (b *build) system() *System {
	level := make(map[*node]int, len(b.entries))
	visiting := make(map[*node]bool)
	var path []*node
	var layer func(n *node) int
	layer = func(n *node) int {
		if l, ok := level[n]; ok {
			return l
		}
		if visiting[n] {
			panic("graph: dependency cycle: " + cyclePath(path, n))
		}
		visiting[n] = true
		path = append(path, n)
		e := b.entries[n]
		l := 0
		for _, d := range e.uses {
			l = max(l, layer(d)+1)
		}
		for _, d := range e.after {
			if _, ok := b.entries[d]; ok {
				l = max(l, layer(d)+1)
			}
		}
		path = path[:len(path)-1]
		visiting[n] = false
		level[n] = l
		return l
	}

	ordered := make([]*entry, 0, len(b.entries))
	for _, e := range b.entries {
		ordered = append(ordered, e)
	}
	slices.SortFunc(ordered, func(a, b *entry) int { return cmp.Compare(a.node.index, b.node.index) })

	var layers [][]*entry
	for _, e := range ordered {
		l := layer(e.node)
		for len(layers) <= l {
			layers = append(layers, nil)
		}
		layers[l] = append(layers[l], e)
	}
	return &System{entries: b.entries, layers: layers}
}
