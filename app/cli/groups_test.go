package cli_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/standards-lab/go-core/process"
)

// groupTree returns prog { cmd }, where prog defines the root flag --dsn
// and cmd the flags --a, --b, --c (strings) and --json, --yaml (bools),
// with wire applied to cmd. ran reports whether cmd ran.
func groupTree(wire func(cmd *cli.Command)) (root *cli.Command, ran *bool) {
	ran = new(bool)
	cmd := &cli.Command{Name: "cmd", Run: func(context.Context, *cli.Invocation) error {
		*ran = true
		return nil
	}}
	for _, name := range []string{"a", "b", "c"} {
		cmd.Flags().String(name, "", "flag "+name)
	}
	cmd.Flags().Bool("json", false, "print JSON")
	cmd.Flags().Bool("yaml", false, "print YAML")
	root = (&cli.Command{Name: "prog"}).Add(cmd)
	root.Flags().String("dsn", "", "database to connect to")
	wire(cmd)
	return root, ran
}

func TestRequire(t *testing.T) {
	tests := []struct {
		name     string
		required []string
		args     []string
		err      string // the error line, or "" for success
	}{
		{"given", []string{"a"}, []string{"cmd", "--a", "x"}, ""},
		{"given at its default", []string{"a"}, []string{"cmd", "--a="}, ""},
		{"bool given false", []string{"json"}, []string{"cmd", "--json=false"}, ""},
		{"missing", []string{"a"}, []string{"cmd"}, "prog cmd: required flag --a not set"},
		{"missing several, in declaration order", []string{"c", "a", "b"}, []string{"cmd", "--b", "x"},
			"prog cmd: required flags --c, --a not set"},
		{"root flag given at the root", []string{"dsn"}, []string{"--dsn", "x", "cmd"}, ""},
		{"root flag given after the leaf", []string{"dsn"}, []string{"cmd", "--dsn", "x"}, ""},
		{"root flag missing", []string{"dsn"}, []string{"cmd"}, "prog cmd: required flag --dsn not set"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, ran := groupTree(func(cmd *cli.Command) { cmd.Require(tt.required...) })

			r := dispatch(t, root, tt.args...)

			checkGroupResult(t, r, *ran, tt.err)
		})
	}
}

func TestExclusive(t *testing.T) {
	tests := []struct {
		name   string
		groups [][]string
		args   []string
		err    string // the error line, or "" for success
	}{
		{"none set", [][]string{{"a", "b"}}, nil, ""},
		{"one set", [][]string{{"a", "b"}}, []string{"--b", "x"}, ""},
		{"two set", [][]string{{"a", "b"}}, []string{"--a", "x", "--b", "y"},
			"prog cmd: flags --a, --b cannot be used together"},
		{"named in declaration order", [][]string{{"c", "b", "a"}}, []string{"--a", "x", "--b", "y", "--c", "z"},
			"prog cmd: flags --c, --b, --a cannot be used together"},
		{"set at the default counts", [][]string{{"json", "yaml"}}, []string{"--json=false", "--yaml"},
			"prog cmd: flags --json, --yaml cannot be used together"},
		{"one per group", [][]string{{"a", "b"}, {"json", "yaml"}}, []string{"--a", "x", "--yaml"}, ""},
		{"first broken group reported", [][]string{{"a", "b"}, {"json", "yaml"}}, []string{"--json", "--yaml", "--a", "x", "--b", "y"},
			"prog cmd: flags --a, --b cannot be used together"},
		{"root flag in a group", [][]string{{"dsn", "a"}}, []string{"--a", "x", "--dsn", "y"},
			"prog cmd: flags --dsn, --a cannot be used together"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, ran := groupTree(func(cmd *cli.Command) {
				for _, g := range tt.groups {
					cmd.Exclusive(g...)
				}
			})

			r := dispatch(t, root, append([]string{"cmd"}, tt.args...)...)

			checkGroupResult(t, r, *ran, tt.err)
		})
	}
}

func TestRequire_CheckedBeforeExclusive(t *testing.T) {
	root, ran := groupTree(func(cmd *cli.Command) {
		cmd.Exclusive("a", "b")
		cmd.Require("c")
	})

	r := dispatch(t, root, "cmd", "--a", "x", "--b", "y")

	checkGroupResult(t, r, *ran, "prog cmd: required flag --c not set")
}

// checkGroupResult checks a dispatch to cmd: success with cmd run when
// wantErr is empty, otherwise ExitUsage, wantErr with cmd's usage on
// stderr, and cmd not run.
func checkGroupResult(t *testing.T, r result, ran bool, wantErr string) {
	t.Helper()
	if wantErr == "" {
		if r.code != process.ExitOK || r.stderr != "" || !ran {
			t.Errorf("code = %d, stderr = %q, ran = %v, want %d, empty, true", r.code, r.stderr, ran, process.ExitOK)
		}
		return
	}
	if r.code != process.ExitUsage {
		t.Errorf("code = %d, want %d", r.code, process.ExitUsage)
	}
	want := wantErr + "\n" +
		"Usage: prog cmd [flags]\n" +
		"Run 'prog cmd --help' for details.\n"
	if r.stderr != want {
		t.Errorf("stderr =\n%s\nwant\n%s", r.stderr, want)
	}
	if ran {
		t.Error("cmd ran, want it not to")
	}
}

func TestRequire_HelpMarksRequiredFlags(t *testing.T) {
	root, _ := groupTree(func(cmd *cli.Command) { cmd.Require("b", "dsn") })

	r := dispatch(t, root, "cmd", "--help")

	want := `Usage:
  prog cmd [flags]

Flags:
  --a string   flag a
  --b string   flag b (required)
  --c string   flag c
  --json       print JSON
  --yaml       print YAML
  --help       Show help for prog cmd

Global flags:
  --dsn string   database to connect to (required)
`
	if r.code != process.ExitUsage || r.stdout != want {
		t.Errorf("code = %d, stdout =\n%s\nwant %d and\n%s", r.code, r.stdout, process.ExitUsage, want)
	}
}

func TestFlagGroups_PanicOnWiringMistake(t *testing.T) {
	tests := []struct {
		name string
		wire func(root, cmd, group *cli.Command)
		want string
	}{
		{"Require names an undefined flag", func(_, cmd, _ *cli.Command) { cmd.Require("a", "bogus") },
			"cli: prog cmd: Require names undefined flag --bogus"},
		{"Exclusive names an undefined flag", func(_, cmd, _ *cli.Command) { cmd.Exclusive("bogus", "a") },
			"cli: prog cmd: Exclusive names undefined flag --bogus"},
		{"Exclusive with one flag", func(_, cmd, _ *cli.Command) { cmd.Exclusive("a") },
			"cli: prog cmd: Exclusive group --a has fewer than two flags"},
		{"Require names a nested parent's flag", func(_, _, group *cli.Command) {
			group.Flags().Bool("deep", false, "")
			leaf := &cli.Command{Name: "leaf", Run: func(context.Context, *cli.Invocation) error { return nil }}
			group.Add(leaf)
			leaf.Require("deep")
		}, "cli: prog group leaf: Require names undefined flag --deep"},
		{"Require on a parent", func(_, _, group *cli.Command) { group.Require("dsn") },
			"cli: prog group: flag requirement or group set on a parent command"},
		{"Exclusive on the root parent", func(root, _, _ *cli.Command) { root.Exclusive("dsn", "dsn") },
			"cli: prog: flag requirement or group set on a parent command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok := &cli.Command{Name: "ok", Run: func(context.Context, *cli.Invocation) error { return nil }}
			cmd := &cli.Command{Name: "cmd", Run: func(context.Context, *cli.Invocation) error { return nil }}
			cmd.Flags().String("a", "", "")
			group := (&cli.Command{Name: "group"}).Add(&cli.Command{Name: "y", Run: func(context.Context, *cli.Invocation) error { return nil }})
			root := (&cli.Command{Name: "prog"}).Add(ok, cmd, group)
			root.Flags().String("dsn", "", "")
			tt.wire(root, cmd, group)
			defer func() {
				if r := recover(); fmt.Sprint(r) != tt.want {
					t.Errorf("panic = %v, want %q", r, tt.want)
				}
			}()

			// The mistake is on a path other than the one dispatched.
			dispatch(t, root, "ok")
		})
	}
}

func TestFlagGroups_RootFlagDefinedAfterRequire(t *testing.T) {
	cmd := &cli.Command{Name: "cmd", Run: func(context.Context, *cli.Invocation) error { return nil }}
	cmd.Require("dsn")
	root := (&cli.Command{Name: "prog"}).Add(cmd)
	root.Flags().String("dsn", "", "")

	r := dispatch(t, root, "cmd")

	if r.code != process.ExitUsage || !strings.HasPrefix(r.stderr, "prog cmd: required flag --dsn not set\n") {
		t.Errorf("code = %d, stderr =\n%s\nwant %d and the missing --dsn", r.code, r.stderr, process.ExitUsage)
	}
}
