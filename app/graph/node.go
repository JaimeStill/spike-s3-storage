package graph

// node is the untyped core of a [Node]: what a Build needs to construct it
// without knowing its type.
type node struct {
	graph *Graph
	name  string
	// index is the node's position in definition order, the order of the
	// nodes within one layer.
	index int
	// ctor is the typed constructor, wrapped to return its value as any.
	ctor func(*Scope) (any, error)
}

// Node is one node of a [Graph], defined by [Graph.Define]: a name and a
// constructor of a T. It is a handle, not a value; a constructor reaches
// the value through [Scope.Use], and a program through [System.Get].
type Node[T any] struct {
	core *node
}

// Ref is any [Node], whatever its type: what [Graph.Build] takes as roots
// and [Scope.After] as an ordering target, and what names a node without
// its type. It is sealed; only *Node[T] implements it.
type Ref interface {
	// Name returns the name the node was defined with.
	Name() string
	ref() *node
}

func (n *Node[T]) ref() *node {
	if n == nil {
		return nil
	}
	return n.core
}

// Name returns the name the node was defined with.
func (n *Node[T]) Name() string { return n.core.name }
