package app_test

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/standards-lab/go-core/process"
	godatabase "github.com/standards-lab/go-database"
)

// The nodes the schema group declares with Use, driven through App.Run.
// The stack-backed tests are in schema_integration_test.go.

// schemaVerbs are the schema group's verbs, each as a run that reaches its
// body.
var schemaVerbs = [][]string{
	{"schema", "status"},
	{"schema", "up"},
	{"schema", "down"},
	{"schema", "reset", "--yes"},
}

func TestSchema_VerbsBuildTheDatabaseAndNeverTheStore(t *testing.T) {
	for _, args := range schemaVerbs {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			clearEnv(t)
			t.Setenv(godatabase.NewEnv("BLOBFS").Name, "app")
			var out, errOut bytes.Buffer
			// The production constructors run, and none does I/O; the
			// lifecycle configuration, the Build's last root, is halted, so
			// the Build constructs the nodes the verb's path declares and
			// everything they reach, and stops before anything starts: a
			// store or storage configuration they reached would be recorded.
			a, built := haltedApp(&out, &errOut)

			code := a.Run(context.Background(), args)

			if code != process.ExitFailure {
				t.Errorf("code = %d, want %d", code, process.ExitFailure)
			}
			want := "blobfs " + strings.Join(args[:2], " ") + ": lifecycle config: halted\n"
			if errOut.String() != want {
				t.Errorf("stderr = %q, want %q", errOut.String(), want)
			}
			wantBuilt := []string{"migrator", "sql", "database", "database config", "lifecycle config"}
			if got := built.Log(); !slices.Equal(got, wantBuilt) {
				t.Errorf("nodes built = %q, want %q", got, wantBuilt)
			}
		})
	}
}

func TestSchema_FailsOnTheDatabaseConfig(t *testing.T) {
	// An empty environment leaves the database name unset.
	for _, args := range schemaVerbs {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			clearEnv(t)

			code, stdout, stderr := run(t, args...)

			if code != process.ExitFailure {
				t.Errorf("code = %d, want %d", code, process.ExitFailure)
			}
			// One report, under the verb's path.
			want := "blobfs " + strings.Join(args[:2], " ") + ": database config: database name required\n"
			if stderr != want {
				t.Errorf("stderr = %q, want %q", stderr, want)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
		})
	}
}

func TestSchema_ResetWithoutYesBuildsNothing(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantStderr string
	}{
		{
			name:       "no --yes",
			args:       []string{"schema", "reset"},
			wantStderr: "blobfs schema reset: required flag --yes not set\n",
		},
		{
			name:       "--yes=false",
			args:       []string{"schema", "reset", "--yes=false"},
			wantStderr: "blobfs schema reset: --yes=false does not confirm the reset\n",
		},
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
			if !strings.HasPrefix(errOut.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to start with %q", errOut.String(), tt.wantStderr)
			}
			if got := built.Log(); len(got) != 0 {
				t.Errorf("nodes built = %q, want none", got)
			}
		})
	}
}

func TestSchema_HelpListsTheVerbs(t *testing.T) {
	code, stdout, _ := run(t, "schema")

	if code != process.ExitUsage {
		t.Errorf("code = %d, want %d", code, process.ExitUsage)
	}
	want := "Commands:\n" +
		"  status   Show each set's head, latest version, pending migrations, and dirty mark\n" +
		"  up       Apply every pending migration, blobfs's set first and then the app's\n" +
		"  down     Revert every applied migration, the app's set first and then blobfs's; the history tables stay\n" +
		"  reset    Revert every set, the app's first, and drop the history tables; requires --yes\n"
	if !strings.Contains(stdout, want) {
		t.Errorf("stdout =\n%s\nwant it to contain\n%s", stdout, want)
	}
}
