package lifecycle_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/JaimeStill/spike-s3-storage/app/graph"
)

// webService is go-web-service's composition, the graph that
// graph/stages_test.go builds, over this package's recorder fakes: the same
// nodes and edges, so the stage table falls out of the layers, each value
// a fake that records its start and stop. The shape is restated here
// because graph's tests may import only the standard library and graph,
// so they cannot run it on a Coordinator, and this package cannot import
// theirs. reactors and server take order-only edges, as there.
func webService(r *recorder, serving chan struct{}) (g *graph.Graph, roots []graph.Ref) {
	g = graph.New()
	config := node(g, &fake{name: "config", r: r})
	// stageInfrastructure: the pool and the object store, over the config.
	database := node(g, &fake{name: "database", r: r}, config)
	store := node(g, &fake{name: "store", r: r}, config)
	// The domain over the database and the object store; stageSchema, the
	// admin service over the pool; the storage admin domain over the store.
	domain := node(g, &fake{name: "domain", r: r}, database, store)
	schema := node(g, &fake{name: "schema", r: r}, database)
	storage := node(g, &fake{name: "storage", r: r}, store)
	// stageReactors: the sweeper over the pool, once the schema is verified.
	reactors := g.Define("reactors", func(s *graph.Scope) (*fake, error) {
		s.After(schema)
		s.Use(database)
		return &fake{name: "reactors", r: r}, nil
	})
	// stageRoot: the server over the domain and admin pieces, started after
	// the reactors and drained before them. Its start marks it serving.
	server := g.Define("server", func(s *graph.Scope) (*fake, error) {
		s.After(reactors)
		s.Use(domain)
		s.Use(schema)
		s.Use(storage)
		return &fake{name: "server", r: r, start: func(context.Context) error {
			close(serving)
			return nil
		}}, nil
	})
	// The reactors are a root: nothing uses their value.
	return g, []graph.Ref{server, reactors}
}

// TestRunServesAGraphShapedLikeTheWebService runs the web service's graph
// on Run, the long-running form: its layers start in stage order, it
// serves until its context ends, and it shuts down in reverse.
func TestRunServesAGraphShapedLikeTheWebService(t *testing.T) {
	var r recorder
	serving := make(chan struct{})
	g, roots := webService(&r, serving)
	sys := build(t, g, roots...)
	// The layers TestWebServiceStageOrder asserts in graph's tests, so the
	// shape restated here is the one that test pins.
	layers := [][]string{
		{"config"},
		{"database", "store"},
		{"domain", "schema", "storage"},
		{"reactors"},
		{"server"},
	}
	var got [][]string
	for _, layer := range sys.Layers() {
		var names []string
		for _, d := range layer {
			names = append(names, d.Name)
		}
		got = append(got, names)
	}
	if !slices.EqualFunc(got, layers, slices.Equal) {
		t.Fatalf("layers = %v, want %v", got, layers)
	}
	c8r := coordinator(sys, patience)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c8r.Run(ctx) }()

	await(t, serving, "the server's Start")
	select {
	case err := <-done:
		t.Fatalf("Run returned %v before its context ended", err)
	case <-time.After(20 * time.Millisecond):
	}
	starts := [][]string{
		{"start config"},
		{"start database", "start store"},
		{"start domain", "start schema", "start storage"},
		{"start reactors"},
		{"start server"},
	}
	if got := r.list(); !phased(got, starts) {
		t.Fatalf("events while serving = %q, want the starts %q and nothing more", got, starts)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil on a clean stop", err)
		}
	case <-time.After(patience):
		t.Fatal("Run did not return once its context ended")
	}
	stops := [][]string{
		{"stop server"},
		{"stop reactors"},
		{"stop domain", "stop schema", "stop storage"},
		{"stop database", "stop store"},
		{"stop config"},
	}
	if got := r.list(); !phased(got, append(starts, stops...)) {
		t.Errorf("events = %q, want the starts %q, then the stops %q", got, starts, stops)
	}
}

// phased reports whether got is exactly phases, in order, each phase's
// events in any order among themselves: a layer starts and stops
// concurrently, and the layers one after another.
func phased(got []string, phases [][]string) bool {
	for _, phase := range phases {
		if len(got) < len(phase) || !sameSet(got[:len(phase)], phase...) {
			return false
		}
		got = got[len(phase):]
	}
	return len(got) == 0
}
