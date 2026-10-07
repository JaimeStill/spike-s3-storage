package app_test

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/standards-lab/go-core/process"
	godatabase "github.com/standards-lab/go-database"
	"github.com/standards-lab/go-storage"
	"github.com/standards-lab/go-storage/storagetest"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/JaimeStill/spike-s3-storage/app/internal/apptest"
)

// The scenario parent driven through App.Run over buffers: its help and
// the root's, which end with the same listing and build nothing, and the
// tours, each building the nodes it declares. The narration is the
// scenario package's to test; the full tours run against the stack in the
// integration package, over the built binary.

// listing is the scenario listing over the App's files and storage nodes.
const listing = "" +
	"Scenarios:\n" +
	"  directories   Tour the directory commands on Postgres alone: mkdir, ls, stat, mv, rmdir\n" +
	"                uses files\n" +
	"  files         Tour the object commands on Postgres and the store: put, cat, cp, rm, rm --recursive\n" +
	"                uses files, storage\n"

func TestScenario_AloneHelpEndsWithTheListingAndBuildsNothing(t *testing.T) {
	clearEnv(t)
	var out, errOut bytes.Buffer
	a, built := haltedApp(&out, &errOut)

	code := a.Run(context.Background(), []string{"scenario"})

	if code != process.ExitUsage {
		t.Errorf("code = %d, want %d; stderr = %q", code, process.ExitUsage, errOut.String())
	}
	for _, want := range []string{"Usage:\n  blobfs scenario <command> [flags]\n", "Commands:\n", "  directories ", "  files "} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("scenario help lacks %q:\n%s", want, out.String())
		}
	}
	if !strings.HasSuffix(out.String(), "\n\n"+listing) {
		t.Errorf("scenario help:\n%s\nwant it to end with:\n%s", out.String(), listing)
	}
	if got := built.Log(); len(got) != 0 {
		t.Errorf("nodes built = %q, want none", got)
	}
}

func TestRootHelp_EndsWithTheScenarioListingAndBuildsNothing(t *testing.T) {
	tail := "\nRun 'blobfs <command> --help' for help on a command.\n\n" +
		"A path argument may instead be id:<uuid>, naming the directory or file by its id.\n\n" +
		listing
	tests := []struct {
		name   string
		args   []string
		stderr bool // whether the help goes to stderr
	}{
		{"--help", []string{"--help"}, false},
		{"no command", nil, false},
		{"unknown command", []string{"bogus"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			var out, errOut bytes.Buffer
			a, built := haltedApp(&out, &errOut)

			code := a.Run(context.Background(), tt.args)

			if code != process.ExitUsage {
				t.Errorf("code = %d, want %d", code, process.ExitUsage)
			}
			help, other := out.String(), errOut.String()
			if tt.stderr {
				help, other = other, help
			}
			if !strings.HasSuffix(help, tail) {
				t.Errorf("help:\n%s\nwant it to end with:\n%s", help, tail)
			}
			if other != "" {
				t.Errorf("other stream = %q, want empty", other)
			}
			if got := built.Log(); len(got) != 0 {
				t.Errorf("nodes built = %q, want none", got)
			}
		})
	}
}

func TestRootHelp_ExplainsTheIDOperandOnce(t *testing.T) {
	// The root's help states the id:<uuid> operand once, ahead of the
	// scenario listing; a command's own help, whose synopsis writes
	// <path|id:<uuid>>, does not repeat it, since the root's footer is not
	// inherited.
	const line = "A path argument may instead be id:<uuid>, naming the directory or file by its id.\n"

	_, root, _ := run(t)
	_, ls, _ := run(t, "ls", "--help")

	if n := strings.Count(root, line); n != 1 || strings.Index(root, line) > strings.Index(root, "Scenarios:") {
		t.Errorf("root help:\n%s\nwant %q once, before the scenario listing", root, line)
	}
	if strings.Contains(ls, "id:<uuid>, naming") {
		t.Errorf("ls help:\n%s\nwant no root footer", ls)
	}
}

func TestRootHelp_ScenarioListingIsTheRootsAndTheParentsOwn(t *testing.T) {
	for _, args := range [][]string{{"scenario", "directories", "--help"}, {"schema"}, {"version", "--help"}} {
		code, out, errOut := run(t, args...)

		if code != process.ExitUsage {
			t.Errorf("%v: code = %d, want %d", args, code, process.ExitUsage)
		}
		if !strings.Contains(out, "Usage:") {
			t.Errorf("%v: stdout:\n%s\nwant the command's help", args, out)
		}
		if strings.Contains(out+errOut, "Scenarios:") {
			t.Errorf("%v: stdout:\n%s\nstderr:\n%s\nwant no scenario listing", args, out, errOut)
		}
	}
}

func TestScenario_HelpAndRefusalsBuildNothing(t *testing.T) {
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
			clearEnv(t)
			var out, errOut bytes.Buffer
			a, built := haltedApp(&out, &errOut)

			code := a.Run(context.Background(), tt.args)

			if code != process.ExitUsage {
				t.Errorf("code = %d, want %d; stderr = %q", code, process.ExitUsage, errOut.String())
			}
			if got := built.Log(); len(got) != 0 {
				t.Errorf("nodes built = %q, want none", got)
			}
		})
	}
}

// buildsApp is haltedApp with the store built over a Fake, by
// apptest.FakeStore, and the database's name set, so a run's Build
// constructs, with production constructors elsewhere, everything the tour
// declares, and goes as far as the lifecycle configuration, which halts it
// before anything starts. It returns a run of the App and the Recorder of
// the nodes its runs build.
func buildsApp(t *testing.T, stdout, stderr *bytes.Buffer) (func(args ...string) int, *apptest.Recorder) {
	t.Helper()
	clearEnv(t)
	t.Setenv(godatabase.NewEnv("BLOBFS").Name, "app")
	a, built := haltedApp(stdout, stderr)
	apptest.FakeStore(t, a, storagetest.NewFake())
	return func(args ...string) int { return a.Run(context.Background(), args) }, built
}

func TestScenario_EachTourBuildsTheNodesItDeclares(t *testing.T) {
	tests := []struct {
		tour string
		want []string
	}{
		// The files node alone: Postgres, and never the store or its
		// configuration, which a reach of either would record.
		{"directories", []string{"files", "sql", "database", "database config", "lifecycle config"}},
		// The files and storage nodes: Postgres and the store.
		{"files", []string{"files", "sql", "database", "database config", "storage", "store", "lifecycle config"}},
	}
	for _, tt := range tests {
		t.Run(tt.tour, func(t *testing.T) {
			var out, errOut bytes.Buffer
			run, built := buildsApp(t, &out, &errOut)

			code := run("scenario", tt.tour)

			if code != process.ExitFailure {
				t.Errorf("code = %d, want %d", code, process.ExitFailure)
			}
			if want := "blobfs scenario " + tt.tour + ": lifecycle config: halted\n"; errOut.String() != want {
				t.Errorf("stderr = %q, want %q", errOut.String(), want)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want nothing narrated before the start", out.String())
			}
			if got := built.Log(); !slices.Equal(got, tt.want) {
				t.Errorf("nodes built = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestScenario_FilesWithTheStoreDownFailsOnceNamingTheStore(t *testing.T) {
	fake := storagetest.NewFake()
	fake.SetDown(true)
	var out, errOut bytes.Buffer
	rec, run, ping := objectApp(t, fake, strings.NewReader(""), &out, &errOut)

	code := run("scenario", "files")

	if code != process.ExitFailure {
		t.Errorf("code = %d, want %d", code, process.ExitFailure)
	}
	prefix := "blobfs scenario files: store: "
	if !strings.HasPrefix(errOut.String(), prefix) || strings.Count(errOut.String(), "\n") != 1 {
		t.Errorf("stderr = %q, want one line starting %q", errOut.String(), prefix)
	}
	if !strings.Contains(errOut.String(), storage.ErrUnavailable.Error()) {
		t.Errorf("stderr = %q, want the store's unavailability", errOut.String())
	}
	if out.Len() != 0 || fake.Puts() != 0 {
		t.Errorf("stdout = %q, puts %d: want no step narrated or run", out.String(), fake.Puts())
	}
	if ops := rec.Ops(); slices.ContainsFunc(ops, func(op sqltest.Op) bool { return op != sqltest.OpPrepare }) {
		t.Errorf("ops = %v, want prepares only", ops)
	}
	if err := ping(); err == nil {
		t.Error("the database's pool answers a ping after the run, want it closed")
	}
}
