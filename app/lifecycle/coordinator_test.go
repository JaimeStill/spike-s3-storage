package lifecycle_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JaimeStill/spike-s3-storage/app/graph"
	"github.com/JaimeStill/spike-s3-storage/app/lifecycle"
	"github.com/standards-lab/go-core/config"
)

// patience bounds every wait a test makes on another goroutine, so a broken
// lifecycle fails the test instead of hanging it.
const patience = 5 * time.Second

// recorder collects events, in the order they happen.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) record(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

// await fails the test unless ch closes within patience.
func await(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(patience):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// rendezvous returns two functions, each of which marks its side begun and
// waits for the other's, failing with an error when the other never begins:
// the pair completes only when both run at once.
func rendezvous() (a, b func() error) {
	aBegun, bBegun := make(chan struct{}), make(chan struct{})
	meet := func(mine chan struct{}, theirs <-chan struct{}) func() error {
		return func() error {
			close(mine)
			select {
			case <-theirs:
				return nil
			case <-time.After(patience):
				return errors.New("sibling never began")
			}
		}
	}
	return meet(aBegun, bBegun), meet(bBegun, aBegun)
}

// sameSet reports whether got holds exactly want, in any order.
func sameSet(got []string, want ...string) bool {
	return len(got) == len(want) &&
		slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want)))
}

// fake is a Subsystem that records "start name" and "stop name" on r, then
// runs its optional start and stop behaviour; nil behaviour succeeds.
type fake struct {
	name        string
	r           *recorder
	start, stop func(context.Context) error
}

func (f *fake) Start(ctx context.Context) error {
	f.r.record("start " + f.name)
	if f.start != nil {
		return f.start(ctx)
	}
	return nil
}

func (f *fake) Shutdown(ctx context.Context) error {
	f.r.record("stop " + f.name)
	if f.stop != nil {
		return f.stop(ctx)
	}
	return nil
}

// node defines f on g under f's name, using deps, so f sits one layer
// above the highest of them.
func node(g *graph.Graph, f *fake, deps ...*graph.Node[*fake]) *graph.Node[*fake] {
	return g.Define(f.name, func(s *graph.Scope) (*fake, error) {
		for _, d := range deps {
			s.Use(d)
		}
		return f, nil
	})
}

// build builds g from roots, failing the test on an error.
func build(t *testing.T, g *graph.Graph, roots ...graph.Ref) *graph.System {
	t.Helper()
	sys, err := g.Build(roots...)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return sys
}

// coordinator returns a Coordinator for sys that shuts down within timeout.
func coordinator(sys *graph.System, timeout time.Duration) *lifecycle.Coordinator {
	return lifecycle.New(sys, lifecycle.Config{ShutdownTimeout: config.Duration(timeout)})
}

// noop is an Exec function that succeeds.
func noop(context.Context) error { return nil }

// mustPanic fails the test unless fn panics with a message starting
// "lifecycle: " and containing want.
func mustPanic(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		msg, _ := recover().(string)
		if !strings.HasPrefix(msg, "lifecycle: ") || !strings.Contains(msg, want) {
			t.Errorf("panic = %q, want a lifecycle panic containing %q", msg, want)
		}
	}()
	fn()
}

func TestExecStartsLayersThenRunsThenShutsDownInReverse(t *testing.T) {
	var r recorder
	g := graph.New()
	// a's Start yields before it returns, so a missing barrier lets layer
	// 1 start before it finishes.
	a := node(g, &fake{name: "a", r: &r, start: func(context.Context) error {
		time.Sleep(20 * time.Millisecond)
		r.record("started a")
		return nil
	}})
	b := node(g, &fake{name: "b", r: &r}, a)
	c := node(g, &fake{name: "c", r: &r}, a)
	d := node(g, &fake{name: "d", r: &r}, b, c)
	c8r := coordinator(build(t, g, d), patience)

	err := c8r.Exec(context.Background(), func(context.Context) error {
		r.record("fn")
		return nil
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	got := r.list()
	if len(got) != 10 {
		t.Fatalf("events = %q, want 10", got)
	}
	if !slices.Equal(got[:2], []string{"start a", "started a"}) ||
		!sameSet(got[2:4], "start b", "start c") ||
		!slices.Equal(got[4:7], []string{"start d", "fn", "stop d"}) ||
		!sameSet(got[7:9], "stop b", "stop c") || got[9] != "stop a" {
		t.Errorf("events = %q, want a, {b c}, d, fn, then d, {b c}, a", got)
	}
}

func TestLayerStartsConcurrently(t *testing.T) {
	var r recorder
	a, b := rendezvous()
	g := graph.New()
	na := node(g, &fake{name: "a", r: &r, start: func(context.Context) error { return a() }})
	nb := node(g, &fake{name: "b", r: &r, start: func(context.Context) error { return b() }})
	if err := coordinator(build(t, g, na, nb), patience).Exec(context.Background(), noop); err != nil {
		t.Fatalf("Exec: %v", err)
	}
}

func TestLayerShutsDownConcurrently(t *testing.T) {
	var r recorder
	a, b := rendezvous()
	g := graph.New()
	na := node(g, &fake{name: "a", r: &r, stop: func(context.Context) error { return a() }})
	nb := node(g, &fake{name: "b", r: &r, stop: func(context.Context) error { return b() }})
	if err := coordinator(build(t, g, na, nb), 2*patience).Exec(context.Background(), noop); err != nil {
		t.Fatalf("Exec: %v", err)
	}
}

func TestRunServesUntilContextEnds(t *testing.T) {
	var r recorder
	started := make(chan struct{})
	g := graph.New()
	a := node(g, &fake{name: "a", r: &r})
	b := node(g, &fake{name: "b", r: &r, start: func(context.Context) error {
		close(started)
		return nil
	}}, a)
	c8r := coordinator(build(t, g, b), patience)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c8r.Run(ctx) }()

	await(t, started, "b's Start")
	select {
	case err := <-done:
		t.Fatalf("Run returned %v before its context ended", err)
	case <-time.After(20 * time.Millisecond):
	}
	if got := r.list(); slices.Contains(got, "stop a") || slices.Contains(got, "stop b") {
		t.Fatalf("events = %q, shut down while serving", got)
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
	want := []string{"start a", "start b", "stop b", "stop a"}
	if got := r.list(); !slices.Equal(got, want) {
		t.Errorf("events = %q, want %q", got, want)
	}
}

func TestContextEndedBeforeStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	t.Run("Run stops cleanly", func(t *testing.T) {
		var r recorder
		g := graph.New()
		a := node(g, &fake{name: "a", r: &r})
		if err := coordinator(build(t, g, a), patience).Run(ctx); err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
		if got := r.list(); len(got) != 0 {
			t.Errorf("events = %q, want nothing started", got)
		}
	})

	t.Run("Exec returns the cancellation", func(t *testing.T) {
		var r recorder
		g := graph.New()
		a := node(g, &fake{name: "a", r: &r})
		ran := false
		err := coordinator(build(t, g, a), patience).Exec(ctx, func(context.Context) error {
			ran = true
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Exec = %v, want context.Canceled", err)
		}
		if ran {
			t.Error("Exec ran fn after its context ended")
		}
		if got := r.list(); len(got) != 0 {
			t.Errorf("events = %q, want nothing started", got)
		}
	})
}

func TestRunContextEndedDuringStartup(t *testing.T) {
	var r recorder
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := graph.New()
	a := node(g, &fake{name: "a", r: &r})
	b := node(g, &fake{name: "b", r: &r, start: func(ctx context.Context) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}}, a)
	c := node(g, &fake{name: "c", r: &r}, b)

	if err := coordinator(build(t, g, c), patience).Run(ctx); err != nil {
		t.Fatalf("Run = %v, want nil on a stop during startup", err)
	}
	// b's Start failed with the cancellation, and is still shut down; c
	// never starts.
	want := []string{"start a", "start b", "stop b", "stop a"}
	if got := r.list(); !slices.Equal(got, want) {
		t.Errorf("events = %q, want %q", got, want)
	}
}

// starter is a start-only participant: a Starter and not a Stopper.
type starter struct {
	name string
	r    *recorder
}

func (s starter) Start(context.Context) error {
	s.r.record("start " + s.name)
	return nil
}

// stopper is a stop-only participant: a Stopper and not a Starter.
type stopper struct {
	name string
	r    *recorder
}

func (s stopper) Shutdown(context.Context) error {
	s.r.record("stop " + s.name)
	return nil
}

func TestParticipation(t *testing.T) {
	var r recorder
	g := graph.New()
	nodes := []graph.Ref{
		node(g, &fake{name: "subsystem", r: &r}),
		g.Define("start-only", func(*graph.Scope) (starter, error) {
			return starter{name: "start-only", r: &r}, nil
		}),
		g.Define("stop-only", func(*graph.Scope) (stopper, error) {
			return stopper{name: "stop-only", r: &r}, nil
		}),
		g.Define("inert", func(*graph.Scope) (int, error) { return 2, nil }),
	}
	if err := coordinator(build(t, g, nodes...), patience).Exec(context.Background(), noop); err != nil {
		t.Fatalf("Exec: %v", err)
	}

	got := r.list()
	starts := []string{"start subsystem", "start start-only"}
	stops := []string{"stop subsystem", "stop stop-only"}
	if len(got) != 4 || !sameSet(got[:2], starts...) || !sameSet(got[2:], stops...) {
		t.Errorf("events = %q, want starts %q then stops %q", got, starts, stops)
	}
}

func TestStartFailure(t *testing.T) {
	var r recorder
	errBoom := errors.New("boom")
	waiting := make(chan struct{})
	g := graph.New()
	base := node(g, &fake{name: "base", r: &r})
	database := node(g, &fake{name: "database", r: &r, start: func(context.Context) error {
		select {
		case <-waiting:
			return errBoom
		case <-time.After(patience):
			return errors.New("sibling never began")
		}
	}}, base)
	slow := node(g, &fake{name: "slow", r: &r, start: func(ctx context.Context) error {
		close(waiting)
		select {
		case <-ctx.Done():
			r.record("slow cancelled")
			return ctx.Err()
		case <-time.After(patience):
			return errors.New("never cancelled")
		}
	}}, base)
	top := node(g, &fake{name: "top", r: &r}, database, slow)

	ran := false
	err := coordinator(build(t, g, top), patience).Exec(context.Background(), func(context.Context) error {
		ran = true
		return nil
	})

	if !errors.Is(err, errBoom) {
		t.Fatalf("Exec = %v, want errBoom", err)
	}
	if err.Error() != "database: boom" {
		t.Errorf("Exec = %q, want the single error %q", err, "database: boom")
	}
	if errors.Is(err, context.Canceled) {
		t.Errorf("Exec = %v, kept the consequent cancellation", err)
	}
	if ran {
		t.Error("Exec ran fn after a start failure")
	}

	got := r.list()
	if len(got) != 7 || got[0] != "start base" ||
		!sameSet(got[1:3], "start database", "start slow") || got[3] != "slow cancelled" ||
		!sameSet(got[4:6], "stop database", "stop slow") || got[6] != "stop base" {
		t.Errorf("events = %q, want base, {database slow}, slow cancelled, "+
			"then {database slow} and base shut down, top never started", got)
	}
}

func TestShutdownErrorsAreLabelledJoinedAndPrefixed(t *testing.T) {
	errA, errB := errors.New("a stuck"), errors.New("b stuck")
	newGraph := func(r *recorder) *graph.System {
		g := graph.New()
		a := node(g, &fake{name: "a", r: r, stop: func(context.Context) error { return errA }})
		b := node(g, &fake{name: "b", r: r, stop: func(context.Context) error { return errB }}, a)
		return build(t, g, b)
	}

	t.Run("fails a clean run", func(t *testing.T) {
		var r recorder
		err := coordinator(newGraph(&r), patience).Exec(context.Background(), noop)
		if !errors.Is(err, errA) || !errors.Is(err, errB) {
			t.Fatalf("Exec = %v, want errA and errB", err)
		}
		if want := "shutdown: b: b stuck\na: a stuck"; err.Error() != want {
			t.Errorf("Exec = %q, want %q", err, want)
		}
	})

	t.Run("joins fn's error", func(t *testing.T) {
		var r recorder
		errFn := errors.New("fn failed")
		err := coordinator(newGraph(&r), patience).Exec(context.Background(), func(context.Context) error {
			return errFn
		})
		if !errors.Is(err, errFn) || !errors.Is(err, errA) || !errors.Is(err, errB) {
			t.Fatalf("Exec = %v, want errFn, errA and errB", err)
		}
		want := []string{"start a", "start b", "stop b", "stop a"}
		if got := r.list(); !slices.Equal(got, want) {
			t.Errorf("events = %q, want %q", got, want)
		}
	})

	t.Run("fails Run's clean stop", func(t *testing.T) {
		var r recorder
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			for !slices.Contains(r.list(), "start b") {
				time.Sleep(time.Millisecond)
			}
			cancel()
		}()
		err := coordinator(newGraph(&r), patience).Run(ctx)
		if !errors.Is(err, errA) || !errors.Is(err, errB) ||
			!strings.HasPrefix(err.Error(), "shutdown: ") {
			t.Fatalf("Run = %v, want the shutdown errors", err)
		}
	})
}

func TestShutdownContextIsDetachedAndBounded(t *testing.T) {
	var r recorder
	const timeout = time.Minute
	var (
		stopErr  error
		deadline time.Time
		bounded  bool
	)
	g := graph.New()
	a := node(g, &fake{name: "a", r: &r, stop: func(ctx context.Context) error {
		stopErr = ctx.Err()
		deadline, bounded = ctx.Deadline()
		return nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	begun := time.Now()

	err := coordinator(build(t, g, a), timeout).Exec(ctx, func(ctx context.Context) error {
		cancel()
		return ctx.Err()
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Exec = %v, want fn's cancellation", err)
	}
	if !slices.Contains(r.list(), "stop a") {
		t.Fatal("a was not shut down after its context was cancelled mid-fn")
	}
	if stopErr != nil {
		t.Errorf("shutdown context err = %v, want live", stopErr)
	}
	if !bounded || deadline.Before(begun.Add(timeout)) || deadline.After(time.Now().Add(timeout)) {
		t.Errorf("shutdown deadline = %v (set %v), want ShutdownTimeout from shutdown", deadline, bounded)
	}
}

func TestShutdownOverrunStillAttemptsLowerLayers(t *testing.T) {
	var r recorder
	release := make(chan struct{})
	defer close(release)
	baseStopped := make(chan struct{})
	errTop := errors.New("top failed")
	g := graph.New()
	base := node(g, &fake{name: "base", r: &r, stop: func(context.Context) error {
		close(baseStopped)
		return nil
	}})
	hang := node(g, &fake{name: "hang", r: &r, stop: func(context.Context) error {
		<-release
		return errors.New("late")
	}}, base)
	top := node(g, &fake{name: "top", r: &r, stop: func(context.Context) error { return errTop }}, hang)

	err := coordinator(build(t, g, top), 20*time.Millisecond).Exec(context.Background(), noop)
	// Past the deadline shutdown no longer waits on a layer, so base's
	// shutdown may begin after Exec returns; it must begin.
	await(t, baseStopped, "base's shutdown")

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Exec = %v, want a deadline error", err)
	}
	if !errors.Is(err, errTop) {
		t.Errorf("Exec = %v, want errTop from the layer before the overrun", err)
	}
	if strings.Contains(err.Error(), "late") {
		t.Errorf("Exec = %v, kept the straggler's late error", err)
	}
	if n := strings.Count(err.Error(), context.DeadlineExceeded.Error()); n != 1 {
		t.Errorf("Exec = %v, want exactly one deadline error, got %d", err, n)
	}
	want := []string{"start base", "start hang", "start top", "stop top", "stop hang", "stop base"}
	if got := r.list(); !slices.Equal(got, want) {
		t.Errorf("events = %q, want %q", got, want)
	}
}

func TestCoordinatorRunsOnce(t *testing.T) {
	for _, tc := range []struct {
		name          string
		first, second func(*lifecycle.Coordinator)
		want          string
	}{
		{"Exec twice", exec, exec, "Exec"},
		{"Run after Exec", exec, run, "Run"},
		{"Exec after Run", run, exec, "Exec"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r recorder
			g := graph.New()
			c8r := coordinator(build(t, g, node(g, &fake{name: "a", r: &r})), patience)
			tc.first(c8r)
			mustPanic(t, tc.want, func() { tc.second(c8r) })
		})
	}
}

func exec(c *lifecycle.Coordinator) { _ = c.Exec(context.Background(), noop) }

func run(c *lifecycle.Coordinator) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = c.Run(ctx)
}

func TestNewPanicsOnWiringMistakes(t *testing.T) {
	g := graph.New()
	sys := build(t, g)
	mustPanic(t, "not positive", func() { lifecycle.New(sys, lifecycle.Config{}) })
	mustPanic(t, "nil System", func() {
		lifecycle.New(nil, lifecycle.Config{ShutdownTimeout: config.Duration(time.Second)})
	})
}
