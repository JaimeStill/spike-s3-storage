package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/JaimeStill/spike-s3-storage/app/graph"
)

// Coordinator runs one built [graph.System]: it starts the System's
// subsystems layer by layer, runs or serves, and shuts them down in reverse.
// A Coordinator runs once.
type Coordinator struct {
	sys     *graph.System
	timeout time.Duration
	ran     atomic.Bool
}

// New returns a Coordinator for sys, shutting down within
// cfg.ShutdownTimeout. It panics on a nil sys or a ShutdownTimeout that is
// not positive, as an unfinalized Config's is: both are wiring mistakes.
func New(sys *graph.System, cfg Config) *Coordinator {
	if sys == nil {
		panic("lifecycle: New: nil System")
	}
	if cfg.ShutdownTimeout <= 0 {
		panic(fmt.Sprintf(
			"lifecycle: New: shutdown timeout %v is not positive; finalize the Config",
			cfg.ShutdownTimeout,
		))
	}
	return &Coordinator{sys: sys, timeout: cfg.ShutdownTimeout.Duration()}
}

// Exec starts the System, runs fn under ctx, and shuts the System down. It
// returns fn's error joined with the shutdown's. When startup fails, or ctx
// ends before startup completes, fn does not run and Exec returns the
// startup error, or ctx's, joined with the shutdown's. A second call to Exec
// or [Coordinator.Run] panics.
func (c *Coordinator) Exec(ctx context.Context, fn func(context.Context) error) error {
	c.claim("Exec")
	var e engine
	if errs := c.startup(ctx, &e); len(errs) > 0 {
		return errors.Join(errors.Join(errs...), c.shutdown(&e))
	}
	return errors.Join(fn(ctx), c.shutdown(&e))
}

// Run starts the System, serves until ctx ends, and shuts the System down.
// The end of ctx is the clean stop, and so is a ctx that ends before or
// during startup: whatever started is shut down, and Run returns only the
// shutdown's error, nil when it is clean. A startup failure returns the
// startup error joined with the shutdown's. A second call to Run or
// [Coordinator.Exec] panics.
func (c *Coordinator) Run(ctx context.Context) error {
	c.claim("Run")
	var e engine
	if errs := c.startup(ctx, &e); len(errs) > 0 {
		if !cutShort(ctx, errs) {
			return errors.Join(errors.Join(errs...), c.shutdown(&e))
		}
		return c.shutdown(&e)
	}
	<-ctx.Done()
	return c.shutdown(&e)
}

// claim marks the Coordinator as run, panicking when it already was.
func (c *Coordinator) claim(op string) {
	if c.ran.Swap(true) {
		panic("lifecycle: " + op + " on a Coordinator that already ran")
	}
}

// startup starts the System's layers, lowest first, each as one engine
// phase, and returns the first failing layer's errors. A layer starts only
// when ctx is live: once ctx ends, startup returns ctx's error and starts
// nothing further.
func (c *Coordinator) startup(ctx context.Context, e *engine) []error {
	for _, layer := range c.sys.Layers() {
		if err := ctx.Err(); err != nil {
			return []error{err}
		}
		steps := participants(layer)
		if len(steps) == 0 {
			continue
		}
		if errs := e.start(ctx, steps); len(errs) > 0 {
			return errs
		}
	}
	return nil
}

// shutdown unwinds every phase startup pushed within the configured
// timeout, and returns the errors joined under "shutdown: ", or nil.
func (c *Coordinator) shutdown(e *engine) error {
	if errs := e.unwind(c.timeout); len(errs) > 0 {
		return fmt.Errorf("shutdown: %w", errors.Join(errs...))
	}
	return nil
}

// cutShort reports whether startup's errs are only the consequence of ctx
// ending: ctx is done and every error wraps its cause.
func cutShort(ctx context.Context, errs []error) bool {
	cause := ctx.Err()
	if cause == nil {
		return false
	}
	for _, err := range errs {
		if !errors.Is(err, cause) {
			return false
		}
	}
	return true
}

// participants returns the engine steps for one layer's dependencies, in
// layer order. A value takes part in each phase it implements: a [Starter]
// in startup, a [Stopper] in shutdown, and a value that is neither is
// skipped.
func participants(layer []graph.Dependency) []step {
	var steps []step
	for _, d := range layer {
		s := step{name: d.Name}
		if v, ok := d.Value.(Starter); ok {
			s.start = v.Start
		}
		if v, ok := d.Value.(Stopper); ok {
			s.stop = v.Shutdown
		}
		if s.start == nil && s.stop == nil {
			continue
		}
		steps = append(steps, s)
	}
	return steps
}
