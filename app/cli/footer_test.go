package cli_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/standards-lab/go-core/process"
)

// footerTree returns the fixture's tree with a Footer on the root that
// writes a section of its own.
func footerTree() *fixture {
	f := newFixture(nil)
	f.root.Footer = func(w io.Writer) {
		_, _ = fmt.Fprint(w, "Topics:\n  one   The first topic\n")
	}
	return f
}

func TestRun_FooterEndsTheCommandsFullHelp(t *testing.T) {
	const tail = "\nRun 'prog <command> --help' for help on a command.\n\n" +
		"Topics:\n  one   The first topic\n"
	tests := []struct {
		name   string
		args   []string
		stderr bool // whether the help goes to stderr
	}{
		{"--help", []string{"--help"}, false},
		{"-h", []string{"-h"}, false},
		{"no subcommand", nil, false},
		{"unknown subcommand", []string{"bogus"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := footerTree()

			r := dispatch(t, f.root, tt.args...)

			if r.code != process.ExitUsage {
				t.Errorf("code = %d, want %d", r.code, process.ExitUsage)
			}
			got, other := r.stdout, r.stderr
			if tt.stderr {
				got, other = r.stderr, r.stdout
			}
			if !strings.HasSuffix(got, tail) {
				t.Errorf("help =\n%s\nwant it to end with\n%s", got, tail)
			}
			if strings.Count(got, "Topics:") != 1 {
				t.Errorf("help =\n%s\nwant the footer once", got)
			}
			if other != "" {
				t.Errorf("other stream = %q, want empty", other)
			}
		})
	}
}

func TestRun_FooterIsNotInherited(t *testing.T) {
	for _, args := range [][]string{{"echo", "--help"}, {"group"}, {"group", "leaf", "-h"}} {
		f := footerTree()

		r := dispatch(t, f.root, args...)

		if !strings.Contains(r.stdout, "Usage:") {
			t.Errorf("%v: stdout =\n%s\nwant the command's help", args, r.stdout)
		}
		if strings.Contains(r.stdout+r.stderr, "Topics:") {
			t.Errorf("%v: stdout =\n%s\nstderr =\n%s\nwant no parent's footer", args, r.stdout, r.stderr)
		}
	}
}

func TestRun_FooterIsNotInAUsageError(t *testing.T) {
	f := footerTree()

	r := dispatch(t, f.root, "--bogus")

	if r.code != process.ExitUsage {
		t.Errorf("code = %d, want %d", r.code, process.ExitUsage)
	}
	if !strings.Contains(r.stderr, "Usage:") || strings.Contains(r.stdout+r.stderr, "Topics:") {
		t.Errorf("stdout =\n%s\nstderr =\n%s\nwant the short usage on stderr without the footer", r.stdout, r.stderr)
	}
}

func TestRun_FooterEndsALeafsHelp(t *testing.T) {
	leaf := &cli.Command{
		Name:   "note",
		Run:    func(context.Context, *cli.Invocation) error { return nil },
		Footer: func(w io.Writer) { _, _ = fmt.Fprint(w, "Examples:\n  prog note\n") },
	}
	root := (&cli.Command{Name: "prog"}).Add(leaf)

	r := dispatch(t, root, "note", "--help")

	want := "Usage:\n  prog note [flags]\n\nFlags:\n  --help   Show help for prog note\n\n" +
		"Examples:\n  prog note\n"
	if r.code != process.ExitUsage || r.stdout != want {
		t.Errorf("code = %d, stdout =\n%s\nwant %d,\n%s", r.code, r.stdout, process.ExitUsage, want)
	}
}

func TestRun_FooterWritingNothingAddsNothing(t *testing.T) {
	plain := dispatch(t, newFixture(nil).root, "--help")
	f := newFixture(nil)
	f.root.Footer = func(io.Writer) {}

	r := dispatch(t, f.root, "--help")

	if r.stdout != plain.stdout {
		t.Errorf("stdout =\n%s\nwant the help without a footer\n%s", r.stdout, plain.stdout)
	}
}
