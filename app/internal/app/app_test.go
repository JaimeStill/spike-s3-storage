package app_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/standards-lab/go-core/config"
	"github.com/standards-lab/go-core/process"
	godatabase "github.com/standards-lab/go-database"
	"github.com/standards-lab/go-storage"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/JaimeStill/spike-s3-storage/app/internal/app"
	"github.com/JaimeStill/spike-s3-storage/app/internal/apptest"
)

// These tests are hermetic. The ones that read configuration clear every
// variable blobfs reads first, with clearEnv, so the compose stack's
// defaults in mise's environment never reach them; the stack-backed tests
// are in the *_integration_test.go files.

// run drives blobfs with args over fresh buffers.
func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = app.New(cli.Streams{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut}).Run(context.Background(), args)
	return code, out.String(), errOut.String()
}

// clearEnv empties every variable blobfs reads, which go-database,
// go-storage, and lifecycle treat as unset, for the duration of t: the
// database's, the store's with every provider option under its options
// prefix, and the shutdown timeout.
func clearEnv(t *testing.T) {
	t.Helper()
	d := godatabase.NewEnv("BLOBFS")
	s := storage.NewEnv("BLOBFS")
	for _, name := range []string{
		d.Host, d.Name, d.User, d.Password, d.Port,
		d.MaxOpenConns, d.MaxIdleConns, d.ConnMaxLifetime, d.ConnMaxIdleTime, d.ConnTimeout,
		s.Endpoint, s.Container, s.Account, s.Key,
		s.MaxObjectSize, s.ListPageSize, s.RequestTimeout, s.ReadIdleTimeout,
		config.EnvName("BLOBFS", "shutdown_timeout"),
	} {
		t.Setenv(name, "")
	}
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, s.Options+"_") {
			t.Setenv(name, "")
		}
	}
}

// haltedApp returns blobfs over stdout and stderr with every run halted
// before anything starts, by apptest.Halt, and a Recorder of the nodes its
// runs build. Every other constructor is the production one, and none does
// I/O, so a run of a command that declares nodes builds them, fails with
// "lifecycle config: halted", and starts nothing.
func haltedApp(stdout, stderr *bytes.Buffer) (*app.App, *apptest.Recorder) {
	a := app.New(cli.Streams{Stdin: strings.NewReader(""), Stdout: stdout, Stderr: stderr})
	built := apptest.Builds(a)
	apptest.Halt(a)
	return a, built
}

func TestRun_BuildsNothingWithoutALeafThatDeclaresNodes(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
	}{
		{"no arguments", nil, process.ExitUsage},
		{"root --help", []string{"--help"}, process.ExitUsage},
		{"unknown command", []string{"bogus"}, process.ExitUsage},
		{"version", []string{"version"}, process.ExitOK},
		{"version --help", []string{"version", "--help"}, process.ExitUsage},
		{"version with an argument", []string{"version", "extra"}, process.ExitUsage},
		{"version with an unknown flag", []string{"version", "--bogus"}, process.ExitUsage},
		{"group alone", []string{"schema"}, process.ExitUsage},
		{"group --help", []string{"schema", "--help"}, process.ExitUsage},
		{"unknown verb", []string{"schema", "bogus"}, process.ExitUsage},
		{"status --help", []string{"schema", "status", "--help"}, process.ExitUsage},
		{"up --help", []string{"schema", "up", "--help"}, process.ExitUsage},
		{"down --help", []string{"schema", "down", "--help"}, process.ExitUsage},
		{"reset --help", []string{"schema", "reset", "--help"}, process.ExitUsage},
		{"status with an argument", []string{"schema", "status", "extra"}, process.ExitUsage},
		{"up with an argument", []string{"schema", "up", "extra"}, process.ExitUsage},
		{"down with an argument", []string{"schema", "down", "extra"}, process.ExitUsage},
		{"reset with an argument", []string{"schema", "reset", "--yes", "extra"}, process.ExitUsage},
		{"status with an unknown flag", []string{"schema", "status", "--bogus"}, process.ExitUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			var out, errOut bytes.Buffer
			a, built := haltedApp(&out, &errOut)

			code := a.Run(context.Background(), tt.args)

			if code != tt.wantCode {
				t.Errorf("code = %d, want %d; stderr = %q", code, tt.wantCode, errOut.String())
			}
			if got := built.Log(); len(got) != 0 {
				t.Errorf("nodes built = %q, want none", got)
			}
		})
	}
}

func TestRun_ConfigurationReadOnlyOnRequest(t *testing.T) {
	// Every configuration holds a value its Finalize rejects, so a run
	// that exits other than 1 read none of them.
	setUnreadable := func(t *testing.T) {
		clearEnv(t)
		t.Setenv(godatabase.NewEnv("BLOBFS").Port, "not-a-port")
		t.Setenv(storage.NewEnv("BLOBFS").MaxObjectSize, "not-a-size")
		t.Setenv(config.EnvName("BLOBFS", "shutdown_timeout"), "not-a-duration")
	}
	tests := []struct {
		name     string
		args     []string
		wantCode int
	}{
		{"version", []string{"version"}, process.ExitOK},
		{"leaf --help", []string{"schema", "status", "--help"}, process.ExitUsage},
		{"usage error", []string{"schema", "status", "extra"}, process.ExitUsage},
		{"missing required flag", []string{"schema", "reset"}, process.ExitUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setUnreadable(t)

			code, _, stderr := run(t, tt.args...)

			if code != tt.wantCode {
				t.Errorf("code = %d, want %d; stderr = %q", code, tt.wantCode, stderr)
			}
		})
	}

	t.Run("request", func(t *testing.T) {
		setUnreadable(t)

		code, _, stderr := run(t, "schema", "status")

		if code != process.ExitFailure {
			t.Errorf("code = %d, want %d: the request should read the bad port", code, process.ExitFailure)
		}
		if want := "blobfs schema status: database config: " + godatabase.NewEnv("BLOBFS").Port; !strings.HasPrefix(stderr, want) {
			t.Errorf("stderr = %q, want it to start with %q", stderr, want)
		}
	})

	t.Run("lifecycle config with the Build", func(t *testing.T) {
		// The database configuration is valid, and constructing the pool
		// does no I/O, so the Build reaches the lifecycle configuration and
		// fails there, before anything starts.
		setUnreadable(t)
		t.Setenv(godatabase.NewEnv("BLOBFS").Port, "")
		t.Setenv(godatabase.NewEnv("BLOBFS").Name, "app")

		code, _, stderr := run(t, "schema", "status")

		if code != process.ExitFailure {
			t.Errorf("code = %d, want %d", code, process.ExitFailure)
		}
		if want := "blobfs schema status: lifecycle config: BLOBFS_SHUTDOWN_TIMEOUT"; !strings.HasPrefix(stderr, want) {
			t.Errorf("stderr = %q, want it to start with %q", stderr, want)
		}
	})
}

func TestNew_IsCold(t *testing.T) {
	var out, errOut bytes.Buffer

	app.New(cli.Streams{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut})

	if out.Len() != 0 || errOut.Len() != 0 {
		t.Errorf("New() wrote %q, %q, want nothing", out.String(), errOut.String())
	}
}

func TestRun_VersionPrintsVersion(t *testing.T) {
	code, stdout, stderr := run(t, "version")

	if code != process.ExitOK {
		t.Errorf("code = %d, want %d", code, process.ExitOK)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 1 || strings.TrimSpace(lines[0]) == "" || !strings.HasSuffix(stdout, "\n") {
		t.Errorf("stdout = %q, want one non-empty line", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestRun_NoArgumentsPrintsRootHelp(t *testing.T) {
	code, stdout, stderr := run(t)

	if code != process.ExitUsage {
		t.Errorf("code = %d, want %d", code, process.ExitUsage)
	}
	for _, want := range []string{
		"Usage:\n  blobfs <command> [flags]\n",
		"\nCommands:\n  version    Print the blobfs version\n  schema     Report, apply, revert, and reset the two migration sets\n",
		"\nFlags:\n  --help   Show help for blobfs\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout =\n%s\nwant it to contain %q", stdout, want)
		}
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestRun_HelpFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"root --help", []string{"--help"}, "Usage:\n  blobfs <command> [flags]\n"},
		{"root -h", []string{"-h"}, "Usage:\n  blobfs <command> [flags]\n"},
		{"version --help", []string{"version", "--help"}, "Print the blobfs version\n\nUsage:\n  blobfs version [flags]\n"},
		{"version -h", []string{"version", "-h"}, "Print the blobfs version\n\nUsage:\n  blobfs version [flags]\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := run(t, tt.args...)

			if code != process.ExitUsage {
				t.Errorf("code = %d, want %d", code, process.ExitUsage)
			}
			if !strings.Contains(stdout, tt.want) {
				t.Errorf("stdout =\n%s\nwant it to contain %q", stdout, tt.want)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want empty", stderr)
			}
		})
	}
}

func TestRun_UnknownCommand(t *testing.T) {
	code, stdout, stderr := run(t, "bogus")

	if code != process.ExitUsage {
		t.Errorf("code = %d, want %d", code, process.ExitUsage)
	}
	if !strings.HasPrefix(stderr, "blobfs: unknown command \"bogus\"\n") {
		t.Errorf("stderr =\n%s\nwant it to start with the unknown command", stderr)
	}
	if !strings.Contains(stderr, "Usage:\n  blobfs <command> [flags]\n") {
		t.Errorf("stderr =\n%s\nwant it to contain the root's usage", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
}

func TestRun_UnknownFlag(t *testing.T) {
	code, stdout, stderr := run(t, "version", "--bogus")

	if code != process.ExitUsage {
		t.Errorf("code = %d, want %d", code, process.ExitUsage)
	}
	want := "blobfs version: flag provided but not defined: -bogus\n" +
		"Usage: blobfs version [flags]\n" +
		"Run 'blobfs version --help' for details.\n"
	if stderr != want {
		t.Errorf("stderr =\n%s\nwant\n%s", stderr, want)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
}

func TestRun_VersionRejectsArguments(t *testing.T) {
	code, stdout, stderr := run(t, "version", "extra")

	if code != process.ExitUsage {
		t.Errorf("code = %d, want %d", code, process.ExitUsage)
	}
	want := "blobfs version: accepts no arguments, got 1\n" +
		"Usage: blobfs version [flags]\n" +
		"Run 'blobfs version --help' for details.\n"
	if stderr != want {
		t.Errorf("stderr =\n%s\nwant\n%s", stderr, want)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
}
