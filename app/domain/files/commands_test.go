package files_test

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/standards-lab/blobfs"
	bfdata "github.com/standards-lab/blobfs/data"
	blobfspg "github.com/standards-lab/blobfs/postgres"
	"github.com/standards-lab/go-core/process"
	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/JaimeStill/spike-s3-storage/app/domain/files"
	"github.com/JaimeStill/spike-s3-storage/app/graph"
	"github.com/JaimeStill/spike-s3-storage/app/lifecycle"
)

// The commands driven through cli.Run over a graph of the domain's two
// nodes, built as the composition root builds them: the Service over the
// scripted driver, and the Storage over it and a started store on a fresh
// Fake. A run reads its arguments with the CLI-syntax parsers in Validate,
// so a refused term is a usage error before anything is built.

// result is what one run of a command reports.
type result struct {
	code           int
	stdout, stderr string
	rec            *sqltest.Recorder
}

// runCommand runs args under a root holding files.Commands, the Service's
// pool answering with responses.
func runCommand(t *testing.T, responses []sqltest.Response, args ...string) result {
	t.Helper()
	pool, rec := sqltest.Open(t, responses...)
	st, _ := startedStore(t)
	g := graph.New()
	svc := g.Define("files", func(*graph.Scope) (*files.Service, error) {
		return files.New(sqlate.Wrap(pool, sqltest.ReturningDialect{}), bfdata.WithEngine(blobfspg.Engine))
	})
	storage := g.Define("storage", func(s *graph.Scope) (*files.Storage, error) {
		return files.NewStorage(s.Use(svc), st), nil
	})
	config := g.Define("lifecycle config", func(*graph.Scope) (lifecycle.Config, error) {
		var cfg lifecycle.Config
		err := cfg.Finalize("FILES_TEST")
		return cfg, err
	})
	root := (&cli.Command{Name: "blobfs"}).Add(files.Commands(svc, storage)...)
	var out, errOut bytes.Buffer
	code := cli.Run(context.Background(), root, args, cli.Streams{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut}, cli.WithGraph(g, config))
	return result{code: code, stdout: out.String(), stderr: errOut.String(), rec: rec}
}

func TestStat_ReadsAnIDArgumentInCanonicalForm(t *testing.T) {
	// The id is upper case on the command line; the statement binds it in
	// blobfs's canonical form. The read itself is unscripted and fails.
	r := runCommand(t, nil, "stat", "id:00000000-0000-7000-8000-00000000000A")

	if r.code != process.ExitFailure {
		t.Errorf("code = %d, want %d; stderr = %q", r.code, process.ExitFailure, r.stderr)
	}
	var bound []any
	for _, c := range r.rec.Calls() {
		if c.Op == sqltest.OpQuery {
			bound = append(bound, c.Args...)
		}
	}
	if !slices.Contains(bound, any("00000000-0000-7000-8000-00000000000a")) {
		t.Errorf("queries bound %v, want the canonical id", bound)
	}
}

func TestLs_ReadsEachFilterAndSortTermIntoTheListing(t *testing.T) {
	// Each term reaches the file half's statement as the parser read it:
	// a value with colons kept whole, null with no value, in as a list,
	// and the sort terms in order with their directions.
	r := runCommand(t, []sqltest.Response{
		resolvedRoot(),
		sqltest.WithTotal(directories(), 0),
		listed(blobfs.RootID),
		sqltest.WithTotal(fileRows(), 0),
		listed(blobfs.RootID),
	}, "ls", "/",
		"--filter", "name:eq:a.txt",
		"--filter", "created_at:ge:2026-01-01T00:00:00Z",
		"--filter", "etag:null",
		"--filter", "status:in:available,pending",
		"--sort", "size:desc", "--sort", "name:asc")

	if r.code != process.ExitOK {
		t.Fatalf("code = %d, stderr = %q", r.code, r.stderr)
	}
	var queries []sqltest.Call
	for _, c := range r.rec.Calls() {
		if c.Op == sqltest.OpQuery {
			queries = append(queries, c)
		}
	}
	if len(queries) != 5 {
		t.Fatalf("queries = %d, want 5", len(queries))
	}
	fs := queries[3]
	for _, want := range []string{"ORDER BY q.size DESC, q.name OFFSET", "q.etag IS NULL"} {
		if !strings.Contains(fs.SQL, want) {
			t.Errorf("the file half's statement:\n%s\nwant %q", fs.SQL, want)
		}
	}
	for _, want := range []any{"a.txt", "2026-01-01T00:00:00Z", "available", "pending"} {
		if !slices.Contains(fs.Args, want) {
			t.Errorf("the file half bound %v, want %v", fs.Args, want)
		}
	}
}

func TestCommands_RefuseMalformedInputBeforeAnythingIsBuilt(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"a filter with no operator", []string{"ls", "/", "--filter", "name"}, "write <field>:<op>:<value>"},
		{"a filter with no field", []string{"ls", "/", "--filter", ":eq:x"}, "write <field>:<op>:<value>"},
		{"a filter with an empty operator", []string{"ls", "/", "--filter", "name::x"}, "names no operator"},
		{"a filter with no value", []string{"ls", "/", "--filter", "name:eq"}, "names no value"},
		{"null with a value", []string{"ls", "/", "--filter", "etag:null:x"}, "null takes no value"},
		{"in with no list", []string{"ls", "/", "--filter", "status:in"}, "in takes a comma-separated list"},
		{"a sort with no field", []string{"ls", "/", "--sort", ":desc"}, "names no field"},
		{"an empty sort", []string{"ls", "/", "--sort="}, "names no field"},
		{"a sort with an unknown direction", []string{"ls", "/", "--sort", "name:up"}, "the direction is asc or desc"},
		{"a bookmark sort with an unknown direction", []string{"bookmark", "ls", "--unit", unitID, "--sort", "path:up"}, "the direction is asc or desc"},
		{"an unknown total mode", []string{"ls", "/", "--total", "some"}, `--total "some": the mode is exact or none`},
		{"a unit that is not a UUID", []string{"mkdir", "/x", "--unit", "nope"}, `--unit "nope" is not a UUID`},
		{"an id that is not a UUID", []string{"stat", "id:nope"}, "must be a UUID"},
		{"the root's id", []string{"stat", "id:" + blobfs.RootID}, blobfs.ErrInvalidID.Error()},
		{"mkdir by id", []string{"mkdir", "id:" + dirID}, "blobfs mkdir: a directory is created by path, not by id\n"},
		{"put - into a directory by id", []string{"put", "-", "id:" + dirID}, "blobfs put: stdin has no name to store under"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := runCommand(t, nil, tt.args...)

			if r.code != process.ExitUsage {
				t.Errorf("code = %d, want %d; stderr = %q", r.code, process.ExitUsage, r.stderr)
			}
			if !strings.Contains(r.stderr, tt.want) || !strings.Contains(r.stderr, "Usage:") {
				t.Errorf("stderr = %q, want %q with the usage", r.stderr, tt.want)
			}
			if calls := r.rec.Calls(); len(calls) != 0 {
				t.Errorf("calls = %v, want none: nothing is built", r.rec.Ops())
			}
		})
	}
}

func TestCommands_AddFlagWordingToTheDomainsRefusals(t *testing.T) {
	// The unit rules are the domain's, run in Validate: each refusal is a
	// usage error worded with the command's flags, and nothing is built.
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"mkdir --unit below the top level", []string{"mkdir", "/reports/2026", "--unit", unitID},
			"blobfs mkdir: ownership applies to a top-level directory only; give --unit with a top-level path only\n"},
		{"ls / --unit after a directory cursor", []string{"ls", "/", "--unit", unitID, "--after-dirs", "c"},
			"blobfs ls: the owner listing pages by number only; ls / --unit takes no --after-dirs or --after-files\n"},
		{"ls / --unit after a file cursor", []string{"ls", "/", "--unit", unitID, "--after-files", "c"},
			"blobfs ls: the owner listing pages by number only; ls / --unit takes no --after-dirs or --after-files\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := runCommand(t, nil, tt.args...)

			if r.code != process.ExitUsage {
				t.Errorf("code = %d, want %d; stderr = %q", r.code, process.ExitUsage, r.stderr)
			}
			if !strings.Contains(r.stderr, tt.want) || !strings.Contains(r.stderr, "Usage:") {
				t.Errorf("stderr = %q, want %q with the usage", r.stderr, tt.want)
			}
			if r.stdout != "" {
				t.Errorf("stdout = %q, want empty", r.stdout)
			}
			if calls := r.rec.Calls(); len(calls) != 0 {
				t.Errorf("calls = %v, want none: nothing is built", r.rec.Ops())
			}
		})
	}
}
