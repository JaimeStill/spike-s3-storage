package apptest

import (
	"database/sql"
	"errors"
	"slices"
	"sync"
	"testing"

	godatabase "github.com/standards-lab/go-database"
	"github.com/standards-lab/go-storage"
	"github.com/standards-lab/go-storage/storagetest"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/JaimeStill/spike-s3-storage/app/graph"
	"github.com/JaimeStill/spike-s3-storage/app/internal/app"
	"github.com/JaimeStill/spike-s3-storage/app/lifecycle"
)

// envPrefix is the prefix the substitutes finalize their configuration
// under. Nothing sets it, so they read the defaults whatever blobfs's own
// variables hold.
const envPrefix = "APPTEST"

// Recorder records events in order. It is safe for concurrent use, as a
// value's Start or Shutdown may record from its own goroutine. The zero
// value is ready to use.
type Recorder struct {
	mu     sync.Mutex
	events []string
}

// Record appends event.
func (r *Recorder) Record(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

// Log returns a copy of the events recorded so far.
func (r *Recorder) Log() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

// Builds returns a Recorder of the name of each node a's runs build, in
// the order each Build begins them: depth-first from the roots, each node
// once per Build, a node whose constructor fails included. A run that
// builds nothing records nothing.
func Builds(a *app.App) *Recorder {
	r := &Recorder{}
	a.Graph().Observe(r.Record)
	return r
}

// ErrHalted is what a run of an App given to [Halt] fails with, labelled
// "lifecycle config".
var ErrHalted = errors.New("halted")

// Halt makes the lifecycle configuration node fail with ErrHalted. The
// dispatcher adds that node to every Build as its last root, so a run of a
// command that declares nodes constructs all of them and then fails before
// anything starts, and a run that does no I/O in its constructors does none
// at all.
func Halt(a *app.App) {
	a.Graph().Replace(a.Nodes().LifecycleConfig, func(*graph.Scope) (lifecycle.Config, error) {
		return lifecycle.Config{}, ErrHalted
	})
}

// ScriptDatabase makes a's database node a go-database DB over a pool on
// sqlate's scripted driver, answering with responses, and returns the
// driver's recorder and the pool, which is closed when the database node's
// value shuts down or the test ends. The substitute reads no configuration
// node, so a run builds no database configuration.
func ScriptDatabase(t testing.TB, a *app.App, responses ...sqltest.Response) (*sqltest.Recorder, *sql.DB) {
	t.Helper()
	pool, rec := sqltest.Open(t, responses...)
	a.Graph().Replace(a.Nodes().Database, func(*graph.Scope) (*godatabase.DB, error) {
		cfg := godatabase.Config{Name: "app"}
		if err := cfg.Finalize(envPrefix); err != nil {
			return nil, err
		}
		return godatabase.New(pool, cfg), nil
	})
	return rec, pool
}

// FakeStore makes a's store node go-storage's Store over fake, not
// started: the lifecycle starts it as it starts the production store, and
// a fake that is down fails that start. The substitute reads no
// configuration node, so a run builds no storage configuration.
func FakeStore(t testing.TB, a *app.App, fake *storagetest.Fake) {
	t.Helper()
	a.Graph().Replace(a.Nodes().Store, func(*graph.Scope) (*storage.Store, error) {
		cfg := storage.Config{Container: "objects"}
		if err := cfg.Finalize(envPrefix); err != nil {
			return nil, err
		}
		return storage.New(fake, cfg), nil
	})
}
