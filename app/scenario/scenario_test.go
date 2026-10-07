package scenario_test

import (
	"bytes"
	"context"
	"database/sql/driver"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/standards-lab/blobfs"
	bfdata "github.com/standards-lab/blobfs/data"
	blobfspg "github.com/standards-lab/blobfs/postgres"
	"github.com/standards-lab/go-core/config"
	"github.com/standards-lab/go-core/process"
	"github.com/standards-lab/go-storage"
	"github.com/standards-lab/go-storage/storagetest"
	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/postgres"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/JaimeStill/spike-s3-storage/app/domain/files"
	"github.com/JaimeStill/spike-s3-storage/app/graph"
	"github.com/JaimeStill/spike-s3-storage/app/lifecycle"
	"github.com/JaimeStill/spike-s3-storage/app/scenario"
)

// The scenario parent driven through cli.Run over buffers, mounted under a
// stub root over a graph of its own: the files node a Service over sqlate's
// scripted driver, the storage node a Storage over that Service and a store
// over a storagetest.Fake. The full tours run against the stack in the
// integration package, over the built binary.

// stack is the program the tests run: its root, the graph and the nodes
// Commands is given, and what a run builds and queries.
type stack struct {
	root  *cli.Command
	g     *graph.Graph
	cfg   *graph.Node[lifecycle.Config]
	svc   *graph.Node[*files.Service]
	st    *graph.Node[*files.Storage]
	built []string
	rec   *sqltest.Recorder
	fake  *storagetest.Fake
}

// newStack returns the program with its database answering with
// responses, every node's construction recorded in built.
func newStack(t *testing.T, responses ...sqltest.Response) *stack {
	t.Helper()
	pool, rec := sqltest.Open(t, responses...)
	s := &stack{g: graph.New(), rec: rec, fake: storagetest.NewFake()}
	s.g.Observe(func(name string) { s.built = append(s.built, name) })
	s.cfg = s.g.Define("lifecycle config", func(*graph.Scope) (lifecycle.Config, error) {
		return lifecycle.Config{ShutdownTimeout: config.Duration(5 * time.Second)}, nil
	})
	s.svc = s.g.Define("files", func(*graph.Scope) (*files.Service, error) {
		return files.New(sqlate.Wrap(pool, postgres.Dialect{}), bfdata.WithEngine(blobfspg.Engine))
	})
	store := s.g.Define("store", func(*graph.Scope) (*storage.Store, error) {
		cfg := storage.Config{Container: "objects"}
		if err := cfg.Finalize("SCENARIOTEST"); err != nil {
			return nil, err
		}
		return storage.New(s.fake, cfg), nil
	})
	s.st = s.g.Define("storage", func(sc *graph.Scope) (*files.Storage, error) {
		return files.NewStorage(sc.Use(s.svc), sc.Use(store)), nil
	})
	s.root = (&cli.Command{Name: "prog"}).Add(scenario.Commands(s.svc, s.st)...)
	return s
}

func (s *stack) run(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = cli.Run(context.Background(), s.root, args, cli.Streams{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut}, cli.WithGraph(s.g, s.cfg))
	return code, out.String(), errOut.String()
}

// listing is the scenario listing over the stack's nodes.
const listing = "" +
	"Scenarios:\n" +
	"  directories   Tour the directory commands on Postgres alone: mkdir, ls, stat, mv, rmdir\n" +
	"                uses files\n" +
	"  files         Tour the object commands on Postgres and the store: put, cat, cp, rm, rm --recursive\n" +
	"                uses files, storage\n"

func TestCommands_AloneHelpListsTheToursAndEndsWithTheListing(t *testing.T) {
	for _, args := range [][]string{{"scenario"}, {"scenario", "--help"}} {
		s := newStack(t)

		code, out, errOut := s.run(args...)

		if code != process.ExitUsage {
			t.Errorf("%v: code = %d, want %d", args, code, process.ExitUsage)
		}
		// The Commands section and the listing pad the name column to one
		// width, so each tour's line starts the same in both.
		for _, want := range []string{"Usage:\n  prog scenario <command> [flags]\n", "Commands:\n  directories   Tour the directory commands", "\n  files         Tour the object commands"} {
			if !strings.Contains(out, want) {
				t.Errorf("%v: help lacks %q:\n%s", args, want, out)
			}
		}
		if strings.Count(out, "  directories   Tour ") != 2 || strings.Count(out, "  files         Tour ") != 2 {
			t.Errorf("%v: help:\n%s\nwant each tour's line padded alike in Commands and Scenarios", args, out)
		}
		if !strings.HasSuffix(out, "\n\n"+listing) {
			t.Errorf("%v: help:\n%s\nwant it to end with:\n%s", args, out, listing)
		}
		if errOut != "" {
			t.Errorf("%v: stderr = %q, want empty", args, errOut)
		}
		if len(s.built) != 0 {
			t.Errorf("%v: built = %q, want nothing", args, s.built)
		}
	}
}

func TestWriteListing_WritesTheParentsFooter(t *testing.T) {
	s := newStack(t)
	var out bytes.Buffer

	scenario.WriteListing(&out, s.svc, s.st)

	if out.String() != listing {
		t.Errorf("WriteListing wrote:\n%s\nwant:\n%s", out.String(), listing)
	}
	if len(s.built) != 0 {
		t.Errorf("built = %q, want nothing", s.built)
	}
}

func TestCommands_HelpAndRefusalsBuildNothing(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"unknown tour", []string{"scenario", "bogus"}},
		{"directories --help", []string{"scenario", "directories", "--help"}},
		{"files --help", []string{"scenario", "files", "--help"}},
		{"directories with an argument", []string{"scenario", "directories", "extra"}},
		{"files with an unknown flag", []string{"scenario", "files", "--bogus"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newStack(t)

			code, _, errOut := s.run(tt.args...)

			if code != process.ExitUsage {
				t.Errorf("code = %d, want %d; stderr = %q", code, process.ExitUsage, errOut)
			}
			if len(s.built) != 0 {
				t.Errorf("built = %q, want nothing", s.built)
			}
		})
	}
}

func TestCommands_AToursHelpIsItsOwn(t *testing.T) {
	s := newStack(t)

	code, out, _ := s.run("scenario", "files", "--help")

	if code != process.ExitUsage {
		t.Errorf("code = %d, want %d", code, process.ExitUsage)
	}
	want := "Tour the object commands on Postgres and the store: put, cat, cp, rm, rm --recursive\n\nUsage:\n  prog scenario files [flags]\n"
	if !strings.HasPrefix(out, want) {
		t.Errorf("help:\n%s\nwant it to start:\n%s", out, want)
	}
	if strings.Contains(out, "Scenarios:") {
		t.Errorf("help:\n%s\nwant no listing: the footer is the parent's", out)
	}
}

var directoryColumns = []string{"id", "parent_id", "name", "status", "version", "created_at", "updated_at"}

// notFound is the Postgres engine's path resolution of a path that does
// not exist.
func notFound() sqltest.Response {
	return sqltest.Response{Columns: append(slices.Clone(directoryColumns), "depth")}
}

func TestDirectories_NarratesEachStepBeforeDoingItAndBuildsTheFilesNodeAlone(t *testing.T) {
	// The first step's resolution of the working area finds nothing, so
	// the step notes there is nothing to clear; the second step's first
	// query is unscripted, so it fails after its note.
	s := newStack(t, notFound())

	code, out, errOut := s.run("scenario", "directories")

	if code != process.ExitFailure {
		t.Errorf("code = %d, want %d", code, process.ExitFailure)
	}
	// Each step's note is wrapped at 80 columns, every line indented
	// under its heading.
	want := "" +
		"[1/11] Clear /scenario-directories if an earlier run left it behind\n" +
		"  Nothing to clear: /scenario-directories does not exist, so this run starts\n" +
		"  clean.\n" +
		"\n" +
		"[2/11] Create /scenario-directories as the scenario unit's: mkdir --unit\n" +
		"  A directory created with a unit is top-level, and its owner row is written in\n" +
		"  the same transaction, so the unit's scope starts here.\n"
	if out != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", out, want)
	}
	prefix := "prog scenario directories: step 2 (Create /scenario-directories as the scenario unit's: mkdir --unit): "
	if !strings.HasPrefix(errOut, prefix) || !strings.Contains(errOut, sqltest.ErrUnscripted.Error()) || strings.Count(errOut, "\n") != 1 {
		t.Errorf("stderr = %q, want one line starting %q and carrying the failure", errOut, prefix)
	}
	if n := s.rec.Pending(); n != 0 {
		t.Errorf("%d scripted responses unconsumed", n)
	}
	if want := []string{"files", "lifecycle config"}; !slices.Equal(s.built, want) {
		t.Errorf("built = %q, want %q: neither the storage nor the store", s.built, want)
	}
}

func TestFiles_ShowsAStepsResultIndentedUnderItsProse(t *testing.T) {
	const areaID = "00000000-0000-7000-8000-0000000000f1"
	// Nothing to clear; then mkdir of the working area resolves / and
	// inserts it, and mkdir of docs, unscripted, fails the step after the
	// first result is shown.
	s := newStack(t,
		notFound(),
		sqltest.Response{
			Columns: append(slices.Clone(directoryColumns), "depth"),
			Rows:    [][]driver.Value{{blobfs.RootID, nil, "/", "active", int64(1), time.Time{}, time.Time{}, int64(0)}},
		},
		sqltest.Response{Columns: directoryColumns, Rows: [][]driver.Value{
			{areaID, blobfs.RootID, "scenario-files", "active", int64(1), time.Time{}, time.Time{}},
		}},
	)

	code, out, errOut := s.run("scenario", "files")

	if code != process.ExitFailure {
		t.Errorf("code = %d, want %d", code, process.ExitFailure)
	}
	want := "" +
		"[1/9] Clear /scenario-files if an earlier run left it behind\n" +
		"  Nothing to clear: /scenario-files does not exist, so this run starts clean.\n" +
		"\n" +
		"[2/9] Create /scenario-files and /scenario-files/docs: mkdir\n" +
		"    mkdir: /scenario-files (id " + areaID + ")\n"
	if out != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", out, want)
	}
	if prefix := "prog scenario files: step 2 (Create /scenario-files and /scenario-files/docs: mkdir): "; !strings.HasPrefix(errOut, prefix) {
		t.Errorf("stderr = %q, want it to start %q", errOut, prefix)
	}
	if want := []string{"files", "storage", "store", "lifecycle config"}; !slices.Equal(s.built, want) {
		t.Errorf("built = %q, want %q", s.built, want)
	}
}

func TestFiles_WithTheStoreDownFailsAtStartNamingTheStore(t *testing.T) {
	s := newStack(t)
	s.fake.SetDown(true)

	code, out, errOut := s.run("scenario", "files")

	if code != process.ExitFailure {
		t.Errorf("code = %d, want %d", code, process.ExitFailure)
	}
	prefix := "prog scenario files: store: "
	if !strings.HasPrefix(errOut, prefix) || strings.Count(errOut, "\n") != 1 {
		t.Errorf("stderr = %q, want one line starting %q", errOut, prefix)
	}
	if out != "" || s.fake.Puts() != 0 {
		t.Errorf("stdout = %q, puts %d: want no step narrated or run", out, s.fake.Puts())
	}
}
