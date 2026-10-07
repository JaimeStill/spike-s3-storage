package cli_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/standards-lab/go-core/process"
)

func TestRun_LeafFlagsMayFollowPositionals(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		want  []string // positional arguments
		upper bool
		value string // --name
	}{
		{"flag first", []string{"echo", "--upper", "a", "b"}, []string{"a", "b"}, true, "world"},
		{"flag between", []string{"echo", "a", "--upper", "b"}, []string{"a", "b"}, true, "world"},
		{"flag last", []string{"echo", "a", "b", "--upper"}, []string{"a", "b"}, true, "world"},
		{"bool given false", []string{"echo", "a", "--upper=false", "b"}, []string{"a", "b"}, false, "world"},
		{"value takes next word", []string{"echo", "a", "--name", "v", "b"}, []string{"a", "b"}, false, "v"},
		{"value with equals", []string{"echo", "a", "--name=v", "b"}, []string{"a", "b"}, false, "v"},
		{"single dash", []string{"echo", "a", "-name", "v", "b"}, []string{"a", "b"}, false, "v"},
		{"value that looks like a flag", []string{"echo", "--name", "--upper", "a"}, []string{"a"}, false, "--upper"},
		{"value that is the terminator", []string{"echo", "--name", "--", "a"}, []string{"a"}, false, "--"},
		{"terminator", []string{"echo", "--", "--upper", "-x", "a"}, []string{"--upper", "-x", "a"}, false, "world"},
		{"flags before terminator", []string{"echo", "a", "--upper", "--", "--name", "v"}, []string{"a", "--name", "v"}, true, "world"},
		{"lone dash is positional", []string{"echo", "-", "a"}, []string{"-", "a"}, false, "world"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(nil)

			r := dispatch(t, f.root, tt.args...)

			if r.code != process.ExitOK || r.stderr != "" {
				t.Fatalf("code = %d, stderr = %q, want %d, empty", r.code, r.stderr, process.ExitOK)
			}
			if !slices.Equal(f.args, tt.want) {
				t.Errorf("args = %q, want %q", f.args, tt.want)
			}
			if *f.upper != tt.upper || *f.name != tt.value {
				t.Errorf("--upper, --name = %v, %q, want %v, %q", *f.upper, *f.name, tt.upper, tt.value)
			}
		})
	}
}

func TestRun_InterleavedFlagsReachTheCommand(t *testing.T) {
	f := newFixture(nil)

	r := dispatch(t, f.root, "echo", "a", "--upper", "b")

	if r.code != process.ExitOK || r.stdout != "A B\n" {
		t.Errorf("code, stdout = %d, %q, want %d, %q", r.code, r.stdout, process.ExitOK, "A B\n")
	}
}

func TestRun_FlagAfterPositionalIsChecked(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string // stderr's first line
	}{
		{"unknown flag", []string{"echo", "a", "--bogus"}, "prog echo: flag provided but not defined: -bogus\n"},
		{"value flag with no value", []string{"echo", "a", "--name"}, "prog echo: flag needs an argument: -name\n"},
		{"malformed value", []string{"echo", "a", "--count", "x", "b"}, "prog echo: invalid value \"x\" for flag -count: parse error\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(nil)

			r := dispatch(t, f.root, tt.args...)

			if r.code != process.ExitUsage {
				t.Errorf("code = %d, want %d", r.code, process.ExitUsage)
			}
			if !strings.HasPrefix(r.stderr, tt.want) {
				t.Errorf("stderr =\n%s\nwant it to start with %q", r.stderr, tt.want)
			}
			if f.ran || r.stdout != "" {
				t.Errorf("ran = %v, stdout = %q, want false, empty", f.ran, r.stdout)
			}
		})
	}
}

func TestRun_HelpAfterPositionalPrintsHelp(t *testing.T) {
	f := newFixture(nil)

	r := dispatch(t, f.root, "echo", "a", "--help")

	if r.code != process.ExitUsage || !strings.HasPrefix(r.stdout, "Echo the arguments\n") {
		t.Errorf("code = %d, stdout =\n%s\nwant %d and echo's help", r.code, r.stdout, process.ExitUsage)
	}
	if f.ran {
		t.Error("echo ran, want it not to")
	}
}

func TestRun_RootFlagAcceptedAtAnyDepth(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string // the leaf's positional arguments
	}{
		{"before the parent", []string{"--dsn", "x", "group", "leaf"}, nil},
		{"after the parent", []string{"group", "--dsn", "x", "leaf"}, nil},
		{"after the leaf", []string{"group", "leaf", "--dsn", "x"}, nil},
		{"after a positional", []string{"group", "leaf", "a", "--dsn", "x"}, []string{"a"}},
		{"with equals", []string{"group", "leaf", "--dsn=x", "a"}, []string{"a"}},
		{"on a top-level leaf", []string{"echo", "a", "--dsn", "x"}, []string{"a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(nil)

			r := dispatch(t, f.root, tt.args...)

			if r.code != process.ExitOK || !f.ran {
				t.Fatalf("code = %d, ran = %v, stderr = %q, want %d, true", r.code, f.ran, r.stderr, process.ExitOK)
			}
			if *f.dsn != "x" {
				t.Errorf("--dsn = %q, want %q", *f.dsn, "x")
			}
			if !slices.Equal(f.args, tt.want) {
				t.Errorf("args = %q, want %q", f.args, tt.want)
			}
		})
	}
}

func TestRun_RootFlagLastOccurrenceWins(t *testing.T) {
	f := newFixture(nil)

	r := dispatch(t, f.root, "--dsn", "a", "group", "--dsn", "b", "leaf", "--dsn", "c")

	if r.code != process.ExitOK || *f.dsn != "c" {
		t.Errorf("code = %d, --dsn = %q, want %d, %q", r.code, *f.dsn, process.ExitOK, "c")
	}
}

func TestRun_TreeDispatchesMoreThanOnce(t *testing.T) {
	f := newFixture(nil)

	first := dispatch(t, f.root, "group", "leaf")
	second := dispatch(t, f.root, "group", "leaf", "--dsn", "x")

	if first.code != process.ExitOK || second.code != process.ExitOK {
		t.Errorf("codes = %d, %d, want %d, %d", first.code, second.code, process.ExitOK, process.ExitOK)
	}
	if *f.dsn != "x" {
		t.Errorf("--dsn = %q, want %q", *f.dsn, "x")
	}
}

func TestRun_NestedParentFlagsStayLocal(t *testing.T) {
	f := newFixture(nil)

	r := dispatch(t, f.root, "group", "--deep", "leaf")

	if r.code != process.ExitOK || !*f.deep {
		t.Errorf("code = %d, --deep = %v, want %d, true", r.code, *f.deep, process.ExitOK)
	}

	f = newFixture(nil)

	r = dispatch(t, f.root, "group", "leaf", "--deep")

	if r.code != process.ExitUsage {
		t.Errorf("code = %d, want %d", r.code, process.ExitUsage)
	}
	if want := "prog group leaf: flag provided but not defined: -deep\n"; !strings.HasPrefix(r.stderr, want) {
		t.Errorf("stderr =\n%s\nwant it to start with %q", r.stderr, want)
	}
}

func TestRun_HelpListsRootFlagsAsGlobal(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"nested parent", []string{"group", "--help"}, `A nested parent

Usage:
  prog group <command> [flags]

Commands:
  leaf   A nested leaf

Flags:
  --deep   a flag local to group
  --help   Show help for prog group

Global flags:
  --dsn string   database to connect to

Run 'prog group <command> --help' for help on a command.
`},
		{"nested leaf", []string{"group", "leaf", "--help"}, `A nested leaf

Usage:
  prog group leaf [flags]

Flags:
  --help   Show help for prog group leaf

Global flags:
  --dsn string   database to connect to
`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(nil)

			r := dispatch(t, f.root, tt.args...)

			if r.code != process.ExitUsage {
				t.Errorf("code = %d, want %d", r.code, process.ExitUsage)
			}
			if r.stdout != tt.want {
				t.Errorf("stdout =\n%s\nwant\n%s", r.stdout, tt.want)
			}
		})
	}
}

func TestRun_PanicsOnDispatchWiringMistake(t *testing.T) {
	leaf := func(name string) *cli.Command {
		return &cli.Command{Name: name, Run: func(context.Context, *cli.Invocation) error { return nil }}
	}
	tests := []struct {
		name  string
		build func() *cli.Command
		want  string
	}{
		{"leaf redefines a root flag", func() *cli.Command {
			b := leaf("b")
			root := (&cli.Command{Name: "prog"}).Add(leaf("a"), b)
			root.Flags().String("dsn", "", "")
			b.Flags().String("dsn", "", "")
			return root
		}, "cli: prog b: flag --dsn redefines a root flag"},
		{"root flag defined after the leaf's", func() *cli.Command {
			b := leaf("b")
			b.Flags().Bool("dsn", false, "")
			root := (&cli.Command{Name: "prog"}).Add(
				(&cli.Command{Name: "group"}).Add(b),
			)
			root.Flags().String("dsn", "", "")
			return root
		}, "cli: prog group b: flag --dsn redefines a root flag"},
		{"Args on a parent", func() *cli.Command {
			group := (&cli.Command{Name: "group", Args: cli.NoArgs}).Add(leaf("b"))
			return (&cli.Command{Name: "prog"}).Add(leaf("a"), group)
		}, "cli: prog group: Args set on a parent command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := tt.build()
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("no panic, want one")
				}
				if msg := fmt.Sprint(r); msg != tt.want {
					t.Errorf("panic = %q, want %q", msg, tt.want)
				}
			}()
			// The mistake is on a path other than the one dispatched.
			dispatch(t, root, "a")
		})
	}
}

func TestRun_ArgsValidator(t *testing.T) {
	tests := []struct {
		name     string
		validate func([]string) error
		args     []string
		code     int
		stderr   string
	}{
		{"nil accepts any", nil, []string{"x", "y", "z"}, process.ExitOK, ""},
		{"NoArgs accepts none", cli.NoArgs, nil, process.ExitOK, ""},
		{"NoArgs rejects one", cli.NoArgs, []string{"x"}, process.ExitUsage,
			"prog cmd: accepts no arguments, got 1\n"},
		{"ExactArgs accepts n", cli.ExactArgs(2), []string{"x", "y"}, process.ExitOK, ""},
		{"ExactArgs counts after interleaving", cli.ExactArgs(2), []string{"x", "--flag", "y"}, process.ExitOK, ""},
		{"ExactArgs rejects fewer", cli.ExactArgs(2), []string{"x"}, process.ExitUsage,
			"prog cmd: accepts 2 arguments, got 1\n"},
		{"ExactArgs rejects more", cli.ExactArgs(2), []string{"x", "y", "z"}, process.ExitUsage,
			"prog cmd: accepts 2 arguments, got 3\n"},
		{"ExactArgs singular", cli.ExactArgs(1), nil, process.ExitUsage,
			"prog cmd: accepts 1 argument, got 0\n"},
		{"plain error is a usage error", func([]string) error { return errors.New("bad") }, nil, process.ExitUsage,
			"prog cmd: bad\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ran := false
			cmd := &cli.Command{
				Name:     "cmd",
				Synopsis: "<arg>...",
				Args:     tt.validate,
				Run:      func(context.Context, *cli.Invocation) error { ran = true; return nil },
			}
			cmd.Flags().Bool("flag", false, "")
			root := (&cli.Command{Name: "prog"}).Add(cmd)

			r := dispatch(t, root, append([]string{"cmd"}, tt.args...)...)

			if r.code != tt.code {
				t.Errorf("code = %d, want %d", r.code, tt.code)
			}
			if tt.stderr == "" {
				if r.stderr != "" || !ran {
					t.Errorf("stderr = %q, ran = %v, want empty, true", r.stderr, ran)
				}
				return
			}
			want := tt.stderr +
				"Usage: prog cmd [flags] <arg>...\n" +
				"Run 'prog cmd --help' for details.\n"
			if r.stderr != want {
				t.Errorf("stderr =\n%s\nwant\n%s", r.stderr, want)
			}
			if ran {
				t.Error("command ran, want it not to")
			}
		})
	}
}

func TestInvocation_Changed(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		changed []string
		not     []string
	}{
		{"nothing given", []string{"echo"}, nil, []string{"name", "count", "upper", "tag", "dsn"}},
		{"set to its default", []string{"echo", "--name", "world", "--count=0"}, []string{"name", "count"}, []string{"upper", "dsn"}},
		{"bool set false", []string{"echo", "a", "--upper=false"}, []string{"upper"}, []string{"name"}},
		{"repeatable", []string{"echo", "a", "--tag", "x"}, []string{"tag"}, []string{"name"}},
		{"root flag at the root", []string{"--dsn", "", "echo"}, []string{"dsn"}, []string{"name"}},
		{"root flag at a nested parent", []string{"group", "--dsn", "x", "leaf"}, []string{"dsn"}, []string{"deep"}},
		{"root flag after the leaf", []string{"group", "leaf", "--dsn", "x"}, []string{"dsn"}, nil},
		{"nested parent flag is not the leaf's", []string{"group", "--deep", "leaf"}, nil, []string{"deep", "dsn"}},
		{"undefined name", []string{"echo", "--name", "v"}, nil, []string{"bogus"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(nil)

			r := dispatch(t, f.root, tt.args...)

			if r.code != process.ExitOK || f.inv == nil {
				t.Fatalf("code = %d, stderr = %q, want %d and a run", r.code, r.stderr, process.ExitOK)
			}
			for _, name := range tt.changed {
				if !f.inv.Changed(name) {
					t.Errorf("Changed(%q) = false, want true", name)
				}
			}
			for _, name := range tt.not {
				if f.inv.Changed(name) {
					t.Errorf("Changed(%q) = true, want false", name)
				}
			}
		})
	}
}

func TestStringsVar_CollectsEveryOccurrenceInOrder(t *testing.T) {
	f := newFixture(nil)

	r := dispatch(t, f.root, "echo", "--tag", "b", "x", "--tag=a,c", "y", "--tag", "b")

	if r.code != process.ExitOK {
		t.Fatalf("code = %d, stderr = %q, want %d", r.code, r.stderr, process.ExitOK)
	}
	if want := []string{"b", "a,c", "b"}; !slices.Equal(f.tags, want) {
		t.Errorf("tags = %q, want %q", f.tags, want)
	}
	if want := []string{"x", "y"}; !slices.Equal(f.args, want) {
		t.Errorf("args = %q, want %q", f.args, want)
	}
}

func TestStringsVar_Default(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"kept when not given", nil, []string{"d1", "d2"}},
		{"replaced when given", []string{"--tag", "x", "--tag", "y"}, []string{"x", "y"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tags []string
			cmd := &cli.Command{Name: "cmd", Run: func(context.Context, *cli.Invocation) error { return nil }}
			cli.StringsVar(cmd.Flags(), &tags, "tag", []string{"d1", "d2"}, "tag to attach")
			root := (&cli.Command{Name: "prog"}).Add(cmd)

			r := dispatch(t, root, append([]string{"cmd"}, tt.args...)...)

			if r.code != process.ExitOK || !slices.Equal(tags, tt.want) {
				t.Errorf("code = %d, tags = %q, want %d, %q", r.code, tags, process.ExitOK, tt.want)
			}
		})
	}
}

func TestStringsVar_DoesNotAliasTheDefault(t *testing.T) {
	def := []string{"d1"}
	var tags []string
	fs := flag.NewFlagSet("t", flag.ContinueOnError)

	cli.StringsVar(fs, &tags, "tag", def, "")
	tags[0] = "changed"

	if def[0] != "d1" {
		t.Errorf("default = %q, want it unchanged", def)
	}
}

func TestStringsVar_HelpShowsDefault(t *testing.T) {
	var tags []string
	cmd := &cli.Command{Name: "cmd", Run: func(context.Context, *cli.Invocation) error { return nil }}
	cli.StringsVar(cmd.Flags(), &tags, "tag", []string{"d1", "d2"}, "tag to attach")
	root := (&cli.Command{Name: "prog"}).Add(cmd)

	r := dispatch(t, root, "cmd", "--help")

	if want := "  --tag string   tag to attach (repeatable) (default [d1,d2])\n"; !strings.Contains(r.stdout, want) {
		t.Errorf("stdout =\n%s\nwant it to contain %q", r.stdout, want)
	}
}
