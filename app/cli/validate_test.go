package cli_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/JaimeStill/spike-s3-storage/app/graph"
	"github.com/JaimeStill/spike-s3-storage/app/lifecycle"
	"github.com/standards-lab/go-core/config"
	"github.com/standards-lab/go-core/process"
)

// validateTree is a test tree for Validate: prog { cmd }. cmd takes exactly
// one argument, requires --key, holds --a and --b exclusive, declares db,
// and has a Validate; the root has a PreRun. Validate, PreRun, db's
// constructor, and cmd's Run record to calls; Validate returns err and
// keeps the Invocation it saw.
type validateTree struct {
	root  *cli.Command
	g     *graph.Graph
	cfg   *graph.Node[lifecycle.Config]
	calls []string
	err   error
	inv   *cli.Invocation
}

func newValidateTree() *validateTree {
	v := &validateTree{g: graph.New()}
	v.cfg = v.g.Define("lifecycle", func(*graph.Scope) (lifecycle.Config, error) {
		return lifecycle.Config{ShutdownTimeout: config.Duration(5 * time.Second)}, nil
	})
	db := v.g.Define("db", func(*graph.Scope) (int, error) {
		v.calls = append(v.calls, "build db")
		return 0, nil
	})
	cmd := (&cli.Command{
		Name:     "cmd",
		Synopsis: "<arg>",
		Args:     cli.ExactArgs(1),
		Validate: func(inv *cli.Invocation) error {
			v.calls = append(v.calls, "validate")
			v.inv = inv
			return v.err
		},
		Run: func(context.Context, *cli.Invocation) error {
			v.calls = append(v.calls, "run")
			return nil
		},
	}).Use(db)
	cmd.Flags().String("key", "", "")
	cmd.Flags().Bool("a", false, "")
	cmd.Flags().Bool("b", false, "")
	cmd.Require("key")
	cmd.Exclusive("a", "b")
	v.root = &cli.Command{
		Name: "prog",
		PreRun: func(context.Context, *cli.Invocation) error {
			v.calls = append(v.calls, "prerun")
			return nil
		},
	}
	v.root.Add(cmd)
	return v
}

func (v *validateTree) run(args ...string) result {
	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), v.root, args, cli.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr}, cli.WithGraph(v.g, v.cfg))
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func TestValidate_RunsAfterTheFlagChecksAndBeforePreRunAndTheBuild(t *testing.T) {
	v := newValidateTree()

	r := v.run("cmd", "x", "--key", "k", "--a")

	if r.code != process.ExitOK {
		t.Fatalf("code = %d, want %d; stderr %q", r.code, process.ExitOK, r.stderr)
	}
	if want := []string{"validate", "prerun", "build db", "run"}; !slices.Equal(v.calls, want) {
		t.Errorf("calls = %q, want %q", v.calls, want)
	}
	if !slices.Equal(v.inv.Args, []string{"x"}) || !v.inv.Changed("a") || v.inv.Changed("b") {
		t.Errorf("Validate saw Args %q, Changed(a) %v, Changed(b) %v; want [x], true, false",
			v.inv.Args, v.inv.Changed("a"), v.inv.Changed("b"))
	}
}

func TestValidate_NotCalledWhenAnEarlierCheckFails(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		stderr string // the start of the usage error, naming the earlier check
	}{
		{"argument count", []string{"cmd", "--key", "k"}, "prog cmd: accepts 1 argument, got 0\n"},
		{"required flag", []string{"cmd", "x"}, "prog cmd: required flag --key not set\n"},
		{"exclusive group", []string{"cmd", "x", "--key", "k", "--a", "--b"}, "prog cmd: flags --a, --b cannot be used together\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := newValidateTree()

			r := v.run(tt.args...)

			if r.code != process.ExitUsage || !strings.HasPrefix(r.stderr, tt.stderr) {
				t.Errorf("code = %d, stderr = %q, want %d and %q", r.code, r.stderr, process.ExitUsage, tt.stderr)
			}
			if len(v.calls) != 0 {
				t.Errorf("calls = %q, want none", v.calls)
			}
		})
	}
}

func TestValidate_ErrorIsAlwaysAUsageErrorAndBuildsNothing(t *testing.T) {
	tests := []struct {
		name string
		err  error
		msg  string
	}{
		{"plain error", errors.New("--key \"k\" is malformed"), "--key \"k\" is malformed"},
		{"usage error", cli.Usagef("--key %q is malformed", "k"), "--key \"k\" is malformed"},
		{"wrapped plain error", fmt.Errorf("checking: %w", errors.New("bad")), "checking: bad"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := newValidateTree()
			v.err = tt.err

			r := v.run("cmd", "x", "--key", "k")

			if r.code != process.ExitUsage {
				t.Errorf("code = %d, want %d", r.code, process.ExitUsage)
			}
			want := "prog cmd: " + tt.msg + "\n" +
				"Usage: prog cmd [flags] <arg>\n" +
				"Run 'prog cmd --help' for details.\n"
			if r.stderr != want || r.stdout != "" {
				t.Errorf("stdout = %q, stderr =\n%s\nwant empty and\n%s", r.stdout, r.stderr, want)
			}
			if want := []string{"validate"}; !slices.Equal(v.calls, want) {
				t.Errorf("calls = %q, want %q: no PreRun, Build, or Run", v.calls, want)
			}
		})
	}
}

func TestValidate_PanicsOnAParent(t *testing.T) {
	ok := func(context.Context, *cli.Invocation) error { return nil }
	group := (&cli.Command{
		Name:     "group",
		Validate: func(*cli.Invocation) error { return nil },
	}).Add(&cli.Command{Name: "leaf", Run: ok})
	root := (&cli.Command{Name: "prog"}).Add(&cli.Command{Name: "a", Run: ok}, group)

	// The mistake is on a path other than the one dispatched.
	got := panicOf(func() { dispatch(t, root, "a") })

	if want := "cli: prog group: Validate set on a parent command"; got != want {
		t.Errorf("panic = %v, want %q", got, want)
	}
}
