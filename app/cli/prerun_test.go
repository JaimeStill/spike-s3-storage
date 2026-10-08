package cli_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/standards-lab/go-core/process"
)

// hookTree is a test tree whose root has a PreRun hook: prog { cmd, group
// { leaf } }. The hook and both leaves append to calls; cmd takes exactly
// one argument, defines --local, and requires nothing.
type hookTree struct {
	root    *cli.Command
	calls   []string
	dsn     *string
	hookErr error
	hookInv *cli.Invocation
	runInv  *cli.Invocation
}

func newHookTree() *hookTree {
	h := &hookTree{}
	run := func(name string) func(context.Context, *cli.Invocation) error {
		return func(_ context.Context, inv *cli.Invocation) error {
			h.calls = append(h.calls, name)
			h.runInv = inv
			return nil
		}
	}
	cmd := &cli.Command{Name: "cmd", Synopsis: "<arg>", Args: cli.ExactArgs(1), Run: run("cmd")}
	cmd.Flags().Bool("local", false, "")
	h.root = &cli.Command{
		Name: "prog",
		PreRun: func(_ context.Context, inv *cli.Invocation) error {
			h.calls = append(h.calls, "prerun")
			h.hookInv = inv
			return h.hookErr
		},
	}
	h.root.Add(cmd, (&cli.Command{Name: "group"}).Add(&cli.Command{Name: "leaf", Run: run("leaf")}))
	h.dsn = h.root.Flags().String("dsn", "", "database to connect to")
	return h
}

func TestPreRun_RunsOnceBeforeTheLeaf(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"top-level leaf", []string{"cmd", "a"}, []string{"prerun", "cmd"}},
		{"nested leaf", []string{"group", "leaf"}, []string{"prerun", "leaf"}},
		{"root flag at every level", []string{"--dsn", "a", "group", "--dsn", "b", "leaf", "--dsn", "c"}, []string{"prerun", "leaf"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHookTree()

			r := dispatch(t, h.root, tt.args...)

			if r.code != process.ExitOK || r.stderr != "" {
				t.Fatalf("code = %d, stderr = %q, want %d, empty", r.code, r.stderr, process.ExitOK)
			}
			if !slices.Equal(h.calls, tt.want) {
				t.Errorf("calls = %q, want %q", h.calls, tt.want)
			}
		})
	}
}

func TestPreRun_ReceivesTheLeafsInvocation(t *testing.T) {
	h := newHookTree()
	var dsn string
	var changed bool
	h.root.PreRun = func(_ context.Context, inv *cli.Invocation) error {
		h.hookInv = inv
		dsn, changed = *h.dsn, inv.Changed("dsn")
		return nil
	}

	r := dispatch(t, h.root, "cmd", "a", "--dsn", "x")

	if r.code != process.ExitOK {
		t.Fatalf("code = %d, stderr = %q, want %d", r.code, r.stderr, process.ExitOK)
	}
	if dsn != "x" || !changed {
		t.Errorf("hook saw --dsn = %q, Changed = %v, want %q, true", dsn, changed, "x")
	}
	if h.hookInv != h.runInv || !slices.Equal(h.hookInv.Args, []string{"a"}) {
		t.Errorf("hook's Invocation = %+v, want the leaf's, with args [a]", h.hookInv)
	}
}

func TestPreRun_ErrorStopsTheLeaf(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		code   int
		stderr string
	}{
		{"plain error", errors.New("no database"), process.ExitFailure, "prog cmd: no database\n"},
		{"usage error", fmt.Errorf("checking: %w", cli.Usagef("--dsn is malformed")), process.ExitUsage,
			"prog cmd: checking: --dsn is malformed\n" +
				"Usage: prog cmd [flags] <arg>\n" +
				"Run 'prog cmd --help' for details.\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHookTree()
			h.hookErr = tt.err

			r := dispatch(t, h.root, "cmd", "a")

			if r.code != tt.code {
				t.Errorf("code = %d, want %d", r.code, tt.code)
			}
			if r.stderr != tt.stderr || r.stdout != "" {
				t.Errorf("stdout = %q, stderr =\n%s\nwant empty and\n%s", r.stdout, r.stderr, tt.stderr)
			}
			if want := []string{"prerun"}; !slices.Equal(h.calls, want) {
				t.Errorf("calls = %q, want %q", h.calls, want)
			}
		})
	}
}

func TestPreRun_SkippedWhenTheDispatchEndsEarly(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"root help", []string{"--help"}},
		{"leaf help", []string{"cmd", "-h"}},
		{"root with no subcommand", nil},
		{"nested parent with no subcommand", []string{"group"}},
		{"unknown subcommand", []string{"bogus"}},
		{"unknown flag", []string{"cmd", "a", "--bogus"}},
		{"malformed root flag", []string{"--dsn"}},
		{"wrong argument count", []string{"cmd"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHookTree()

			r := dispatch(t, h.root, tt.args...)

			if r.code != process.ExitUsage {
				t.Errorf("code = %d, want %d", r.code, process.ExitUsage)
			}
			if len(h.calls) != 0 {
				t.Errorf("calls = %q, want none", h.calls)
			}
		})
	}
}

func TestPreRun_SkippedOnAFlagGroupError(t *testing.T) {
	h := newHookTree()
	cmd := &cli.Command{Name: "need", Run: func(context.Context, *cli.Invocation) error {
		h.calls = append(h.calls, "need")
		return nil
	}}
	cmd.Require("dsn")
	h.root.Add(cmd)

	r := dispatch(t, h.root, "need")

	if r.code != process.ExitUsage || !strings.HasPrefix(r.stderr, "prog need: required flag --dsn not set\n") {
		t.Errorf("code = %d, stderr =\n%s\nwant %d and the missing --dsn", r.code, r.stderr, process.ExitUsage)
	}
	if len(h.calls) != 0 {
		t.Errorf("calls = %q, want none", h.calls)
	}
}

func TestPreRun_PanicsBelowTheRoot(t *testing.T) {
	hook := func(context.Context, *cli.Invocation) error { return nil }
	leaf := &cli.Command{Name: "leaf", PreRun: hook, Run: func(context.Context, *cli.Invocation) error { return nil }}
	root := (&cli.Command{Name: "prog"}).Add(
		&cli.Command{Name: "a", Run: func(context.Context, *cli.Invocation) error { return nil }},
		(&cli.Command{Name: "group"}).Add(leaf),
	)
	defer func() {
		r := recover()
		if want := "cli: prog group leaf: PreRun set below the root"; fmt.Sprint(r) != want {
			t.Errorf("panic = %v, want %q", r, want)
		}
	}()

	// The mistake is on a path other than the one dispatched.
	dispatch(t, root, "a")
}
