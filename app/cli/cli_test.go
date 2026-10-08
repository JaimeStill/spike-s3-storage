package cli_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/standards-lab/go-core/process"
)

// result is what one dispatch produced.
type result struct {
	code   int
	stdout string
	stderr string
}

// dispatch runs args over root with an empty stdin and fresh buffers.
func dispatch(t *testing.T, root *cli.Command, args ...string) result {
	t.Helper()
	return dispatchIn(t, root, strings.NewReader(""), args...)
}

// dispatchIn runs args over root with stdin and fresh buffers.
func dispatchIn(t *testing.T, root *cli.Command, stdin io.Reader, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), root, args, cli.Streams{Stdin: stdin, Stdout: &stdout, Stderr: &stderr})
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// fixture is a test tree: prog { echo, fail, misuse, group { leaf } }.
// prog defines the root flag --dsn and group the local flag --deep. echo
// and leaf record what they were run with.
type fixture struct {
	root  *cli.Command
	args  []string
	inv   *cli.Invocation
	dsn   *string
	deep  *bool
	name  *string
	count *int
	upper *bool
	tags  []string
	ran   bool
}

func newFixture(runErr error) *fixture {
	f := &fixture{}
	echo := &cli.Command{
		Name:     "echo",
		Summary:  "Echo the arguments",
		Synopsis: "<word>...",
		Run: func(_ context.Context, inv *cli.Invocation) error {
			f.ran = true
			f.args = inv.Args
			f.inv = inv
			line := strings.Join(inv.Args, " ")
			if *f.upper {
				line = strings.ToUpper(line)
			}
			_, err := fmt.Fprintln(inv.Stdout, line)
			return err
		},
	}
	f.name = echo.Flags().String("name", "world", "name to greet")
	f.count = echo.Flags().Int("count", 0, "times to echo")
	f.upper = echo.Flags().Bool("upper", false, "echo in upper case")
	cli.StringsVar(echo.Flags(), &f.tags, "tag", nil, "tag to attach")

	fail := &cli.Command{
		Name:    "fail",
		Summary: "Return an error",
		Run:     func(context.Context, *cli.Invocation) error { return runErr },
	}
	misuse := &cli.Command{
		Name:    "misuse",
		Summary: "Return a usage error",
		Run: func(context.Context, *cli.Invocation) error {
			return fmt.Errorf("checking: %w", cli.Usagef("need %d arguments", 2))
		},
	}
	group := (&cli.Command{Name: "group", Summary: "A nested parent"}).Add(
		&cli.Command{
			Name:    "leaf",
			Summary: "A nested leaf",
			Run: func(_ context.Context, inv *cli.Invocation) error {
				f.ran = true
				f.args = inv.Args
				f.inv = inv
				return nil
			},
		},
	)
	f.deep = group.Flags().Bool("deep", false, "a flag local to group")
	f.root = (&cli.Command{Name: "prog", Summary: "prog is a test program."}).
		Add(echo, fail, misuse, group)
	f.dsn = f.root.Flags().String("dsn", "", "database to connect to")
	return f
}

func TestRun_LeafReceivesFlagsAndArgs(t *testing.T) {
	f := newFixture(nil)

	r := dispatch(t, f.root, "echo", "--name", "gopher", "-count=3", "a", "b")

	if r.code != process.ExitOK {
		t.Errorf("code = %d, want %d", r.code, process.ExitOK)
	}
	if !slices.Equal(f.args, []string{"a", "b"}) {
		t.Errorf("args = %q, want [a b]", f.args)
	}
	if *f.name != "gopher" || *f.count != 3 {
		t.Errorf("flags = %q, %d, want gopher, 3", *f.name, *f.count)
	}
	if r.stdout != "a b\n" || r.stderr != "" {
		t.Errorf("stdout, stderr = %q, %q, want %q, empty", r.stdout, r.stderr, "a b\n")
	}
}

func TestRun_LeafReadsStdinAndTheDispatcherDoesNot(t *testing.T) {
	f := newFixture(nil)
	stdin := strings.NewReader("piped")

	r := dispatchIn(t, f.root, stdin, "echo", "a")

	if r.code != process.ExitOK {
		t.Fatalf("code = %d, want %d", r.code, process.ExitOK)
	}
	if f.inv.Stdin != io.Reader(stdin) {
		t.Errorf("Invocation.Stdin = %v, want the reader passed to Run", f.inv.Stdin)
	}
	if stdin.Len() != len("piped") {
		t.Errorf("stdin has %d bytes left, want all %d: the dispatcher read it", stdin.Len(), len("piped"))
	}
	got, err := io.ReadAll(f.inv.Stdin)
	if err != nil || string(got) != "piped" {
		t.Errorf("reading Invocation.Stdin = %q, %v, want %q", got, err, "piped")
	}
}

func TestRun_NestedLeafRuns(t *testing.T) {
	f := newFixture(nil)

	r := dispatch(t, f.root, "group", "leaf")

	if r.code != process.ExitOK || !f.ran {
		t.Errorf("code = %d, ran = %v, want %d, true", r.code, f.ran, process.ExitOK)
	}
}

func TestRun_ParentWithoutSubcommandPrintsHelp(t *testing.T) {
	f := newFixture(nil)

	r := dispatch(t, f.root)

	if r.code != process.ExitUsage {
		t.Errorf("code = %d, want %d", r.code, process.ExitUsage)
	}
	want := `prog is a test program.

Usage:
  prog <command> [flags]

Commands:
  echo     Echo the arguments
  fail     Return an error
  misuse   Return a usage error
  group    A nested parent

Flags:
  --dsn string   database to connect to
  --help         Show help for prog

Run 'prog <command> --help' for help on a command.
`
	if r.stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", r.stdout, want)
	}
	if r.stderr != "" {
		t.Errorf("stderr = %q, want empty", r.stderr)
	}
}

func TestRun_NestedParentWithoutSubcommandPrintsItsHelp(t *testing.T) {
	f := newFixture(nil)

	r := dispatch(t, f.root, "group")

	if r.code != process.ExitUsage {
		t.Errorf("code = %d, want %d", r.code, process.ExitUsage)
	}
	for _, want := range []string{"Usage:\n  prog group <command> [flags]\n", "  leaf   A nested leaf\n"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout =\n%s\nwant it to contain %q", r.stdout, want)
		}
	}
}

func TestRun_HelpFlagPrintsCommandHelp(t *testing.T) {
	leafHelp := `Echo the arguments

Usage:
  prog echo [flags] <word>...

Flags:
  --count int     times to echo
  --name string   name to greet (default "world")
  --tag string    tag to attach (repeatable)
  --upper         echo in upper case
  --help          Show help for prog echo

Global flags:
  --dsn string   database to connect to
`
	tests := []struct {
		name string
		args []string
		want string // the help's first lines
	}{
		{"leaf --help", []string{"echo", "--help"}, leafHelp},
		{"leaf -h", []string{"echo", "-h"}, leafHelp},
		{"leaf -h after flags", []string{"echo", "--count", "2", "-h"}, leafHelp},
		{"root --help", []string{"--help"}, "prog is a test program.\n\nUsage:\n  prog <command> [flags]\n"},
		{"nested parent -h", []string{"group", "-h"}, "A nested parent\n\nUsage:\n  prog group <command> [flags]\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(nil)

			r := dispatch(t, f.root, tt.args...)

			if r.code != process.ExitUsage {
				t.Errorf("code = %d, want %d", r.code, process.ExitUsage)
			}
			if !strings.HasPrefix(r.stdout, tt.want) {
				t.Errorf("stdout =\n%s\nwant it to start with\n%s", r.stdout, tt.want)
			}
			if r.stderr != "" || f.ran {
				t.Errorf("stderr = %q, ran = %v, want empty, false", r.stderr, f.ran)
			}
		})
	}
}

func TestRun_UnknownSubcommandIsUsageError(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"under root", []string{"bogus"}, []string{
			"prog: unknown command \"bogus\"\n\n",
			"Usage:\n  prog <command> [flags]\n",
			"  echo     Echo the arguments\n",
		}},
		{"under nested parent", []string{"group", "nope"}, []string{
			"prog group: unknown command \"nope\"\n\n",
			"Usage:\n  prog group <command> [flags]\n",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(nil)

			r := dispatch(t, f.root, tt.args...)

			if r.code != process.ExitUsage {
				t.Errorf("code = %d, want %d", r.code, process.ExitUsage)
			}
			if !strings.HasPrefix(r.stderr, tt.want[0]) {
				t.Errorf("stderr =\n%s\nwant it to start with %q", r.stderr, tt.want[0])
			}
			for _, want := range tt.want[1:] {
				if !strings.Contains(r.stderr, want) {
					t.Errorf("stderr =\n%s\nwant it to contain %q", r.stderr, want)
				}
			}
			if r.stdout != "" {
				t.Errorf("stdout = %q, want empty", r.stdout)
			}
		})
	}
}

func TestRun_FlagErrorIsUsageError(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"unknown flag", []string{"echo", "--bogus"},
			"prog echo: flag provided but not defined: -bogus\n" +
				"Usage: prog echo [flags] <word>...\n" +
				"Run 'prog echo --help' for details.\n"},
		{"malformed value", []string{"echo", "--count", "many"},
			"prog echo: invalid value \"many\" for flag -count: parse error\n" +
				"Usage: prog echo [flags] <word>...\n" +
				"Run 'prog echo --help' for details.\n"},
		{"unknown flag on a parent", []string{"--bogus", "echo"},
			"prog: flag provided but not defined: -bogus\n" +
				"Usage: prog <command> [flags]\n" +
				"Run 'prog --help' for details.\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(nil)

			r := dispatch(t, f.root, tt.args...)

			if r.code != process.ExitUsage {
				t.Errorf("code = %d, want %d", r.code, process.ExitUsage)
			}
			if r.stderr != tt.want {
				t.Errorf("stderr =\n%s\nwant\n%s", r.stderr, tt.want)
			}
			if r.stdout != "" || f.ran {
				t.Errorf("stdout = %q, ran = %v, want empty, false", r.stdout, f.ran)
			}
		})
	}
}

func TestRun_CommandErrorReportedOnce(t *testing.T) {
	f := newFixture(errors.New("boom"))

	r := dispatch(t, f.root, "fail")

	if r.code != process.ExitFailure {
		t.Errorf("code = %d, want %d", r.code, process.ExitFailure)
	}
	if r.stderr != "prog fail: boom\n" {
		t.Errorf("stderr = %q, want %q", r.stderr, "prog fail: boom\n")
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want empty", r.stdout)
	}
}

func TestRun_ReturnedUsageErrorIsUsage(t *testing.T) {
	f := newFixture(nil)

	r := dispatch(t, f.root, "misuse")

	if r.code != process.ExitUsage {
		t.Errorf("code = %d, want %d", r.code, process.ExitUsage)
	}
	want := "prog misuse: checking: need 2 arguments\n" +
		"Usage: prog misuse [flags]\n" +
		"Run 'prog misuse --help' for details.\n"
	if r.stderr != want {
		t.Errorf("stderr =\n%s\nwant\n%s", r.stderr, want)
	}
}

func TestUsagef_WrapsWithW(t *testing.T) {
	cause := errors.New("cause")

	err := cli.Usagef("bad input: %w", cause)

	if _, ok := errors.AsType[*cli.UsageError](err); !ok {
		t.Errorf("Usagef() = %T, want *cli.UsageError", err)
	}
	if !errors.Is(err, cause) {
		t.Error("Usagef() does not wrap its %w cause")
	}
	if err.Error() != "bad input: cause" {
		t.Errorf("Error() = %q, want %q", err.Error(), "bad input: cause")
	}
}

func TestAdd_PanicsOnWiringMistake(t *testing.T) {
	leaf := func(name string) *cli.Command {
		return &cli.Command{Name: name, Run: func(context.Context, *cli.Invocation) error { return nil }}
	}
	tests := []struct {
		name  string
		build func()
		want  string
	}{
		{"duplicate subcommand", func() {
			(&cli.Command{Name: "prog"}).Add(leaf("a"), leaf("a"))
		}, `cli: prog: duplicate subcommand "a"`},
		{"empty name", func() {
			(&cli.Command{Name: "prog"}).Add(leaf(""))
		}, "cli: prog: subcommand with no name"},
		{"name with space", func() {
			(&cli.Command{Name: "prog"}).Add(leaf("a b"))
		}, `cli: prog: invalid subcommand name "a b"`},
		{"name like a flag", func() {
			(&cli.Command{Name: "prog"}).Add(leaf("-a"))
		}, `cli: prog: invalid subcommand name "-a"`},
		{"second parent", func() {
			a := leaf("a")
			(&cli.Command{Name: "one"}).Add(a)
			(&cli.Command{Name: "two"}).Add(a)
		}, `cli: two: subcommand "a" already added to one`},
		{"duplicate flag", func() {
			c := leaf("a")
			c.Flags().String("name", "", "")
			c.Flags().Bool("name", false, "")
		}, "flag redefined: name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("no panic, want one")
				}
				if msg := fmt.Sprint(r); !strings.Contains(msg, tt.want) {
					t.Errorf("panic = %q, want it to contain %q", msg, tt.want)
				}
			}()
			tt.build()
		})
	}
}
