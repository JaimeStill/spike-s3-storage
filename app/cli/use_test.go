package cli_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/JaimeStill/spike-s3-storage/app/graph"
	"github.com/JaimeStill/spike-s3-storage/app/lifecycle"
	"github.com/standards-lab/go-core/config"
	"github.com/standards-lab/go-core/process"
)

// recorder collects the events of one dispatch, safe for the concurrent
// starts and shutdowns of a layer.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

// count returns how many events equal event.
func (r *recorder) count(event string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e == event {
			n++
		}
	}
	return n
}

// fake is a subsystem that records its Start and Shutdown, and fails or
// blocks as set.
type fake struct {
	name     string
	rec      *recorder
	startErr error
	stopErr  error
	block    chan struct{} // when non-nil, Shutdown waits for it to close
}

func (f *fake) Start(context.Context) error {
	f.rec.add("start " + f.name)
	return f.startErr
}

func (f *fake) Shutdown(context.Context) error {
	f.rec.add("stop " + f.name)
	if f.block != nil {
		<-f.block
	}
	return f.stopErr
}

// useTree is a test tree with a graph: prog { data { get, put }, plain }.
// data declares db; get adds store, which uses db, and names db again; put adds
// nothing; plain and the root declare nothing. get takes exactly one argument,
// requires --key, and holds --a and --b exclusive. The root's PreRun, the
// constructors, the fakes, and the leaves record to rec.
type useTree struct {
	root  *cli.Command
	data  *cli.Command // the parent that declares db
	g     *graph.Graph
	cfg   *graph.Node[lifecycle.Config]
	db    *graph.Node[*fake]
	store *graph.Node[*fake]
	rec   *recorder

	// Set before dispatching, to fail or shape one step.
	hookErr  error
	buildErr error // store's constructor
	startErr error // store's Start
	stopErr  error // store's Shutdown
	block    chan struct{}
	runErr   error
	timeout  time.Duration
	body     func(ctx context.Context) error // get's body, after recording

	inv *cli.Invocation // what the last leaf to run received
}

func newUseTree() *useTree {
	u := &useTree{rec: &recorder{}, timeout: 5 * time.Second}
	u.g = graph.New()
	u.cfg = u.g.Define("lifecycle", func(*graph.Scope) (lifecycle.Config, error) {
		u.rec.add("build lifecycle")
		return lifecycle.Config{ShutdownTimeout: config.Duration(u.timeout)}, nil
	})
	u.db = u.g.Define("db", func(*graph.Scope) (*fake, error) {
		u.rec.add("build db")
		return &fake{name: "db", rec: u.rec}, nil
	})
	u.store = u.g.Define("store", func(s *graph.Scope) (*fake, error) {
		s.Use(u.db)
		u.rec.add("build store")
		if u.buildErr != nil {
			return nil, u.buildErr
		}
		return &fake{name: "store", rec: u.rec, startErr: u.startErr, stopErr: u.stopErr, block: u.block}, nil
	})

	leaf := func(name string) func(context.Context, *cli.Invocation) error {
		return func(ctx context.Context, inv *cli.Invocation) error {
			u.rec.add("run " + name)
			u.inv = inv
			if name == "get" && u.body != nil {
				return u.body(ctx)
			}
			return u.runErr
		}
	}
	// get declares store and db in two calls, so the second appends.
	get := (&cli.Command{Name: "get", Args: cli.ExactArgs(1), Run: leaf("get")}).Use(u.store).Use(u.db)
	get.Flags().String("key", "", "")
	get.Flags().Bool("a", false, "")
	get.Flags().Bool("b", false, "")
	get.Require("key")
	get.Exclusive("a", "b")
	data := (&cli.Command{Name: "data"}).Use(u.db).Add(
		get,
		&cli.Command{Name: "put", Run: leaf("put")},
	)
	u.root = &cli.Command{
		Name: "prog",
		PreRun: func(context.Context, *cli.Invocation) error {
			u.rec.add("prerun")
			return u.hookErr
		},
	}
	u.data = data
	u.root.Add(data, &cli.Command{Name: "plain", Run: leaf("plain")})
	return u
}

// run dispatches args over the tree with its graph, under ctx.
func (u *useTree) run(ctx context.Context, args ...string) result {
	var stdout, stderr bytes.Buffer
	code := cli.Run(ctx, u.root, args, cli.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr}, cli.WithGraph(u.g, u.cfg))
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// builds returns how many constructors ran.
func (u *useTree) builds() int {
	n := 0
	for _, e := range u.rec.list() {
		if strings.HasPrefix(e, "build ") {
			n++
		}
	}
	return n
}

func TestUse_BuildsAndRunsTheLeafUnderALifecycle(t *testing.T) {
	u := newUseTree()
	r := u.run(context.Background(), "data", "get", "--key", "k", "x")
	if r.code != process.ExitOK {
		t.Fatalf("code = %d, want %d; stderr %q", r.code, process.ExitOK, r.stderr)
	}
	want := []string{
		"prerun",
		// store's constructor records after its Use of db returns.
		"build db", "build store", "build lifecycle",
		"start db", "start store",
		"run get",
		"stop store", "stop db",
	}
	if got := u.rec.list(); !slices.Equal(got, want) {
		t.Errorf("events = %q, want %q", got, want)
	}
	if got := u.inv.Get(u.store); got == nil || got.name != "store" {
		t.Errorf("Get(store) = %v, want the built store", got)
	}
}

func TestUse_InheritedFromTheParent(t *testing.T) {
	u := newUseTree()
	r := u.run(context.Background(), "data", "put")
	if r.code != process.ExitOK {
		t.Fatalf("code = %d, want %d; stderr %q", r.code, process.ExitOK, r.stderr)
	}
	want := []string{"prerun", "build db", "build lifecycle", "start db", "run put", "stop db"}
	if got := u.rec.list(); !slices.Equal(got, want) {
		t.Errorf("events = %q, want %q", got, want)
	}
	if got := u.inv.Get(u.db); got == nil || got.name != "db" {
		t.Errorf("Get(db) = %v, want the built db", got)
	}
}

func TestUse_NodeNamedTwiceIsBuiltOnce(t *testing.T) {
	u := newUseTree()
	u.run(context.Background(), "data", "get", "--key", "k", "x")
	if n := u.rec.count("build db"); n != 1 {
		t.Errorf("db built %d times, want 1", n)
	}
	if n := u.rec.count("start db"); n != 1 {
		t.Errorf("db started %d times, want 1", n)
	}
}

func TestUse_NoneRunsWithoutABuild(t *testing.T) {
	u := newUseTree()
	r := u.run(context.Background(), "plain")
	if r.code != process.ExitOK {
		t.Fatalf("code = %d, want %d; stderr %q", r.code, process.ExitOK, r.stderr)
	}
	if got, want := u.rec.list(), []string{"prerun", "run plain"}; !slices.Equal(got, want) {
		t.Errorf("events = %q, want %q", got, want)
	}
}

func TestUse_BuildsNothingWhenTheDispatchEndsEarly(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		hookErr error
		code    int
	}{
		{"help", []string{"data", "get", "--help"}, nil, process.ExitUsage},
		{"short help", []string{"data", "get", "-h"}, nil, process.ExitUsage},
		{"parent alone", []string{"data"}, nil, process.ExitUsage},
		{"unknown subcommand", []string{"data", "nope"}, nil, process.ExitUsage},
		{"unknown flag", []string{"data", "get", "--nope", "--key", "k", "x"}, nil, process.ExitUsage},
		{"argument count", []string{"data", "get", "--key", "k"}, nil, process.ExitUsage},
		{"missing required flag", []string{"data", "get", "x"}, nil, process.ExitUsage},
		{"exclusive group", []string{"data", "get", "--key", "k", "--a", "--b", "x"}, nil, process.ExitUsage},
		{"PreRun error", []string{"data", "get", "--key", "k", "x"}, errors.New("hook failed"), process.ExitFailure},
		{"PreRun usage error", []string{"data", "put"}, cli.Usagef("bad root flag"), process.ExitUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := newUseTree()
			u.hookErr = tt.hookErr
			r := u.run(context.Background(), tt.args...)
			if r.code != tt.code {
				t.Errorf("code = %d, want %d", r.code, tt.code)
			}
			if n := u.builds(); n != 0 {
				t.Errorf("%d constructors ran, want 0; events %q", n, u.rec.list())
			}
		})
	}
}

func TestUse_FailuresReportedOnce(t *testing.T) {
	tests := []struct {
		name   string
		set    func(u *useTree)
		err    string // the reported error
		events []string
	}{
		{
			"build error",
			func(u *useTree) { u.buildErr = errors.New("no store") },
			"store: no store",
			[]string{"prerun", "build db", "build store"},
		},
		{
			"start error",
			func(u *useTree) { u.startErr = errors.New("store down") },
			"store: store down",
			[]string{"prerun", "build db", "build store", "build lifecycle", "start db", "start store", "stop store", "stop db"},
		},
		{
			"body error",
			func(u *useTree) { u.runErr = errors.New("get failed") },
			"get failed",
			[]string{"prerun", "build db", "build store", "build lifecycle", "start db", "start store", "run get", "stop store", "stop db"},
		},
		{
			"shutdown error",
			func(u *useTree) { u.stopErr = errors.New("store stuck") },
			"shutdown: store: store stuck",
			[]string{"prerun", "build db", "build store", "build lifecycle", "start db", "start store", "run get", "stop store", "stop db"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := newUseTree()
			tt.set(u)
			r := u.run(context.Background(), "data", "get", "--key", "k", "x")
			if r.code != process.ExitFailure {
				t.Errorf("code = %d, want %d", r.code, process.ExitFailure)
			}
			if want := "prog data get: " + tt.err + "\n"; r.stderr != want {
				t.Errorf("stderr = %q, want exactly one report %q", r.stderr, want)
			}
			if got := u.rec.list(); !slices.Equal(got, tt.events) {
				t.Errorf("events = %q, want %q", got, tt.events)
			}
		})
	}
}

func TestUse_ShutsDownWhenTheContextEndsMidBody(t *testing.T) {
	u := newUseTree()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u.body = func(ctx context.Context) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}
	r := u.run(ctx, "data", "get", "--key", "k", "x")
	if r.code != process.ExitFailure {
		t.Errorf("code = %d, want %d", r.code, process.ExitFailure)
	}
	if want := "prog data get: " + context.Canceled.Error() + "\n"; r.stderr != want {
		t.Errorf("stderr = %q, want %q", r.stderr, want)
	}
	got := u.rec.list()
	want := []string{"run get", "stop store", "stop db"}
	if i := slices.Index(got, "run get"); i < 0 || !slices.Equal(got[i:], want) {
		t.Errorf("events = %q, want them to end %q", got, want)
	}
}

func TestUse_ShutdownTimeoutComesFromTheConfigNode(t *testing.T) {
	u := newUseTree()
	u.timeout = 20 * time.Millisecond
	u.block = make(chan struct{})
	t.Cleanup(func() { close(u.block) })
	start := time.Now()
	r := u.run(context.Background(), "data", "get", "--key", "k", "x")
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("dispatch took %v, want it bounded by the 20ms shutdown timeout", elapsed)
	}
	if r.code != process.ExitFailure {
		t.Errorf("code = %d, want %d", r.code, process.ExitFailure)
	}
	want := fmt.Sprintf("prog data get: shutdown: timeout after %v: %v\n", u.timeout, context.DeadlineExceeded)
	if r.stderr != want {
		t.Errorf("stderr = %q, want %q", r.stderr, want)
	}
}

func TestUse_PanicsWithoutWithGraph(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"leaf that declares nodes", []string{"data", "get", "--key", "k", "x"}},
		{"leaf that declares none", []string{"plain"}},
		{"help", []string{"--help"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := newUseTree()
			defer func() {
				r := recover()
				want := "cli: prog data: Use declared but Run has no WithGraph option"
				if r != want {
					t.Errorf("panic = %v, want %q", r, want)
				}
				if n := len(u.rec.list()); n != 0 {
					t.Errorf("events = %q, want none", u.rec.list())
				}
			}()
			dispatch(t, u.root, tt.args...)
		})
	}
}

func TestWithGraph_PanicsOnNil(t *testing.T) {
	u := newUseTree()
	for name, call := range map[string]func(){
		"nil graph":  func() { cli.WithGraph(nil, u.cfg) },
		"nil config": func() { cli.WithGraph(u.g, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Error("WithGraph did not panic")
				}
			}()
			call()
		})
	}
}

func TestUse_ReturnsTheCommand(t *testing.T) {
	u := newUseTree()
	c := &cli.Command{Name: "c"}
	if got := c.Use(u.db); got != c {
		t.Errorf("Use returned %p, want the command %p", got, c)
	}
}

func TestUse_NoRefsDeclaresNothing(t *testing.T) {
	// A Use with no refs is accepted, as Require with no names is, and
	// declares nothing: the tree dispatches without WithGraph.
	root := (&cli.Command{Name: "prog"}).Add(
		(&cli.Command{Name: "leaf", Run: func(context.Context, *cli.Invocation) error { return nil }}).Use(),
	)
	if r := dispatch(t, root, "leaf"); r.code != process.ExitOK {
		t.Errorf("code = %d, want %d; stderr %q", r.code, process.ExitOK, r.stderr)
	}
}

func TestUse_PanicsOnAnUntypedNilRef(t *testing.T) {
	u := newUseTree()
	for name, refs := range map[string][]graph.Ref{
		"nil":                 {nil},
		"nil after a non-nil": {u.db, nil},
	} {
		t.Run(name, func(t *testing.T) {
			leaf := &cli.Command{Name: "leaf"}
			(&cli.Command{Name: "prog"}).Add(leaf)
			defer func() {
				want := "cli: prog leaf: Use of a nil node"
				if r := recover(); r != want {
					t.Errorf("panic = %v, want %q", r, want)
				}
			}()
			leaf.Use(refs...)
		})
	}
}

func TestUse_NilNodePanicsAtDispatch(t *testing.T) {
	// A nil *graph.Node passes Use and reaches graph.Build, which panics on
	// it when a dispatch runs a leaf whose path declares it. Build checks
	// every root before it constructs any, so no constructor runs even when
	// a real node comes before the nil one, declared on the leaf or
	// inherited from a parent.
	var nilNode *graph.Node[*fake]
	tests := []struct {
		name string
		// add adds a leaf named broken to u's tree, declaring nilNode, and
		// returns the path that dispatches it.
		add func(u *useTree, leaf *cli.Command) []string
	}{
		{"alone", func(u *useTree, leaf *cli.Command) []string {
			u.root.Add(leaf.Use(nilNode))
			return []string{"broken"}
		}},
		{"after a non-nil", func(u *useTree, leaf *cli.Command) []string {
			u.root.Add(leaf.Use(u.db, nilNode))
			return []string{"broken"}
		}},
		{"after a parent's node", func(u *useTree, leaf *cli.Command) []string {
			// data declares db, which the leaf inherits ahead of nilNode.
			u.data.Add(leaf.Use(nilNode))
			return []string{"data", "broken"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := newUseTree()
			broken := &cli.Command{Name: "broken", Run: func(context.Context, *cli.Invocation) error {
				u.rec.add("run broken")
				return nil
			}}
			args := tt.add(u, broken)
			defer func() {
				want := "graph: Build of a nil node"
				if r := recover(); r != want {
					t.Errorf("panic = %v, want %q", r, want)
				}
				if u.rec.count("run broken") != 0 {
					t.Error("the leaf ran")
				}
				if u.builds() != 0 {
					t.Errorf("events = %q, want no constructor to run", u.rec.list())
				}
			}()
			u.run(context.Background(), args...)
		})
	}
}
