//go:build integration

package app_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/standards-lab/go-core/process"
	godatabase "github.com/standards-lab/go-database"
	"github.com/standards-lab/go-database/postgres"
	"github.com/standards-lab/go-storage"
	"github.com/standards-lab/go-storage/azureblob"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/JaimeStill/spike-s3-storage/app/graph"
	"github.com/JaimeStill/spike-s3-storage/app/internal/app"
	"github.com/JaimeStill/spike-s3-storage/app/internal/apptest"
	"github.com/JaimeStill/spike-s3-storage/app/lifecycle"
)

// The real database and store against the compose stack: `mise run
// integration` starts its own isolated project, sets BLOBFS_DATABASE_* and
// BLOBFS_STORAGE_* to that project's services, leaves
// BLOBFS_SHUTDOWN_TIMEOUT at its default, and tears the project down when
// the suite ends.

// probeApp returns blobfs with a test-only command, "probe", added at its
// root, that brings up a real database and a real store, which no
// production command combines yet. A node takes part in the lifecycle only
// through its value's methods, so the probe does not declare the production
// database and store nodes, whose values the lifecycle would start
// directly: it defines two test-only nodes, "recorded database" and
// "recorded store", each of which builds its value as the production node
// does, from the production configuration nodes, so from the variables
// `mise run integration` sets, and wraps it in a recorded value that runs
// its Start and Shutdown and records each into r. The recorded store is
// ordered after the recorded database, so the two, one layer in
// production, start and shut down in an order the test can assert.
func probeApp(r *apptest.Recorder, stdout, stderr *bytes.Buffer) *app.App {
	a := app.New(cli.Streams{Stdin: strings.NewReader(""), Stdout: stdout, Stderr: stderr})
	g, n := a.Graph(), a.Nodes()
	database := g.Define("recorded database", func(s *graph.Scope) (*recorded, error) {
		db, err := postgres.New(s.Use(n.DatabaseConfig))
		if err != nil {
			return nil, err
		}
		return &recorded{name: "database", value: db, r: r}, nil
	})
	store := g.Define("recorded store", func(s *graph.Scope) (*recorded, error) {
		s.After(database)
		cfg := s.Use(n.StorageConfig)
		client, err := azureblob.New(cfg)
		if err != nil {
			return nil, err
		}
		return &recorded{name: "store", value: storage.New(client, cfg), r: r}, nil
	})
	a.Root().Add((&cli.Command{
		Name:    "probe",
		Summary: "Start and shut down the database and the store",
		Args:    cli.NoArgs,
		Run:     func(context.Context, *cli.Invocation) error { return nil },
	}).Use(database, store))
	return a
}

// recorded is a Subsystem that runs value's own Start and Shutdown and
// records each on r as "start name" or "shutdown name", with " failed" on
// an error.
type recorded struct {
	name  string
	value lifecycle.Subsystem
	r     *apptest.Recorder
}

func (v *recorded) Start(ctx context.Context) error {
	err := v.value.Start(ctx)
	v.r.Record(event("start "+v.name, err))
	return err
}

func (v *recorded) Shutdown(ctx context.Context) error {
	err := v.value.Shutdown(ctx)
	v.r.Record(event("shutdown "+v.name, err))
	return err
}

func event(name string, err error) string {
	if err != nil {
		return name + " failed"
	}
	return name
}

// oneReport fails t unless stderr is one report whose first line starts
// with prefix: no later line starts a report of its own.
func oneReport(t *testing.T, stderr, prefix string) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
	if !strings.HasPrefix(lines[0], prefix) {
		t.Errorf("stderr = %q, want its first line under %q", stderr, prefix)
	}
	for _, line := range lines[1:] {
		if strings.HasPrefix(line, "blobfs ") {
			t.Errorf("stderr = %q, want one report: line %q starts another", stderr, line)
		}
	}
	if !strings.Contains(stderr, "connection refused") {
		t.Errorf("stderr = %q, want the refused connection", stderr)
	}
}

func TestInfrastructureIntegration_StartsBothAndShutsDownInReverse(t *testing.T) {
	r := &apptest.Recorder{}
	var out, errOut bytes.Buffer

	code := probeApp(r, &out, &errOut).Run(context.Background(), []string{"probe"})

	if code != process.ExitOK {
		t.Fatalf("code = %d, want %d; stderr = %q", code, process.ExitOK, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want empty", errOut.String())
	}
	want := []string{"start database", "start store", "shutdown store", "shutdown database"}
	if got := r.Log(); !slices.Equal(got, want) {
		t.Errorf("events = %q, want %q", got, want)
	}
}

func TestInfrastructureIntegration_StoreUnreachableClosesTheDatabase(t *testing.T) {
	env := storage.NewEnv("BLOBFS")
	t.Setenv(env.Endpoint, fmt.Sprintf("http://127.0.0.1:%s/devstoreaccount1", strconv.Itoa(closedPort(t))))
	t.Setenv(env.RequestTimeout, "5s")
	// One try: the SDK's default retries back off for seconds against a
	// port that refuses at once.
	t.Setenv(env.Options+"_MAX_RETRIES", "0")
	r := &apptest.Recorder{}
	var out, errOut bytes.Buffer

	code := probeApp(r, &out, &errOut).Run(context.Background(), []string{"probe"})

	if code != process.ExitFailure {
		t.Errorf("code = %d, want %d", code, process.ExitFailure)
	}
	oneReport(t, errOut.String(), "blobfs probe: recorded store: ")
	// The store, constructed though it failed to start, is shut down, and
	// then the database it started after.
	want := []string{"start database", "start store failed", "shutdown store", "shutdown database"}
	if got := r.Log(); !slices.Equal(got, want) {
		t.Errorf("events = %q, want %q", got, want)
	}
}

func TestInfrastructureIntegration_DatabaseUnreachable(t *testing.T) {
	env := godatabase.NewEnv("BLOBFS")
	t.Setenv(env.Host, "127.0.0.1")
	t.Setenv(env.Port, strconv.Itoa(closedPort(t)))
	t.Setenv(env.ConnTimeout, "2s")
	r := &apptest.Recorder{}
	var out, errOut bytes.Buffer

	code := probeApp(r, &out, &errOut).Run(context.Background(), []string{"probe"})

	if code != process.ExitFailure {
		t.Errorf("code = %d, want %d", code, process.ExitFailure)
	}
	// pgx's own continuation lines list each dial attempt, indented by a
	// tab, after the dispatcher's line.
	oneReport(t, errOut.String(), "blobfs probe: recorded database: ")
	// The store's layer never began to start, so only the database is
	// shut down.
	want := []string{"start database failed", "shutdown database"}
	if got := r.Log(); !slices.Equal(got, want) {
		t.Errorf("events = %q, want %q", got, want)
	}
}

// closedPort returns a loopback port nothing listens on: one the kernel
// just handed out and that this test has closed again.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}
