package app_test

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/go-core/process"
	godatabase "github.com/standards-lab/go-database"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/JaimeStill/spike-s3-storage/app/domain/files"
	"github.com/JaimeStill/spike-s3-storage/app/internal/app"
	"github.com/JaimeStill/spike-s3-storage/app/internal/apptest"
)

// The directory commands driven through App.Run over buffers: the nodes
// they declare, the files node's statement check at start, and their
// output over a scripted database. The stack-backed runs are the
// integration package's, over the built binary.

const (
	reportsID = "00000000-0000-7000-8000-000000000001"
	planID    = "00000000-0000-7000-8000-000000000002"
	unitID    = "00000000-0000-7000-8000-0000000000aa"
	yearID    = "00000000-0000-7000-8000-000000000005"
)

// planRow is the available file plan.txt, 12 bytes, in the directory with
// directory.
func planRow(directory string) []driver.Value {
	return []driver.Value{planID, directory, "plan.txt", "available", planID + "/plan.txt", int64(12), "text/plain", `"e"`, int64(2), stamp, stamp}
}

// directoryVerbs are the directory and bookmark commands, each as a run
// that reaches its body.
var directoryVerbs = [][]string{
	{"mkdir", "/reports"},
	{"mkdir", "/reports", "--unit", unitID},
	{"ls", "/"},
	{"ls", "/", "--unit", unitID},
	{"ls", "/reports", "--unit", unitID},
	{"ls", "id:" + reportsID},
	{"ls", "id:" + reportsID, "--unit", unitID},
	{"stat", "/reports"},
	{"stat", "id:" + reportsID},
	{"mv", "/reports", "/archive"},
	{"mv", "id:" + planID, "id:" + reportsID},
	{"mv", "/reports/plan.txt", "id:" + reportsID},
	{"mv", "id:" + planID, "/reports"},
	{"rmdir", "/reports"},
	{"rmdir", "id:" + reportsID},
	{"bookmark", "add", "/reports/plan.txt", "--unit", unitID, "--active"},
	{"bookmark", "add", "id:" + planID, "--unit", unitID},
	{"bookmark", "ls", "--unit", unitID},
	{"bookmark", "rm", "/reports/plan.txt", "--unit", unitID},
	{"bookmark", "rm", "id:" + planID, "--unit", unitID},
}

// commandPath returns the path of the command args run, as the dispatcher
// labels its errors: the bookmark subcommand under its parent, any other
// command alone.
func commandPath(args []string) string {
	if args[0] == "bookmark" {
		return "blobfs bookmark " + args[1]
	}
	return "blobfs " + args[0]
}

func TestFiles_CommandsBuildTheDatabaseAndNeverTheStore(t *testing.T) {
	for _, args := range directoryVerbs {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			clearEnv(t)
			t.Setenv(godatabase.NewEnv("BLOBFS").Name, "app")
			var out, errOut bytes.Buffer
			// The production constructors run, and none does I/O; the
			// lifecycle configuration, the Build's last root, is halted, so
			// the Build constructs the files node and everything it reaches,
			// and stops before anything starts: a store or storage
			// configuration it reached would be recorded.
			a, built := haltedApp(&out, &errOut)

			code := a.Run(context.Background(), args)

			if code != process.ExitFailure {
				t.Errorf("code = %d, want %d", code, process.ExitFailure)
			}
			if want := commandPath(args) + ": lifecycle config: halted\n"; errOut.String() != want {
				t.Errorf("stderr = %q, want %q", errOut.String(), want)
			}
			wantBuilt := []string{"files", "sql", "database", "database config", "lifecycle config"}
			if got := built.Log(); !slices.Equal(got, wantBuilt) {
				t.Errorf("nodes built = %q, want %q", got, wantBuilt)
			}
		})
	}
}

// scriptedBuilt is what a run of a command that declares the files node
// builds over a scripted database, which reads no configuration node: the
// files node, the sql node, the database, and the lifecycle configuration,
// and never the store or its configuration.
var scriptedBuilt = []string{"files", "sql", "database", "lifecycle config"}

// scriptedApp returns blobfs with its database node built over sqlate's
// scripted driver, answering with responses, by apptest.ScriptDatabase,
// and a Recorder of the nodes its runs build. The files node keeps its
// production constructor, statement check included, and the store and its
// configuration theirs: the environment is cleared, so a run that reached
// them would fail on the storage configuration, and be recorded.
func scriptedApp(t *testing.T, stdout, stderr *bytes.Buffer, responses ...sqltest.Response) (*app.App, *apptest.Recorder, *sqltest.Recorder) {
	t.Helper()
	a, built, rec, _ := scriptedAppIn(t, strings.NewReader(""), stdout, stderr, responses...)
	return a, built, rec
}

// scriptedAppIn is scriptedApp reading stdin, and it returns the scripted
// pool too, so a test can see whether the database was shut down.
func scriptedAppIn(t *testing.T, stdin io.Reader, stdout, stderr *bytes.Buffer, responses ...sqltest.Response) (*app.App, *apptest.Recorder, *sqltest.Recorder, *sql.DB) {
	t.Helper()
	clearEnv(t)
	a := app.New(cli.Streams{Stdin: stdin, Stdout: stdout, Stderr: stderr})
	built := apptest.Builds(a)
	rec, pool := apptest.ScriptDatabase(t, a, responses...)
	return a, built, rec, pool
}

func TestFiles_AnUnappliedSchemaFailsAtStartNamingTheFilesNode(t *testing.T) {
	for _, args := range directoryVerbs {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			a, built, rec := scriptedApp(t, &out, &errOut)
			rec.FailPrepare = func(string) error { return errors.New(`relation "blobfs_directory" does not exist`) }

			code := a.Run(context.Background(), args)

			if code != process.ExitFailure {
				t.Errorf("code = %d, want %d", code, process.ExitFailure)
			}
			want := commandPath(args) + ": files: " + files.ErrVerify.Error()
			if !strings.HasPrefix(errOut.String(), want) {
				t.Errorf("stderr = %q, want it to start with %q", errOut.String(), want)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want empty", out.String())
			}
			// The body never ran: the statement check prepared and nothing
			// else reached the database.
			if ops := rec.Ops(); slices.ContainsFunc(ops, func(op sqltest.Op) bool { return op != sqltest.OpPrepare }) {
				t.Errorf("ops = %v, want prepares only", ops)
			}
			if got := built.Log(); !slices.Equal(got, scriptedBuilt) {
				t.Errorf("nodes built = %q, want %q: neither the store nor its configuration", got, scriptedBuilt)
			}
		})
	}
}

var (
	directoryColumns = []string{"id", "parent_id", "name", "status", "version", "created_at", "updated_at"}
	fileColumns      = []string{"id", "directory_id", "name", "status", "key", "size", "content_type", "etag", "version", "created_at", "updated_at"}
	stamp            = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
)

func directoryRows(rows ...[]driver.Value) sqltest.Response {
	return sqltest.Response{Columns: directoryColumns, Rows: rows}
}

func directoryRow(id string, parent any, name string) []driver.Value {
	return []driver.Value{id, parent, name, "active", int64(1), stamp, stamp}
}

// resolvedRoot is the Postgres engine's path resolution of /.
func resolvedRoot() sqltest.Response {
	return resolvedAt(blobfs.RootID, nil, "/", 0)
}

// resolvedAt is the Postgres engine's path resolution reaching the
// directory at depth, the number of segments it matched; a depth short of
// the path's segments is a path that does not resolve.
func resolvedAt(id string, parent any, name string, depth int64) sqltest.Response {
	return sqltest.Response{
		Columns: append(slices.Clone(directoryColumns), "depth"),
		Rows:    [][]driver.Value{append(directoryRow(id, parent, name), depth)},
	}
}

// ancestors is the Postgres engine's read of a directory's chain of
// parents, as blobfs computes a path from an id: rows from the directory
// up, ending below the root, which the read appends.
func ancestors(rows ...[]driver.Value) sqltest.Response {
	return sqltest.Response{
		Columns: []string{"id", "parent_id", "name"},
		Rows:    append(rows, []driver.Value{blobfs.RootID, nil, "/"}),
	}
}

func TestFiles_DirectoryCommandsOverAScriptedDatabase(t *testing.T) {
	root := directoryRows(directoryRow(blobfs.RootID, nil, "/"))
	tests := []struct {
		name      string
		args      []string
		responses []sqltest.Response
		want      string
	}{
		{
			name:      "mkdir",
			args:      []string{"mkdir", "/reports"},
			responses: []sqltest.Response{resolvedRoot(), directoryRows(directoryRow(reportsID, blobfs.RootID, "reports"))},
			want:      "mkdir: /reports (id " + reportsID + ")\n",
		},
		{
			name: "ls",
			args: []string{"ls", "/"},
			responses: []sqltest.Response{
				resolvedRoot(),
				sqltest.WithTotal(directoryRows(directoryRow(reportsID, blobfs.RootID, "reports")), 1),
				root,
				sqltest.WithTotal(sqltest.Response{Columns: fileColumns, Rows: [][]driver.Value{
					{planID, blobfs.RootID, "plan.txt", "available", planID + "/plan.txt", int64(12), "text/plain", `"e"`, int64(2), stamp, stamp},
				}}, 1),
				root,
			},
			want: "" +
				"KIND  NAME      SIZE  STATUS     UPDATED              ID\n" +
				"dir   reports   -     -          2026-10-06 12:00:00  " + reportsID + "\n" +
				"file  plan.txt  12    available  2026-10-06 12:00:00  " + planID + "\n" +
				"directories: 1 on page 1 of size 20, total 1\n" +
				"more: no\n" +
				"files: 1 on page 1 of size 20, total 1\n" +
				"more: no\n",
		},
		{
			name: "rmdir by id",
			args: []string{"rmdir", "id:" + planID},
			responses: []sqltest.Response{
				directoryRows(directoryRow(planID, reportsID, "2026")),
				ancestors([]driver.Value{planID, reportsID, "2026"}, []driver.Value{reportsID, blobfs.RootID, "reports"}),
				{},
				{Affected: 1},
			},
			want: "rmdir: /reports/2026 (id " + planID + ")\n",
		},
		{
			// The destination by id resolves first, then the source by
			// path: /reports/plan.txt is no directory, so it is the file
			// in /reports.
			name: "mv a path into an id",
			args: []string{"mv", "/reports/plan.txt", "id:" + yearID},
			responses: []sqltest.Response{
				ancestors([]driver.Value{yearID, reportsID, "2026"}, []driver.Value{reportsID, blobfs.RootID, "reports"}),
				resolvedAt(reportsID, blobfs.RootID, "reports", 1),
				resolvedAt(reportsID, blobfs.RootID, "reports", 1),
				{Columns: fileColumns, Rows: [][]driver.Value{planRow(reportsID)}},
				{Columns: fileColumns, Rows: [][]driver.Value{planRow(yearID)}},
			},
			want: "mv: /reports/plan.txt -> /reports/2026/plan.txt (id " + planID + ")\n",
		},
		{
			// The source by id is read first, for its name and path, then
			// the destination path, an existing directory.
			name: "mv an id into a path",
			args: []string{"mv", "id:" + planID, "/reports/2026"},
			responses: []sqltest.Response{
				{Columns: fileColumns, Rows: [][]driver.Value{planRow(reportsID)}},
				ancestors([]driver.Value{reportsID, blobfs.RootID, "reports"}),
				resolvedAt(yearID, reportsID, "2026", 2),
				{Columns: fileColumns, Rows: [][]driver.Value{planRow(yearID)}},
			},
			want: "mv: /reports/plan.txt -> /reports/2026/plan.txt (id " + planID + ")\n",
		},
		{
			// No file has the id, so the directory's row is read, then its
			// path, which leads the record as it does by path.
			name: "stat by id",
			args: []string{"stat", "id:" + reportsID},
			responses: []sqltest.Response{
				{Columns: fileColumns},
				directoryRows(directoryRow(reportsID, blobfs.RootID, "reports")),
				ancestors([]driver.Value{reportsID, blobfs.RootID, "reports"}),
			},
			want: "" +
				"path:    /reports\n" +
				"id:      " + reportsID + "\n" +
				"parent:  " + blobfs.RootID + "\n" +
				"name:    reports\n" +
				"version: 1\n" +
				"created: 2026-10-06T12:00:00Z\n" +
				"updated: 2026-10-06T12:00:00Z\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			a, built, rec := scriptedApp(t, &out, &errOut, tt.responses...)

			code := a.Run(context.Background(), tt.args)

			if code != process.ExitOK {
				t.Fatalf("code = %d, want %d; stderr = %q", code, process.ExitOK, errOut.String())
			}
			if out.String() != tt.want {
				t.Errorf("stdout =\n%s\nwant\n%s", out.String(), tt.want)
			}
			if errOut.Len() != 0 {
				t.Errorf("stderr = %q, want empty", errOut.String())
			}
			if n := rec.Pending(); n != 0 {
				t.Errorf("%d scripted responses unconsumed", n)
			}
			// The statement check ran at start, before the body's first
			// statement.
			if ops := rec.Ops(); len(ops) == 0 || ops[0] != sqltest.OpPrepare {
				t.Errorf("ops = %v, want the statement check's prepares first", ops)
			}
			if got := built.Log(); !slices.Equal(got, scriptedBuilt) {
				t.Errorf("nodes built = %q, want %q: neither the store nor its configuration", got, scriptedBuilt)
			}
		})
	}
}

func TestFiles_HelpListsTheDirectoryCommands(t *testing.T) {
	code, stdout, _ := run(t)

	if code != process.ExitUsage {
		t.Errorf("code = %d, want %d", code, process.ExitUsage)
	}
	want := "" +
		"  mkdir      Create a directory under an existing parent\n" +
		"  ls         List a directory: its directories, then its files, one page each\n" +
		"  stat       Show a file's or a directory's row, one field per line\n" +
		"  mv         Move or rename a directory or a file within its top-level directory\n" +
		"  rmdir      Remove an empty directory\n" +
		"  bookmark   Bookmark files for a unit, at most one of them active: add, ls, rm\n"
	if !strings.Contains(stdout, want) {
		t.Errorf("stdout =\n%s\nwant it to contain\n%s", stdout, want)
	}
}

func TestFiles_LsHelpListsItsFlags(t *testing.T) {
	code, stdout, _ := run(t, "ls", "--help")

	if code != process.ExitUsage {
		t.Errorf("code = %d, want %d", code, process.ExitUsage)
	}
	for _, flag := range []string{"--page int", "--size int", "--sort string", "--filter string", "--total string", "--after-dirs string", "--after-files string", "--cursors"} {
		if !strings.Contains(stdout, flag) {
			t.Errorf("stdout =\n%s\nwant it to list %s", stdout, flag)
		}
	}
	if !strings.Contains(stdout, "Usage:\n  blobfs ls [flags] <path|id:<uuid>>\n") {
		t.Errorf("stdout =\n%s\nwant the usage line", stdout)
	}
}

func TestFiles_MalformedArgumentsAreUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"an id that is not a UUID", []string{"ls", "id:nope"}, "blobfs ls: blobfs: invalid id \"nope\": must be a UUID\n"},
		{"the root's id", []string{"stat", "id:" + blobfs.RootID}, "the nil UUID is the root's"},
		{"a total mode", []string{"ls", "/", "--total", "some"}, "--total \"some\": the mode is exact or none"},
		{"a filter with no operator", []string{"ls", "/", "--filter", "name"}, "write <field>:<op>:<value>"},
		{"a sort direction", []string{"ls", "/", "--sort", "name:up"}, "the direction is asc or desc"},
		{"a page that is not a number", []string{"ls", "/", "--page", "two"}, "invalid value \"two\" for flag -page"},
		{"mkdir without a path", []string{"mkdir"}, "accepts 1 argument, got 0"},
		{"mv with one path", []string{"mv", "/a"}, "accepts 2 arguments, got 1"},
		{"mkdir of a relative path", []string{"mkdir", "a/b"}, "blobfs mkdir: blobfs: invalid path: \"a/b\" does not start with /\n"},
		{"mkdir --unit of a relative path", []string{"mkdir", "a/b", "--unit", unitID}, "blobfs mkdir: blobfs: invalid path: \"a/b\" does not start with /\n"},
		{"mkdir of a trailing slash", []string{"mkdir", "/reports/"}, "invalid path: \"/reports/\": segment 2: blobfs: invalid name \"\": must not be empty"},
		{"ls of a relative path", []string{"ls", "relative"}, "blobfs ls: blobfs: invalid path: \"relative\" does not start with /\n"},
		{"ls of a dot segment", []string{"ls", "/reports/../x"}, "segment 2: blobfs: invalid name \"..\": must not be . or .."},
		{"mv of two relative paths", []string{"mv", "a", "b"}, "blobfs mv: blobfs: invalid path: \"a\" does not start with /\n"},
		{"mv to a relative path", []string{"mv", "/a", "b"}, "blobfs mv: blobfs: invalid path: \"b\" does not start with /\n"},
		{"stat of a relative path", []string{"stat", "reports"}, "blobfs stat: blobfs: invalid path: \"reports\" does not start with /\n"},
		{"rmdir of the root", []string{"rmdir", "/"}, "blobfs rmdir: blobfs: the root directory; rmdir removes a directory below the root\n"},
		{"rmdir of the root's id", []string{"rmdir", "id:" + blobfs.RootID}, "the nil UUID is the root's"},
		{"bookmark add of the root", []string{"bookmark", "add", "/", "--unit", unitID}, "blobfs bookmark add: blobfs: the root directory; bookmark add takes a file\n"},
		{"mv and its synopsis", []string{"mv", "a", "b"}, "Usage: blobfs mv [flags] <path|id:<uuid>> <path|id:<uuid>>"},
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
			if !strings.Contains(errOut.String(), tt.want) || !strings.Contains(errOut.String(), "Usage: "+commandPath(tt.args)+" [flags]") {
				t.Errorf("stderr = %q, want %q and the usage line", errOut.String(), tt.want)
			}
			if got := built.Log(); len(got) != 0 {
				t.Errorf("nodes built = %q, want none", got)
			}
		})
	}
}
