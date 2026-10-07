package graph_test

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-s3-storage/app/graph"
)

// names returns the names in each of sys's layers, layer 0 first.
func names(sys *graph.System) [][]string {
	var out [][]string
	for _, layer := range sys.Layers() {
		var ns []string
		for _, d := range layer {
			ns = append(ns, d.Name)
		}
		out = append(out, ns)
	}
	return out
}

// contains reports whether a node named name is in sys.
func contains(sys *graph.System, name string) bool {
	for _, layer := range sys.Layers() {
		for _, d := range layer {
			if d.Name == name {
				return true
			}
		}
	}
	return false
}

// dependency returns the Dependency named name in sys, failing the test
// when there is none.
func dependency(t *testing.T, sys *graph.System, name string) graph.Dependency {
	t.Helper()
	for _, layer := range sys.Layers() {
		for _, d := range layer {
			if d.Name == name {
				return d
			}
		}
	}
	t.Fatalf("no dependency %q in %v", name, names(sys))
	return graph.Dependency{}
}

// mustPanic fails the test unless fn panics with a message starting
// "graph: " and containing want.
func mustPanic(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		r := recover()
		if r == nil {
			t.Fatalf("no panic; want one containing %q", want)
		}
		msg := fmt.Sprint(r)
		if !strings.HasPrefix(msg, "graph: ") || !strings.Contains(msg, want) {
			t.Fatalf("panic %q; want a \"graph: \" message containing %q", msg, want)
		}
	}()
	fn()
}

// mustBuild builds roots, failing the test on an error.
func mustBuild(t *testing.T, g *graph.Graph, roots ...graph.Ref) *graph.System {
	t.Helper()
	sys, err := g.Build(roots...)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return sys
}

// counter counts constructor runs by node name.
type counter map[string]int

// constant returns a constructor that counts its run and returns v.
func constant[T any](c counter, name string, v T) func(*graph.Scope) (T, error) {
	return func(*graph.Scope) (T, error) {
		c[name]++
		return v, nil
	}
}

func TestDefineConstructsNothing(t *testing.T) {
	c := counter{}
	g := graph.New()
	a := g.Define("a", constant(c, "a", 1))
	g.Define("b", func(s *graph.Scope) (int, error) {
		c["b"]++
		return s.Use(a) + 1, nil
	})
	if len(c) != 0 {
		t.Fatalf("constructors ran before Build: %v", c)
	}
	if a.Name() != "a" {
		t.Fatalf("Name = %q, want a", a.Name())
	}
}

func TestBuildMemoizesSharedNodes(t *testing.T) {
	c := counter{}
	g := graph.New()
	type conn struct{ id int }
	shared := g.Define("shared", func(*graph.Scope) (*conn, error) {
		c["shared"]++
		return &conn{id: c["shared"]}, nil
	})
	left := g.Define("left", func(s *graph.Scope) (*conn, error) {
		c["left"]++
		return s.Use(shared), nil
	})
	right := g.Define("right", func(s *graph.Scope) (*conn, error) {
		c["right"]++
		// A second Use of the same node returns the memoized value.
		s.Use(shared)
		return s.Use(shared), nil
	})
	top := g.Define("top", func(s *graph.Scope) ([2]*conn, error) {
		c["top"]++
		return [2]*conn{s.Use(left), s.Use(right)}, nil
	})

	sys := mustBuild(t, g, top, left)
	if want := (counter{"shared": 1, "left": 1, "right": 1, "top": 1}); !maps.Equal(c, want) {
		t.Fatalf("constructor runs = %v, want %v", c, want)
	}
	pair := sys.Get(top)
	if pair[0] != pair[1] || pair[0] != sys.Get(shared) {
		t.Fatalf("dependents got different values: %p %p %p", pair[0], pair[1], sys.Get(shared))
	}

	// A second Build constructs fresh values.
	again := mustBuild(t, g, top)
	if c["shared"] != 2 {
		t.Fatalf("shared ran %d times over two Builds, want 2", c["shared"])
	}
	if again.Get(shared) == sys.Get(shared) {
		t.Fatal("second Build reused the first Build's value")
	}
}

func TestBuildConstructsDepthFirst(t *testing.T) {
	var order []string
	g := graph.New()
	rec := func(name string, deps ...*graph.Node[string]) *graph.Node[string] {
		return g.Define(name, func(s *graph.Scope) (string, error) {
			order = append(order, "enter "+name)
			for _, d := range deps {
				s.Use(d)
			}
			order = append(order, "leave "+name)
			return name, nil
		})
	}
	leaf := rec("leaf")
	mid := rec("mid", leaf)
	top := rec("top", mid, leaf)
	mustBuild(t, g, top)
	want := []string{"enter top", "enter mid", "enter leaf", "leave leaf", "leave mid", "leave top"}
	if !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestBuildConstructsOnlyWhatRootsReach(t *testing.T) {
	c := counter{}
	g := graph.New()
	a := g.Define("a", constant(c, "a", 1))
	b := g.Define("b", constant(c, "b", 2))
	g.Define("c", func(s *graph.Scope) (int, error) {
		c["c"]++
		return s.Use(b), nil
	})
	sys := mustBuild(t, g, a)
	if want := (counter{"a": 1}); !maps.Equal(c, want) {
		t.Fatalf("constructor runs = %v, want %v", c, want)
	}
	if got := names(sys); !slices.EqualFunc(got, [][]string{{"a"}}, slices.Equal) {
		t.Fatalf("layers = %v, want [[a]]", got)
	}
}

func TestLayersAreLongestPath(t *testing.T) {
	g := graph.New()
	// Defined out of dependency order, so the within-layer order is
	// definition order and not construction order.
	var a, b, c, d, e *graph.Node[int]
	// e uses d before a, so a layer taken from its last Use, not its
	// highest, would put it too low.
	e = g.Define("e", func(s *graph.Scope) (int, error) { return s.Use(d) + s.Use(a), nil })
	b = g.Define("b", func(*graph.Scope) (int, error) { return 2, nil })
	d = g.Define("d", func(s *graph.Scope) (int, error) { return s.Use(c), nil })
	a = g.Define("a", func(*graph.Scope) (int, error) { return 1, nil })
	c = g.Define("c", func(s *graph.Scope) (int, error) { return s.Use(a) + s.Use(b), nil })

	sys := mustBuild(t, g, e)
	// e uses a (layer 0) and d (layer 2): its longest path puts it at 3.
	want := [][]string{{"b", "a"}, {"c"}, {"d"}, {"e"}}
	if got := names(sys); !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("layers = %v, want %v", got, want)
	}
}

func TestAfterOrdersOnlyWithinTheSystem(t *testing.T) {
	c := counter{}
	g := graph.New()
	base := g.Define("base", constant(c, "base", 0))
	gate := g.Define("gate", func(s *graph.Scope) (int, error) {
		c["gate"]++
		return s.Use(base), nil
	})
	// worker has no Use, so its layer comes from After alone.
	worker := g.Define("worker", func(s *graph.Scope) (int, error) {
		c["worker"]++
		s.After(gate)
		return 0, nil
	})

	// gate is in the System through the root list, so worker sits above it.
	sys := mustBuild(t, g, worker, gate)
	want := [][]string{{"base"}, {"gate"}, {"worker"}}
	if got := names(sys); !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("layers = %v, want %v", got, want)
	}

	// Without gate in the System, After neither builds it nor orders on it.
	clear(c)
	sys = mustBuild(t, g, worker)
	if c["gate"] != 0 || contains(sys, "gate") {
		t.Fatalf("After pulled gate in: runs %v, layers %v", c, names(sys))
	}
	want = [][]string{{"worker"}}
	if got := names(sys); !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("layers = %v, want %v", got, want)
	}
}

func TestAfterTargetBuiltLaterStillOrders(t *testing.T) {
	g := graph.New()
	late := g.Define("late", func(*graph.Scope) (int, error) { return 0, nil })
	early := g.Define("early", func(s *graph.Scope) (int, error) {
		// late is not built yet when After names it.
		s.After(late)
		return 0, nil
	})
	sys := mustBuild(t, g, early, late)
	want := [][]string{{"late"}, {"early"}}
	if got := names(sys); !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("layers = %v, want %v", got, want)
	}
}

func TestConstructorErrorIsLabelled(t *testing.T) {
	errDown := errors.New("connection refused")
	g := graph.New()
	db := g.Define("database", func(*graph.Scope) (int, error) { return 0, errDown })
	sys, err := g.Build(db)
	if sys != nil {
		t.Fatal("Build returned a System with its error")
	}
	if err == nil || err.Error() != "database: connection refused" {
		t.Fatalf("err = %v, want database: connection refused", err)
	}
	if !errors.Is(err, errDown) {
		t.Fatalf("errors.Is(%v, errDown) = false", err)
	}
}

// codeError is an error type for errors.As.
type codeError struct{ code int }

func (e *codeError) Error() string { return fmt.Sprintf("code %d", e.code) }

func TestDependencyErrorAbortsDependents(t *testing.T) {
	g := graph.New()
	cfg := g.Define("config", func(*graph.Scope) (int, error) { return 0, &codeError{code: 7} })
	resumed := false
	db := g.Define("database", func(s *graph.Scope) (int, error) {
		v := s.Use(cfg)
		resumed = true
		return v, nil
	})
	server := g.Define("server", func(s *graph.Scope) (int, error) {
		v := s.Use(db)
		resumed = true
		return v, nil
	})
	_, err := g.Build(server)
	if resumed {
		t.Fatal("a dependent's constructor resumed after its dependency failed")
	}
	if err == nil || err.Error() != "config: code 7" {
		t.Fatalf("err = %v, want config: code 7", err)
	}
	var ce *codeError
	if !errors.As(err, &ce) || ce.code != 7 {
		t.Fatalf("errors.As(%v) = %v", err, ce)
	}
}

func TestForeignPanicsPassThrough(t *testing.T) {
	g := graph.New()
	boom := g.Define("boom", func(*graph.Scope) (int, error) { panic("not ours") })
	top := g.Define("top", func(s *graph.Scope) (int, error) { return s.Use(boom), nil })
	defer func() {
		if r := recover(); r != "not ours" {
			t.Fatalf("recovered %v, want the constructor's own panic", r)
		}
	}()
	_, _ = g.Build(top)
	t.Fatal("Build returned")
}

func TestDependencyCarriesNameAndValue(t *testing.T) {
	g := graph.New()
	db := g.Define("database", func(*graph.Scope) (string, error) { return "pool", nil })
	top := g.Define("top", func(s *graph.Scope) (string, error) { return "over " + s.Use(db), nil })
	sys := mustBuild(t, g, top)

	want := [][]graph.Dependency{
		{{Name: "database", Value: "pool"}},
		{{Name: "top", Value: "over pool"}},
	}
	if got := sys.Layers(); !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("Layers = %v, want %v", got, want)
	}
	if got := sys.Get(db); got != "pool" {
		t.Fatalf("Get = %q, want pool", got)
	}
}

func TestRefNamesTheNode(t *testing.T) {
	g := graph.New()
	refs := []graph.Ref{
		g.Define("count", func(*graph.Scope) (int, error) { return 0, nil }),
		g.Define("label", func(*graph.Scope) (string, error) { return "", nil }),
	}
	var got []string
	for _, r := range refs {
		got = append(got, r.Name())
	}
	if want := []string{"count", "label"}; !slices.Equal(got, want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
}

// swallow runs use and recovers whatever it panics with, as a constructor
// must not.
func swallow(use func()) {
	defer func() { _ = recover() }()
	use()
}

func TestRecoveredAbortPanics(t *testing.T) {
	failing := func(g *graph.Graph) *graph.Node[int] {
		return g.Define("database", func(*graph.Scope) (int, error) { return 0, errors.New("refused") })
	}
	const want = `the constructor of "top" carried on after its Use of `

	t.Run("returning a value", func(t *testing.T) {
		g := graph.New()
		db := failing(g)
		top := g.Define("top", func(s *graph.Scope) (int, error) {
			swallow(func() { s.Use(db) })
			return 1, nil
		})
		mustPanic(t, want+`"database" aborted; a constructor must not recover`, func() { _, _ = g.Build(top) })
	})
	t.Run("returning an error", func(t *testing.T) {
		g := graph.New()
		db := failing(g)
		top := g.Define("top", func(s *graph.Scope) (int, error) {
			swallow(func() { s.Use(db) })
			return 0, errors.New("its own")
		})
		mustPanic(t, want+`"database" aborted`, func() { _, _ = g.Build(top) })
	})
	t.Run("an abort from further down", func(t *testing.T) {
		g := graph.New()
		db := failing(g)
		mid := g.Define("mid", func(s *graph.Scope) (int, error) { return s.Use(db), nil })
		top := g.Define("top", func(s *graph.Scope) (int, error) {
			swallow(func() { s.Use(mid) })
			return 1, nil
		})
		mustPanic(t, want+`"mid" aborted`, func() { _, _ = g.Build(top) })
	})
	t.Run("a Use after the recover", func(t *testing.T) {
		g := graph.New()
		db := failing(g)
		other := g.Define("other", func(*graph.Scope) (int, error) { return 2, nil })
		reached := false
		top := g.Define("top", func(s *graph.Scope) (int, error) {
			swallow(func() { s.Use(db) })
			v := s.Use(other)
			reached = true
			return v, nil
		})
		mustPanic(t, want+`"database" aborted`, func() { _, _ = g.Build(top) })
		if reached {
			t.Error("a Use after the recovered abort returned")
		}
	})
}

func TestReplaceSubstitutesTheConstructor(t *testing.T) {
	g := graph.New()
	cfg := g.Define("config", func(*graph.Scope) (string, error) { return "dsn", nil })
	store := g.Define("store", func(s *graph.Scope) (string, error) {
		return "real:" + s.Use(cfg), nil
	})
	top := g.Define("top", func(s *graph.Scope) (string, error) { return s.Use(store), nil })
	other := g.Define("other", func(s *graph.Scope) (string, error) { return s.Use(store), nil })

	runs := 0
	g.Replace(store, func(s *graph.Scope) (string, error) {
		runs++
		return "fake:" + s.Use(cfg), nil
	})
	sys := mustBuild(t, g, top, other)

	if got := sys.Get(top); got != "fake:dsn" {
		t.Fatalf("top = %q, want fake:dsn", got)
	}
	if runs != 1 {
		t.Fatalf("substitute ran %d times, want 1", runs)
	}
	want := [][]string{{"config"}, {"store"}, {"top", "other"}}
	if got := names(sys); !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("layers = %v, want %v", got, want)
	}
	if got := dependency(t, sys, "store").Value; got != "fake:dsn" {
		t.Fatalf("store's Value = %v, want fake:dsn", got)
	}
}

func TestObserveSeesEachNodeABuildBegins(t *testing.T) {
	g := graph.New()
	var seen, again []string
	g.Observe(func(name string) { seen = append(seen, name) })
	g.Observe(func(name string) { again = append(again, name) })
	var order []string
	leaf := g.Define("leaf", func(*graph.Scope) (int, error) {
		order = append(order, "construct leaf")
		return 0, errors.New("down")
	})
	g.Define("unreached", func(*graph.Scope) (int, error) { return 0, nil })
	mid := g.Define("mid", func(s *graph.Scope) (int, error) { return s.Use(leaf), nil })
	top := g.Define("top", func(s *graph.Scope) (int, error) { return s.Use(mid) + s.Use(leaf), nil })
	g.Observe(func(name string) { order = append(order, "observe "+name) })

	if _, err := g.Build(top); err == nil {
		t.Fatal("Build succeeded, want the leaf's error")
	}

	// Depth-first from the root, each node once, the failing leaf included,
	// and never a node the root does not reach.
	want := []string{"top", "mid", "leaf"}
	if !slices.Equal(seen, want) || !slices.Equal(again, want) {
		t.Fatalf("observed %v and %v, want %v from each observer", seen, again, want)
	}
	// An observer sees a node before its constructor runs.
	if wantOrder := []string{"observe top", "observe mid", "observe leaf", "construct leaf"}; !slices.Equal(order, wantOrder) {
		t.Fatalf("order = %v, want %v", order, wantOrder)
	}
}

func TestNilInterfaceValue(t *testing.T) {
	g := graph.New()
	none := g.Define("none", func(*graph.Scope) (error, error) { return nil, nil })
	top := g.Define("top", func(s *graph.Scope) (bool, error) { return s.Use(none) == nil, nil })
	sys := mustBuild(t, g, top)
	if !sys.Get(top) || sys.Get(none) != nil {
		t.Fatal("a nil interface value did not pass through Use and Get")
	}
}

func TestWiringPanics(t *testing.T) {
	t.Run("cycle", func(t *testing.T) {
		g := graph.New()
		var a, b, c *graph.Node[int]
		a = g.Define("a", func(s *graph.Scope) (int, error) { return s.Use(b), nil })
		b = g.Define("b", func(s *graph.Scope) (int, error) { return s.Use(c), nil })
		resumed := false
		c = g.Define("c", func(s *graph.Scope) (int, error) {
			v := s.Use(b)
			resumed = true
			return v, nil
		})
		mustPanic(t, "dependency cycle: b -> c -> b", func() { _, _ = g.Build(a) })
		if resumed {
			t.Fatal("the Use that closed the cycle returned")
		}
	})
	t.Run("self cycle", func(t *testing.T) {
		g := graph.New()
		var a *graph.Node[int]
		a = g.Define("a", func(s *graph.Scope) (int, error) { return s.Use(a), nil })
		mustPanic(t, "dependency cycle: a -> a", func() { _, _ = g.Build(a) })
	})
	t.Run("after cycle", func(t *testing.T) {
		g := graph.New()
		var a, b *graph.Node[int]
		a = g.Define("a", func(s *graph.Scope) (int, error) { return s.Use(b), nil })
		b = g.Define("b", func(s *graph.Scope) (int, error) { s.After(a); return 0, nil })
		mustPanic(t, "dependency cycle: a -> b -> a", func() { _, _ = g.Build(a) })
	})
	t.Run("use of a foreign node", func(t *testing.T) {
		g, h := graph.New(), graph.New()
		foreign := h.Define("foreign", func(*graph.Scope) (int, error) { return 0, nil })
		top := g.Define("top", func(s *graph.Scope) (int, error) { return s.Use(foreign), nil })
		mustPanic(t, `Use of "foreign", a node defined on another Graph`, func() { _, _ = g.Build(top) })
	})
	t.Run("after of a foreign node", func(t *testing.T) {
		g, h := graph.New(), graph.New()
		foreign := h.Define("foreign", func(*graph.Scope) (int, error) { return 0, nil })
		top := g.Define("top", func(s *graph.Scope) (int, error) { s.After(foreign); return 0, nil })
		mustPanic(t, `After of "foreign", a node defined on another Graph`, func() { _, _ = g.Build(top) })
	})
	t.Run("build of a foreign root", func(t *testing.T) {
		g, h := graph.New(), graph.New()
		foreign := h.Define("foreign", func(*graph.Scope) (int, error) { return 0, nil })
		mustPanic(t, `Build of "foreign", a node defined on another Graph`, func() { _, _ = g.Build(foreign) })
	})
	t.Run("nil root", func(t *testing.T) {
		g := graph.New()
		var n *graph.Node[int]
		mustPanic(t, "Build of a nil node", func() { _, _ = g.Build(n) })
	})
	t.Run("nil root after a real one", func(t *testing.T) {
		g, c := graph.New(), counter{}
		real := g.Define("real", constant(c, "real", 1))
		var n *graph.Node[int]
		mustPanic(t, "Build of a nil node", func() { _, _ = g.Build(real, n) })
		if c["real"] != 0 {
			t.Errorf("real's constructor ran %d times before the panic, want 0", c["real"])
		}
	})
	t.Run("foreign root after a real one", func(t *testing.T) {
		g, h, c := graph.New(), graph.New(), counter{}
		real := g.Define("real", constant(c, "real", 1))
		foreign := h.Define("foreign", constant(c, "foreign", 2))
		mustPanic(t, `Build of "foreign", a node defined on another Graph`, func() { _, _ = g.Build(real, foreign) })
		if c["real"] != 0 || c["foreign"] != 0 {
			t.Errorf("constructors ran %v before the panic, want none", c)
		}
	})

	escaped := func(t *testing.T) (*graph.Scope, *graph.Node[int]) {
		t.Helper()
		g := graph.New()
		var kept *graph.Scope
		dep := g.Define("dep", func(*graph.Scope) (int, error) { return 0, nil })
		top := g.Define("top", func(s *graph.Scope) (int, error) { kept = s; return 0, nil })
		mustBuild(t, g, top, dep)
		return kept, dep
	}
	t.Run("escaped Use", func(t *testing.T) {
		s, dep := escaped(t)
		mustPanic(t, `Use called after the constructor of "top" returned`, func() { s.Use(dep) })
	})
	t.Run("escaped After", func(t *testing.T) {
		s, dep := escaped(t)
		mustPanic(t, `After called after the constructor of "top" returned`, func() { s.After(dep) })
	})

	t.Run("define after build", func(t *testing.T) {
		g := graph.New()
		n := g.Define("n", func(*graph.Scope) (int, error) { return 0, errors.New("fails") })
		_, _ = g.Build(n)
		mustPanic(t, `Define of "late" after Build`, func() {
			g.Define("late", func(*graph.Scope) (int, error) { return 1, nil })
		})
	})
	t.Run("replace after build", func(t *testing.T) {
		g := graph.New()
		n := g.Define("n", func(*graph.Scope) (int, error) { return 0, errors.New("fails") })
		_, _ = g.Build(n)
		mustPanic(t, `Replace of "n" after Build`, func() {
			g.Replace(n, func(*graph.Scope) (int, error) { return 1, nil })
		})
	})
	t.Run("replace of a foreign node", func(t *testing.T) {
		g, h := graph.New(), graph.New()
		foreign := h.Define("foreign", func(*graph.Scope) (int, error) { return 0, nil })
		mustPanic(t, `Replace of "foreign", a node defined on another Graph`, func() {
			g.Replace(foreign, func(*graph.Scope) (int, error) { return 1, nil })
		})
	})
	t.Run("get of a node not in the system", func(t *testing.T) {
		g := graph.New()
		in := g.Define("in", func(*graph.Scope) (int, error) { return 0, nil })
		out := g.Define("out", func(*graph.Scope) (int, error) { return 0, nil })
		sys := mustBuild(t, g, in)
		mustPanic(t, `Get of "out", a node not in the System`, func() { sys.Get(out) })
	})
	t.Run("get of a node from another build", func(t *testing.T) {
		g, h := graph.New(), graph.New()
		in := g.Define("in", func(*graph.Scope) (int, error) { return 0, nil })
		foreign := h.Define("in", func(*graph.Scope) (int, error) { return 0, nil })
		sys := mustBuild(t, g, in)
		mustPanic(t, `Get of "in", a node not in the System`, func() { sys.Get(foreign) })
	})
	t.Run("empty name", func(t *testing.T) {
		g := graph.New()
		mustPanic(t, "Define with an empty name", func() {
			g.Define("", func(*graph.Scope) (int, error) { return 0, nil })
		})
	})
	t.Run("duplicate name", func(t *testing.T) {
		g := graph.New()
		g.Define("db", func(*graph.Scope) (int, error) { return 0, nil })
		mustPanic(t, `Define of duplicate name "db"`, func() {
			g.Define("db", func(*graph.Scope) (string, error) { return "", nil })
		})
	})
	t.Run("nil constructor", func(t *testing.T) {
		g := graph.New()
		mustPanic(t, `Define "db" with a nil constructor`, func() {
			g.Define[int]("db", nil)
		})
	})
	t.Run("nil observer", func(t *testing.T) {
		g := graph.New()
		mustPanic(t, "Observe with a nil function", func() { g.Observe(nil) })
	})
}
