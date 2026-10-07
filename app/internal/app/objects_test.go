package app_test

import (
	"bytes"
	"context"
	"database/sql/driver"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/go-core/process"
	"github.com/standards-lab/go-core/process/processtest"
	godatabase "github.com/standards-lab/go-database"
	"github.com/standards-lab/go-storage"
	"github.com/standards-lab/go-storage/storagetest"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/JaimeStill/spike-s3-storage/app/internal/apptest"
)

// The object commands driven through App.Run over buffers: the nodes they
// declare, standard input reaching put through the dispatcher, and a store
// that cannot be reached failing them at start. The object store is
// go-storage's Store over its in-memory Fake; the stack-backed runs are
// the integration package's, over the built binary.

// objectVerbs are the object commands, each as a run that reaches its
// body.
var objectVerbs = [][]string{
	{"put", "-", "/a.txt"},
	{"put", "local.txt", "id:" + reportsID},
	{"cat", "/a.txt"},
	{"cat", "id:" + planID},
	{"cp", "/a.txt", "/b.txt"},
	{"cp", "id:" + planID, "id:" + reportsID},
	{"cp", "id:" + planID, "/b.txt"},
	{"cp", "/a.txt", "id:" + reportsID},
	{"rm", "/a.txt"},
	{"rm", "id:" + planID},
	{"rm", "--recursive", "/reports"},
	{"rm", "--recursive", "id:" + reportsID},
}

func TestObjects_CommandsBuildTheDatabaseAndTheStore(t *testing.T) {
	for _, args := range objectVerbs {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			clearEnv(t)
			t.Setenv(godatabase.NewEnv("BLOBFS").Name, "app")
			var out, errOut bytes.Buffer
			// The production constructors run but the store's, which is
			// built over a Fake, and none does I/O; the lifecycle
			// configuration, the Build's last root, is halted, so the Build
			// constructs the storage node and everything it reaches, and
			// stops before anything starts.
			a, built := haltedApp(&out, &errOut)
			apptest.FakeStore(t, a, storagetest.NewFake())

			code := a.Run(context.Background(), args)

			if code != process.ExitFailure {
				t.Errorf("code = %d, want %d", code, process.ExitFailure)
			}
			if want := "blobfs " + args[0] + ": lifecycle config: halted\n"; errOut.String() != want {
				t.Errorf("stderr = %q, want %q", errOut.String(), want)
			}
			wantBuilt := []string{"storage", "files", "sql", "database", "database config", "store", "lifecycle config"}
			if got := built.Log(); !slices.Equal(got, wantBuilt) {
				t.Errorf("nodes built = %q, want %q", got, wantBuilt)
			}
		})
	}
}

// objectApp is scriptedAppIn with the store node built over fake, by
// apptest.FakeStore. It returns the scripted driver's recorder, a run of
// the App, and a ping of the scripted pool.
func objectApp(t *testing.T, fake *storagetest.Fake, stdin io.Reader, stdout, stderr *bytes.Buffer, responses ...sqltest.Response) (*sqltest.Recorder, func(args ...string) int, func() error) {
	t.Helper()
	a, _, rec, pool := scriptedAppIn(t, stdin, stdout, stderr, responses...)
	apptest.FakeStore(t, a, fake)
	run := func(args ...string) int { return a.Run(context.Background(), args) }
	return rec, run, func() error { return pool.Ping() }
}

func TestObjects_PutDashReadsTheInvocationsStdin(t *testing.T) {
	const fileID = "00000000-0000-7000-8000-000000000003"
	fake := storagetest.NewFake()
	stdin := strings.NewReader("piped bytes")
	var out, errOut bytes.Buffer
	rec, run, _ := objectApp(t, fake, stdin, &out, &errOut,
		resolvedRoot(),
		sqltest.Response{Columns: fileColumns},
		sqltest.Response{Columns: fileColumns, Rows: [][]driver.Value{
			{fileID, blobfs.RootID, "a.txt", "pending", fileID + "/a.txt", nil, "application/octet-stream", nil, int64(1), stamp, stamp},
		}},
		sqltest.Response{Columns: fileColumns, Rows: [][]driver.Value{
			{fileID, blobfs.RootID, "a.txt", "available", fileID + "/a.txt", int64(11), "application/octet-stream", `"e"`, int64(2), stamp, stamp},
		}},
	)

	code := run("put", "-", "/a.txt")

	if code != process.ExitOK {
		t.Fatalf("code = %d, want %d; stderr = %q", code, process.ExitOK, errOut.String())
	}
	if want := "put: /a.txt (id " + fileID + ", 11 bytes, etag \"e\")\n"; out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
	if stdin.Len() != 0 {
		t.Errorf("stdin has %d bytes unread, want the whole body read", stdin.Len())
	}
	blob, err := fake.Get(context.Background(), fileID+"/a.txt", storage.GetOptions{})
	if err != nil {
		t.Fatalf("the store holds no object: %v", err)
	}
	if b, _ := io.ReadAll(blob.Body); string(b) != "piped bytes" {
		t.Errorf("the object = %q, want stdin's bytes", b)
	}
	if opts, _ := fake.LastPut(); opts.ContentType != "application/octet-stream" || opts.Size != 0 {
		t.Errorf("the put's options = %+v, want octet-stream and an unknown size", opts)
	}
	if n := rec.Pending(); n != 0 {
		t.Errorf("%d scripted responses unconsumed", n)
	}
}

// TestObjects_PutDashEndsWithTheContextWhileStdinBlocks holds standard
// input open on a pipe nothing writes to, whose Read blocks whatever the
// context, as a read of a pipe does in the kernel. The context ends once
// the store's Put is reading the body: the run abandons the pending row
// and returns with the cancellation while the read still blocks.
func TestObjects_PutDashEndsWithTheContextWhileStdinBlocks(t *testing.T) {
	const fileID = "00000000-0000-7000-8000-000000000003"
	stdin, held := io.Pipe()
	defer func() { _ = held.Close() }()
	fake := storagetest.NewFake()
	var out, errOut bytes.Buffer
	a, _, rec, _ := scriptedAppIn(t, stdin, &out, &errOut,
		resolvedRoot(),
		sqltest.Response{Columns: fileColumns},
		sqltest.Response{Columns: fileColumns, Rows: [][]driver.Value{
			{fileID, blobfs.RootID, "a.txt", "pending", fileID + "/a.txt", nil, "application/octet-stream", nil, int64(1), stamp, stamp},
		}},
		// The abandon of the pending row: marked deleting, then purged.
		sqltest.Response{Columns: fileColumns, Rows: [][]driver.Value{
			{fileID, blobfs.RootID, "a.txt", "deleting", fileID + "/a.txt", nil, "application/octet-stream", nil, int64(2), stamp, stamp},
		}},
		sqltest.Response{Affected: 1},
	)
	apptest.FakeStore(t, a, fake)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- a.Run(ctx, []string{"put", "-", "/a.txt"}) }()

	processtest.WaitFor(t, "the store's Put", func() bool { return fake.Puts() == 1 })
	cancel()
	select {
	case code := <-done:
		if code != process.ExitFailure {
			t.Errorf("code = %d, want %d", code, process.ExitFailure)
		}
	case <-time.After(processtest.Failsafe):
		t.Fatal("put did not return once its context ended, its stdin still blocking")
	}
	if !strings.HasPrefix(errOut.String(), "blobfs put: files: put /a.txt: ") ||
		!strings.HasSuffix(errOut.String(), ": "+context.Canceled.Error()+"\n") {
		t.Errorf("stderr = %q, want the put's cancellation", errOut.String())
	}
	if n := rec.Pending(); n != 0 {
		t.Errorf("%d scripted responses unconsumed", n)
	}
}

func TestObjects_SuccessLinesNameTheResolvedPath(t *testing.T) {
	// Each run names an entry by id, alone or beside a path, and its
	// success line names every entry by the path the operation resolved.
	const copyID = "00000000-0000-7000-8000-000000000006"
	local := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(local, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	reports := []driver.Value{reportsID, blobfs.RootID, "reports"}
	files := func(rows ...[]driver.Value) sqltest.Response {
		return sqltest.Response{Columns: fileColumns, Rows: rows}
	}
	tests := []struct {
		name      string
		args      []string
		responses []sqltest.Response
		want      string
	}{
		{
			// The file by id and its path in the delete's first
			// transaction, the hold, the bookmark count, the delete, and
			// the purge.
			name: "rm by id",
			args: []string{"rm", "id:" + planID},
			responses: []sqltest.Response{
				files(planRow(reportsID)),
				ancestors(reports),
				{Columns: []string{"id"}, Rows: [][]driver.Value{{planID}}},
				{Columns: []string{"n"}, Rows: [][]driver.Value{{int64(0)}}},
				files([]driver.Value{planID, reportsID, "plan.txt", "deleting", planID + "/plan.txt", int64(12), "text/plain", `"e"`, int64(3), stamp, stamp}),
				{Affected: 1},
			},
			want: "rm: /reports/plan.txt (id " + planID + ")\n",
		},
		{
			// The directory's path, the name's lookup, the pending row,
			// and the completion.
			name: "put into a directory by id",
			args: []string{"put", local, "id:" + reportsID},
			responses: []sqltest.Response{
				ancestors(reports),
				files(),
				files([]driver.Value{copyID, reportsID, "notes.txt", "pending", copyID + "/notes.txt", nil, "text/plain", nil, int64(1), stamp, stamp}),
				files([]driver.Value{copyID, reportsID, "notes.txt", "available", copyID + "/notes.txt", int64(5), "text/plain", `"e"`, int64(2), stamp, stamp}),
			},
			want: "put: /reports/notes.txt (id " + copyID + ", 5 bytes, etag \"e\")\n",
		},
		{
			// The source by id and its path, then the destination path,
			// an existing directory, then the copy's write.
			name: "cp an id into a path",
			args: []string{"cp", "id:" + planID, "/archive"},
			responses: []sqltest.Response{
				files(planRow(reportsID)),
				ancestors(reports),
				resolvedAt(yearID, blobfs.RootID, "archive", 1),
				files([]driver.Value{copyID, yearID, "plan.txt", "pending", copyID + "/plan.txt", nil, "text/plain", nil, int64(1), stamp, stamp}),
				files([]driver.Value{copyID, yearID, "plan.txt", "available", copyID + "/plan.txt", int64(12), "text/plain", `"e"`, int64(2), stamp, stamp}),
			},
			want: "cp: /reports/plan.txt -> /archive/plan.txt (id " + copyID + ", 12 bytes, etag \"e\")\n",
		},
		{
			// The source by path, then the destination by id's path.
			name: "cp a path into an id",
			args: []string{"cp", "/reports/plan.txt", "id:" + yearID},
			responses: []sqltest.Response{
				resolvedAt(reportsID, blobfs.RootID, "reports", 1),
				files(planRow(reportsID)),
				ancestors([]driver.Value{yearID, blobfs.RootID, "archive"}),
				files([]driver.Value{copyID, yearID, "plan.txt", "pending", copyID + "/plan.txt", nil, "text/plain", nil, int64(1), stamp, stamp}),
				files([]driver.Value{copyID, yearID, "plan.txt", "available", copyID + "/plan.txt", int64(12), "text/plain", `"e"`, int64(2), stamp, stamp}),
			},
			want: "cp: /reports/plan.txt -> /archive/plan.txt (id " + copyID + ", 12 bytes, etag \"e\")\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := storagetest.NewFake()
			if _, err := fake.Put(context.Background(), planID+"/plan.txt", strings.NewReader("twelve bytes"), storage.PutOptions{}); err != nil {
				t.Fatal(err)
			}
			var out, errOut bytes.Buffer
			rec, run, _ := objectApp(t, fake, strings.NewReader(""), &out, &errOut, tt.responses...)

			code := run(tt.args...)

			if code != process.ExitOK {
				t.Fatalf("code = %d, want %d; stderr = %q", code, process.ExitOK, errOut.String())
			}
			if out.String() != tt.want {
				t.Errorf("stdout = %q, want %q", out.String(), tt.want)
			}
			if n := rec.Pending(); n != 0 {
				t.Errorf("%d scripted responses unconsumed", n)
			}
		})
	}
}

func TestObjects_RmRecursiveByIDReportsTheBranchsPath(t *testing.T) {
	// /reports/2026 by its id, an empty directory: the row and its path,
	// the mark and the bookmark count, and the sweep's one pass.
	var out, errOut bytes.Buffer
	deleting := []driver.Value{yearID, reportsID, "2026", "deleting", int64(2), stamp, stamp}
	rec, run, _ := objectApp(t, storagetest.NewFake(), strings.NewReader(""), &out, &errOut,
		directoryRows(directoryRow(yearID, reportsID, "2026")),
		ancestors([]driver.Value{yearID, reportsID, "2026"}, []driver.Value{reportsID, blobfs.RootID, "reports"}),
		sqltest.Response{}, // the tree lock
		sqltest.Response{Affected: 1},
		sqltest.Response{},
		sqltest.Response{Columns: []string{"n"}, Rows: [][]driver.Value{{int64(0)}}},
		directoryRows(deleting),
		sqltest.Response{Columns: fileColumns},
		directoryRows(),
		sqltest.Response{},
		sqltest.Response{Affected: 1},
	)

	code := run("rm", "--recursive", "id:"+yearID)

	if code != process.ExitOK {
		t.Fatalf("code = %d, want %d; stderr = %q", code, process.ExitOK, errOut.String())
	}
	if want := "rm --recursive: /reports/2026 (0 files, 1 directory)\n"; out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
	if n := rec.Pending(); n != 0 {
		t.Errorf("%d scripted responses unconsumed", n)
	}
}

func TestObjects_AnUnreachableStoreFailsOnceNamingTheStore(t *testing.T) {
	for _, args := range objectVerbs {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			fake := storagetest.NewFake()
			fake.SetDown(true)
			stdin := strings.NewReader("never read")
			var out, errOut bytes.Buffer
			rec, run, ping := objectApp(t, fake, stdin, &out, &errOut)

			code := run(args...)

			if code != process.ExitFailure {
				t.Errorf("code = %d, want %d", code, process.ExitFailure)
			}
			prefix := "blobfs " + args[0] + ": store: "
			if !strings.HasPrefix(errOut.String(), prefix) || strings.Count(errOut.String(), "\n") != 1 {
				t.Errorf("stderr = %q, want one line starting %q", errOut.String(), prefix)
			}
			if !strings.Contains(errOut.String(), storage.ErrUnavailable.Error()) {
				t.Errorf("stderr = %q, want the store's unavailability", errOut.String())
			}
			if out.Len() != 0 || stdin.Len() != len("never read") || fake.Puts() != 0 {
				t.Errorf("stdout = %q, stdin left %d, puts %d: want the body never run", out.String(), stdin.Len(), fake.Puts())
			}
			// Only the statement check reached the database, and the
			// database was shut down with the command.
			if ops := rec.Ops(); slices.ContainsFunc(ops, func(op sqltest.Op) bool { return op != sqltest.OpPrepare }) {
				t.Errorf("ops = %v, want prepares only", ops)
			}
			if err := ping(); err == nil {
				t.Error("the database's pool answers a ping after the run, want it closed")
			}
		})
	}
}

func TestObjects_UsageErrorsBuildNothing(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"put - into a directory id", []string{"put", "-", "id:" + reportsID}, "stdin has no name to store under"},
		{"rm -r", []string{"rm", "-r", "/reports"}, "flag provided but not defined: -r"},
		{"cat with no argument", []string{"cat"}, "accepts 1 argument, got 0"},
		{"put with one argument", []string{"put", "-"}, "accepts 2 arguments, got 1"},
		{"cat of a relative path", []string{"cat", "a.txt"}, "blobfs cat: blobfs: invalid path: \"a.txt\" does not start with /\n"},
		{"put to a relative path", []string{"put", "-", "a.txt"}, "blobfs put: blobfs: invalid path: \"a.txt\" does not start with /\n"},
		{"cp from a relative path", []string{"cp", "a.txt", "/b.txt"}, "blobfs cp: blobfs: invalid path: \"a.txt\" does not start with /\n"},
		{"cp and its synopsis", []string{"cp", "/a.txt"}, "Usage: blobfs cp [flags] <path|id:<uuid>> <path|id:<uuid>>"},
		{"rm of the root", []string{"rm", "/"}, "blobfs rm: blobfs: the root directory; rm removes a file, or with --recursive a directory, below the root\n"},
		{"rm --recursive of the root", []string{"rm", "--recursive", "/"}, "blobfs rm: blobfs: the root directory; rm removes a file, or with --recursive a directory, below the root\n"},
		{"rm --recursive of the root's id", []string{"rm", "--recursive", "id:" + blobfs.RootID}, "the nil UUID is the root's"},
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
			if !strings.Contains(errOut.String(), tt.want) {
				t.Errorf("stderr = %q, want %q", errOut.String(), tt.want)
			}
			if got := built.Log(); len(got) != 0 {
				t.Errorf("nodes built = %q, want none", got)
			}
		})
	}
}

func TestObjects_HelpListsTheObjectCommands(t *testing.T) {
	code, stdout, _ := run(t)

	if code != process.ExitUsage {
		t.Errorf("code = %d, want %d", code, process.ExitUsage)
	}
	for _, want := range []string{
		"  put        Upload a local file, or stdin for -, as the file at a path or into a directory\n",
		"  cat        Write an available file's content to stdout\n",
		"  cp         Copy an available file into a directory or to a new path\n",
		"  rm         Delete a file, or with --recursive a directory and everything beneath it\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout =\n%s\nwant it to contain %q", stdout, want)
		}
	}
}
