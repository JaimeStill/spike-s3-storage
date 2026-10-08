package files_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/standards-lab/blobfs"
	bfdata "github.com/standards-lab/blobfs/data"
	blobfspg "github.com/standards-lab/blobfs/postgres"
	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/JaimeStill/spike-s3-storage/app/domain/files"
)

// The Service's directory operations over the scripted driver, built as the
// composition root builds it: blobfs's Postgres engine, and a dialect that
// renders the returning commands as single statements, as Postgres's does.
// No test here reaches a network.

const (
	dirID   = "00000000-0000-7000-8000-000000000001"
	otherID = "00000000-0000-7000-8000-000000000002"
	fileID  = "00000000-0000-7000-8000-000000000003"
)

// open builds the Service over a scripted pool that answers with responses.
func open(t *testing.T, responses ...sqltest.Response) (*files.Service, *sqltest.Recorder) {
	t.Helper()
	pool, rec := sqltest.Open(t, responses...)
	s, err := files.New(sqlate.Wrap(pool, sqltest.ReturningDialect{}), bfdata.WithEngine(blobfspg.Engine))
	if err != nil {
		t.Fatal(err)
	}
	return s, rec
}

var (
	directoryColumns = []string{"id", "parent_id", "name", "status", "version", "created_at", "updated_at"}
	fileColumns      = []string{"id", "directory_id", "name", "status", "key", "size", "content_type", "etag", "version", "created_at", "updated_at"}
	stamp            = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
)

// directoryRow is one blobfs_directory row, active at version 1; a nil
// parent is the root's.
func directoryRow(id string, parent any, name string) []driver.Value {
	return []driver.Value{id, parent, name, "active", int64(1), stamp, stamp}
}

// fileRow is one available blobfs_file row at version 2.
func fileRow(id, directory, name string, size int64) []driver.Value {
	return []driver.Value{id, directory, name, "available", id + "/" + name, size, "text/plain", `"etag"`, int64(2), stamp, stamp}
}

// resolved is the engine's path resolution reaching the directory at
// depth, the number of segments it matched.
func resolved(id string, parent any, name string, depth int64) sqltest.Response {
	return sqltest.Response{
		Columns: append(slices.Clone(directoryColumns), "depth"),
		Rows:    [][]driver.Value{append(directoryRow(id, parent, name), depth)},
	}
}

// resolvedRoot is the engine's path resolution of the empty path: the root.
func resolvedRoot() sqltest.Response {
	return resolved(blobfs.RootID, nil, "/", 0)
}

// listed is the directory read that follows each half's page, which
// blobfs runs to refuse a listing of a deleting directory: the directory
// with id, active.
func listed(id string) sqltest.Response {
	return directories(directoryRow(id, nil, "listed"))
}

func directories(rows ...[]driver.Value) sqltest.Response {
	return sqltest.Response{Columns: directoryColumns, Rows: rows}
}

func fileRows(rows ...[]driver.Value) sqltest.Response {
	return sqltest.Response{Columns: fileColumns, Rows: rows}
}

func TestStart_PreparesBlobfsTheEnginesAndTheDomainsStatements(t *testing.T) {
	s, rec := open(t)

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v", err)
	}

	prepared := rec.SQL(sqltest.OpPrepare)
	if len(prepared) == 0 {
		t.Fatal("Start prepared nothing")
	}
	if !slices.ContainsFunc(prepared, func(q string) bool { return strings.Contains(q, "pg_advisory_xact_lock") }) {
		t.Errorf("Start did not prepare the engine's tree lock; prepared:\n%s", strings.Join(prepared, "\n---\n"))
	}
	for _, table := range []string{"INSERT INTO directory_owner", "JOIN directory_owner", "INSERT INTO bookmark", "FROM bookmark b"} {
		if !slices.ContainsFunc(prepared, func(q string) bool { return strings.Contains(q, table) }) {
			t.Errorf("Start did not prepare a statement with %q", table)
		}
	}
	if ops := rec.Ops(); slices.ContainsFunc(ops, func(op sqltest.Op) bool { return op != sqltest.OpPrepare }) {
		t.Errorf("ops = %v, want prepares only", ops)
	}
}

func TestStart_AnUnappliedSchemaIsErrVerify(t *testing.T) {
	s, rec := open(t)
	rec.FailPrepare = func(string) error { return errors.New(`relation "blobfs_directory" does not exist`) }

	err := s.Start(context.Background())

	if !errors.Is(err, files.ErrVerify) {
		t.Fatalf("Start() = %v, want ErrVerify", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "the schema is not applied") || !strings.Contains(msg, "does not exist") || strings.Contains(msg, "blobfs schema") {
		t.Errorf("Start() = %q, want the domain's wording, no command, and the cause", err)
	}
}

func TestList_ResolvesAndReadsBothHalvesInOneReadOnlySnapshot(t *testing.T) {
	s, rec := open(t,
		resolved(dirID, blobfs.RootID, "reports", 1),
		sqltest.WithTotal(directories(directoryRow(otherID, dirID, "2026")), 1),
		listed(dirID),
		sqltest.WithTotal(fileRows(fileRow(fileID, dirID, "a.txt", 20)), 1),
		listed(dirID),
	)

	c, err := s.List(context.Background(), files.Ref{Path: "/reports"}, files.Listing{Page: 1, Size: 20})

	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	if len(c.Directories.Rows) != 1 || c.Directories.Rows[0].Name != "2026" ||
		len(c.Files.Rows) != 1 || c.Files.Rows[0].Name != "a.txt" || c.Directories.Total != 1 || c.Files.Total != 1 {
		t.Errorf("List() = %+v", c)
	}
	want := []sqltest.Op{sqltest.OpBegin, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpCommit}
	if got := rec.Ops(); !slices.Equal(got, want) {
		t.Errorf("ops = %v, want %v", got, want)
	}
	if opts := rec.Calls()[0].TxOptions; !opts.ReadOnly || sql.IsolationLevel(opts.Isolation) != sql.LevelRepeatableRead {
		t.Errorf("transaction options = %+v, want read-only repeatable read", opts)
	}
	if args := rec.Calls()[1].Args; len(args) != 2 || args[0] != blobfs.RootID {
		t.Errorf("the resolution bound %v, want the root and the segments", args)
	}
}

func TestList_FileTermsLeaveTheDirectoryHalf(t *testing.T) {
	// A sort by size and a filter on status name fields the directory
	// listing does not declare or does not share; blobfs would refuse them
	// before any SQL, so the listing succeeding shows they were kept off
	// the directory half, and the SQL shows they reached the file half.
	s, rec := open(t,
		resolvedRoot(),
		sqltest.WithTotal(directories(), 0),
		listed(blobfs.RootID),
		sqltest.WithTotal(fileRows(), 0),
		listed(blobfs.RootID),
	)
	l := files.Listing{
		Page: 1, Size: 20,
		Sort:    []files.Sort{{Field: "size", Descending: true}, {Field: "name"}},
		Filters: []files.Filter{{Field: "status", Op: "in", Value: []any{"available"}}, {Field: "name", Op: "like", Value: "a%"}},
	}

	if _, err := s.List(context.Background(), files.Ref{Path: "/"}, l); err != nil {
		t.Fatalf("List() = %v", err)
	}

	queries := rec.SQL(sqltest.OpQuery)
	if len(queries) != 5 {
		t.Fatalf("queries = %d, want 5", len(queries))
	}
	if dirs := queries[1]; strings.Contains(dirs, "size DESC") || !strings.Contains(dirs, "LIKE") {
		t.Errorf("the directory half's statement:\n%s\nwant the name filter and no size sort", dirs)
	}
	if fs := queries[3]; !strings.Contains(fs, "size DESC") || !strings.Contains(fs, "LIKE") {
		t.Errorf("the file half's statement:\n%s\nwant the size sort and the name filter", fs)
	}
}

func TestList_TotalNoneCountsNothing(t *testing.T) {
	s, _ := open(t, resolvedRoot(), directories(), listed(blobfs.RootID), fileRows(), listed(blobfs.RootID))

	c, err := s.List(context.Background(), files.Ref{Path: "/"}, files.Listing{Page: 1, Size: 20, Total: files.TotalNone})

	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	if c.Directories.Total != files.NoTotal || c.Files.Total != files.NoTotal {
		t.Errorf("totals = %d, %d, want NoTotal for both", c.Directories.Total, c.Files.Total)
	}
}

func TestList_ContinuesAHalfFromItsCursorWithoutACount(t *testing.T) {
	// The first page holds one of two files, so it has a next page and a
	// cursor; the second listing continues the file half from it, reading
	// the directory half by number again.
	s, _ := open(t,
		resolvedRoot(),
		sqltest.WithTotal(directories(), 0),
		listed(blobfs.RootID),
		sqltest.WithTotal(fileRows(fileRow(fileID, blobfs.RootID, "a.txt", 1), fileRow(otherID, blobfs.RootID, "b.txt", 2)), 2),
		listed(blobfs.RootID),
	)
	first, err := s.List(context.Background(), files.Ref{Path: "/"}, files.Listing{Page: 1, Size: 1})
	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	if !first.Files.More || first.Files.Next == "" || first.Directories.Next != "" {
		t.Fatalf("first page = %+v, want a file cursor and none for directories", first)
	}

	s, rec := open(t,
		resolvedRoot(),
		sqltest.WithTotal(directories(), 0),
		listed(blobfs.RootID),
		fileRows(fileRow(otherID, blobfs.RootID, "b.txt", 2)),
		listed(blobfs.RootID),
	)
	next, err := s.List(context.Background(), files.Ref{Path: "/"}, files.Listing{Page: 1, Size: 1, After: files.After{Files: first.Files.Next}})

	if err != nil {
		t.Fatalf("List() after the cursor = %v", err)
	}
	if len(next.Files.Rows) != 1 || next.Files.Rows[0].Name != "b.txt" || next.Files.Total != files.NoTotal || next.Files.More {
		t.Errorf("continued file half = %+v, want b.txt, no total, no more", next.Files)
	}
	if next.Directories.Total != 0 {
		t.Errorf("directory half total = %d, want 0, counted by number", next.Directories.Total)
	}
	if fs := rec.SQL(sqltest.OpQuery)[3]; strings.Contains(fs, "sqlate_total") {
		t.Errorf("the continued half's statement counts:\n%s", fs)
	}
}

func TestList_RefusesAPathBeforeAnyIO(t *testing.T) {
	tests := []struct {
		path string
		want error
	}{
		{"reports", blobfs.ErrInvalidPath},
		{"/reports/", blobfs.ErrInvalidPath},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			s, rec := open(t)

			_, err := s.List(context.Background(), files.Ref{Path: tt.path}, files.Listing{Page: 1, Size: 20})

			if !errors.Is(err, tt.want) {
				t.Errorf("List(%q) = %v, want %v", tt.path, err, tt.want)
			}
			if slices.Contains(rec.Ops(), sqltest.OpQuery) {
				t.Errorf("ops = %v, want no query", rec.Ops())
			}
		})
	}
}

func TestList_ByIDReadsTheDirectoryFirst(t *testing.T) {
	s, rec := open(t, directories())

	_, err := s.List(context.Background(), files.Ref{ID: dirID}, files.Listing{Page: 1, Size: 20})

	if !errors.Is(err, blobfs.ErrNotFound) {
		t.Fatalf("List(id) = %v, want ErrNotFound", err)
	}
	if want := []sqltest.Op{sqltest.OpBegin, sqltest.OpQuery, sqltest.OpRollback}; !slices.Equal(rec.Ops(), want) {
		t.Errorf("ops = %v, want %v: no listing after the missing directory", rec.Ops(), want)
	}

	s, _ = open(t,
		directories(directoryRow(dirID, blobfs.RootID, "reports")),
		sqltest.WithTotal(directories(), 0),
		listed(dirID),
		sqltest.WithTotal(fileRows(fileRow(fileID, dirID, "a.txt", 3)), 1),
		listed(dirID),
	)
	c, err := s.List(context.Background(), files.Ref{ID: dirID}, files.Listing{Page: 1, Size: 20})
	if err != nil {
		t.Fatalf("List(id) = %v", err)
	}
	if len(c.Files.Rows) != 1 {
		t.Errorf("List(id) = %+v, want the file", c)
	}
}

func TestStat_ResolvesTheParentAndFindsTheFileByName(t *testing.T) {
	s, rec := open(t, resolved(dirID, blobfs.RootID, "reports", 1), fileRows(fileRow(fileID, dirID, "a.txt", 3)))

	e, err := s.Stat(context.Background(), files.Ref{Path: "/reports/a.txt"})

	if err != nil || e.Kind != files.EntryFile || e.File.ID != fileID || e.Path != "/reports/a.txt" {
		t.Fatalf("Stat() = %+v, %v", e, err)
	}
	if args := rec.Calls()[1].Args; len(args) != 2 || args[0] != dirID || args[1] != "a.txt" {
		t.Errorf("the lookup bound %v, want the parent's id and the name", args)
	}
}

func TestStat_AFileByIDCarriesItsResolvedPath(t *testing.T) {
	// The file's row is read by id, then its directory's path, which the
	// file's name completes.
	s, rec := open(t, fileRows(fileRow(fileID, dirID, "a.txt", 3)), ancestors([]driver.Value{dirID, blobfs.RootID, "reports"}))

	e, err := s.Stat(context.Background(), files.Ref{ID: fileID})

	if err != nil || e.Kind != files.EntryFile || e.File.ID != fileID || e.Path != "/reports/a.txt" {
		t.Errorf("Stat(id) = %+v, %v, want the file at /reports/a.txt", e, err)
	}
	if n := rec.Pending(); n != 0 {
		t.Errorf("%d scripted responses unconsumed", n)
	}
}

func TestStat_TheRootIsItsDirectorysRow(t *testing.T) {
	// The root is no file, which is known before any I/O, so the one
	// query is the directory's resolution.
	s, rec := open(t, resolvedRoot())

	e, err := s.Stat(context.Background(), files.Ref{Path: "/"})

	if err != nil || e.Kind != files.EntryDirectory || e.Directory.ID != blobfs.RootID {
		t.Errorf("Stat(/) = %+v, %v, want the root's row", e, err)
	}
	if n := len(rec.SQL(sqltest.OpQuery)); n != 1 {
		t.Errorf("queries = %d, want the root's resolution alone", n)
	}
}

func TestStat_FileFirstThenDirectory(t *testing.T) {
	reports := []driver.Value{dirID, blobfs.RootID, "reports"}
	tests := []struct {
		name string
		ref  files.Ref
		// responses answer the file's lookup, then the directory's, then,
		// for an id, the read of the directory's path.
		responses []sqltest.Response
	}{
		{"by id", files.Ref{ID: dirID}, []sqltest.Response{fileRows(), directories(directoryRow(dirID, blobfs.RootID, "reports")), ancestors(reports)}},
		{"by path", files.Ref{Path: "/reports"}, []sqltest.Response{resolvedRoot(), fileRows(), resolved(dirID, blobfs.RootID, "reports", 1)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, rec := open(t, tt.responses...)
			e, err := s.Stat(context.Background(), tt.ref)
			if err != nil || e.Kind != files.EntryDirectory || e.Directory.Name != "reports" || e.Path != "/reports" {
				t.Errorf("Stat(directory) = %+v, %v, want the directory at /reports", e, err)
			}
			if n := rec.Pending(); n != 0 {
				t.Errorf("%d scripted responses unconsumed", n)
			}
		})
	}

	s, _ := open(t, fileRows(), directories())
	_, err := s.Stat(context.Background(), files.Ref{ID: otherID})
	if !errors.Is(err, blobfs.ErrNotFound) || !strings.Contains(err.Error(), "no file or directory has it") {
		t.Errorf("Stat(missing) = %v, want ErrNotFound saying neither has it", err)
	}
}

func TestMkdir_CreatesUnderTheResolvedParent(t *testing.T) {
	s, rec := open(t, resolved(dirID, blobfs.RootID, "reports", 1), directories(directoryRow(otherID, dirID, "2026")))

	d, err := s.Mkdir(context.Background(), files.Ref{Path: "/reports/2026"}, "")

	if err != nil || d.ID != otherID {
		t.Fatalf("Mkdir() = %+v, %v", d, err)
	}
	insert := rec.Calls()[1]
	if !strings.HasPrefix(insert.SQL, "INSERT INTO blobfs_directory") || !slices.Contains(insert.Args, any(dirID)) || !slices.Contains(insert.Args, any("2026")) {
		t.Errorf("the create ran %q with %v, want the insert under the parent", insert.SQL, insert.Args)
	}
}

func TestMkdir_RefusesBeforeAnyIO(t *testing.T) {
	tests := []struct {
		path string
		want error
	}{
		{"/", blobfs.ErrRootDirectory},
		{"reports", blobfs.ErrInvalidPath},
		{"/reports/", blobfs.ErrInvalidPath},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			s, rec := open(t)

			_, err := s.Mkdir(context.Background(), files.Ref{Path: tt.path}, "")

			if !errors.Is(err, tt.want) {
				t.Errorf("Mkdir(%q) = %v, want %v", tt.path, err, tt.want)
			}
			if len(rec.Calls()) != 0 {
				t.Errorf("calls = %v, want none", rec.Ops())
			}
		})
	}
}

func TestMkdir_AMissingParentIsNotFound(t *testing.T) {
	s, rec := open(t, resolvedRoot())

	_, err := s.Mkdir(context.Background(), files.Ref{Path: "/missing/child"}, "")

	if !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("Mkdir() = %v, want ErrNotFound", err)
	}
	if n := len(rec.SQL(sqltest.OpQuery)); n != 1 {
		t.Errorf("queries = %d, want the resolution alone", n)
	}
}

func TestRemoveDirectory_TheRootIsRefusedBeforeAnyIO(t *testing.T) {
	for _, ref := range []files.Ref{{Path: "/"}, {ID: blobfs.RootID}} {
		s, rec := open(t)

		_, err := s.RemoveDirectory(context.Background(), ref)

		if !errors.Is(err, blobfs.ErrRootDirectory) {
			t.Errorf("RemoveDirectory(%+v) = %v, want ErrRootDirectory", ref, err)
		}
		if len(rec.Calls()) != 0 {
			t.Errorf("RemoveDirectory(%+v) calls = %v, want none", ref, rec.Ops())
		}
	}
}

func TestMove_StaysUnderOneTopLevelDirectory(t *testing.T) {
	tests := []struct {
		name     string
		src, dst string
		// dst is resolved as an existing directory at depth.
		dstID    string
		dstDepth int64
	}{
		{"into another top-level directory", "/a/y", "/b", otherID, 1},
		{"up to the top level", "/a/y", "/", blobfs.RootID, 0},
		{"a top-level directory below another", "/a", "/b", otherID, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, rec := open(t, resolved(tt.dstID, nil, "b", tt.dstDepth))

			_, err := s.Move(context.Background(), files.Ref{Path: tt.src}, files.Ref{Path: tt.dst})

			if !errors.Is(err, files.ErrMoveAcrossScopes) {
				t.Fatalf("Move(%s, %s) = %v, want ErrMoveAcrossScopes", tt.src, tt.dst, err)
			}
			if n := strings.Count(err.Error(), "files:"); n != 1 {
				t.Errorf("Move(%s, %s) = %q, want the package named once", tt.src, tt.dst, err)
			}
			want := []sqltest.Op{sqltest.OpBegin, sqltest.OpQuery, sqltest.OpRollback}
			if got := rec.Ops(); !slices.Equal(got, want) {
				t.Errorf("ops = %v, want %v: refused after the destination's resolution alone", got, want)
			}
		})
	}
}

func TestMove_TheRootIsRefusedBeforeAnyIO(t *testing.T) {
	s, rec := open(t)

	_, err := s.Move(context.Background(), files.Ref{Path: "/"}, files.Ref{Path: "/elsewhere"})

	if !errors.Is(err, blobfs.ErrRootDirectory) {
		t.Errorf("Move(/) = %v, want ErrRootDirectory", err)
	}
	if len(rec.Calls()) != 0 {
		t.Errorf("calls = %v, want none", rec.Ops())
	}
}

func TestMove_ByIDTheRootIsRefusedBeforeAnyIO(t *testing.T) {
	s, rec := open(t)

	_, err := s.Move(context.Background(), files.Ref{ID: blobfs.RootID}, files.Ref{ID: dirID})

	if !errors.Is(err, blobfs.ErrRootDirectory) {
		t.Errorf("Move(root) = %v, want ErrRootDirectory", err)
	}
	if len(rec.Calls()) != 0 {
		t.Errorf("calls = %v, want none", rec.Ops())
	}
}

func TestMove_ByIDMovesTheFileWithTheIDIntoTheDirectory(t *testing.T) {
	// The file is found first, then both directories' paths, so the
	// one-top-level-directory rule reads paths, and the file moves under
	// its own name, guarded by the version read.
	s, rec := open(t,
		fileRows(fileRow(fileID, dirID, "a.txt", 3)),
		ancestors([]driver.Value{dirID, blobfs.RootID, "reports"}),
		ancestors([]driver.Value{otherID, dirID, "2026"}, []driver.Value{dirID, blobfs.RootID, "reports"}),
		fileRows(fileRow(fileID, otherID, "a.txt", 3)),
	)

	res, err := s.Move(context.Background(), files.Ref{ID: fileID}, files.Ref{ID: otherID})

	if err != nil {
		t.Fatalf("Move() = %v", err)
	}
	want := files.MoveResult{Kind: files.EntryFile, ID: fileID, From: "/reports/a.txt", To: "/reports/2026/a.txt"}
	if res != want {
		t.Errorf("Move() = %+v, want %+v", res, want)
	}
	if n := rec.Pending(); n != 0 {
		t.Errorf("%d scripted responses unconsumed", n)
	}
}

func TestMove_MixesAPathAndAnID(t *testing.T) {
	// Each Ref resolves on its own. A source by path has its name in the
	// path, so the destination resolves first and the source after the
	// scope check; a source by id is read first for its name and its path.
	// A destination by id is the directory the source moves into, keeping
	// its name; a destination by path is read the Unix way.
	reports := []driver.Value{dirID, blobfs.RootID, "reports"}
	tests := []struct {
		name      string
		src, dst  files.Ref
		responses []sqltest.Response
		to        string
	}{
		{"a path into an id", files.Ref{Path: "/reports/a.txt"}, files.Ref{ID: otherID}, []sqltest.Response{
			ancestors([]driver.Value{otherID, dirID, "2026"}, reports),
			resolved(dirID, blobfs.RootID, "reports", 1),
			resolved(dirID, blobfs.RootID, "reports", 1),
			fileRows(fileRow(fileID, dirID, "a.txt", 3)),
			fileRows(fileRow(fileID, otherID, "a.txt", 3)),
		}, "/reports/2026/a.txt"},
		{"an id into an existing directory's path", files.Ref{ID: fileID}, files.Ref{Path: "/reports/2026"}, []sqltest.Response{
			fileRows(fileRow(fileID, dirID, "a.txt", 3)),
			ancestors(reports),
			resolved(otherID, dirID, "2026", 2),
			fileRows(fileRow(fileID, otherID, "a.txt", 3)),
		}, "/reports/2026/a.txt"},
		{"an id to a new path, a rename", files.Ref{ID: fileID}, files.Ref{Path: "/reports/b.txt"}, []sqltest.Response{
			fileRows(fileRow(fileID, dirID, "a.txt", 3)),
			ancestors(reports),
			resolved(dirID, blobfs.RootID, "reports", 1),
			resolved(dirID, blobfs.RootID, "reports", 1),
			fileRows(fileRow(fileID, dirID, "b.txt", 3)),
		}, "/reports/b.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, rec := open(t, tt.responses...)

			res, err := s.Move(context.Background(), tt.src, tt.dst)

			if err != nil {
				t.Fatalf("Move() = %v", err)
			}
			want := files.MoveResult{Kind: files.EntryFile, ID: fileID, From: "/reports/a.txt", To: tt.to}
			if res != want {
				t.Errorf("Move() = %+v, want %+v", res, want)
			}
			if n := rec.Pending(); n != 0 {
				t.Errorf("%d scripted responses unconsumed", n)
			}
		})
	}
}

func TestMove_MixedFormsStayUnderOneTopLevelDirectory(t *testing.T) {
	// The scope rule reads the resolved paths, whichever form named each
	// side, and refuses before anything moves.
	tests := []struct {
		name      string
		src, dst  files.Ref
		responses []sqltest.Response
		want      []sqltest.Op
	}{
		{"a path into an id under another", files.Ref{Path: "/a/y"}, files.Ref{ID: otherID}, []sqltest.Response{
			ancestors([]driver.Value{otherID, blobfs.RootID, "b"}),
		}, []sqltest.Op{sqltest.OpBegin, sqltest.OpQuery, sqltest.OpRollback}},
		{"an id into a path under another", files.Ref{ID: fileID}, files.Ref{Path: "/b"}, []sqltest.Response{
			fileRows(fileRow(fileID, dirID, "y", 3)),
			ancestors([]driver.Value{dirID, blobfs.RootID, "a"}),
			resolved(otherID, blobfs.RootID, "b", 1),
		}, []sqltest.Op{sqltest.OpBegin, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpRollback}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, rec := open(t, tt.responses...)

			_, err := s.Move(context.Background(), tt.src, tt.dst)

			if !errors.Is(err, files.ErrMoveAcrossScopes) || !strings.Contains(err.Error(), "/a/y is under /a and /b/y under /b") {
				t.Fatalf("Move() = %v, want ErrMoveAcrossScopes naming /a/y and /b/y", err)
			}
			if got := rec.Ops(); !slices.Equal(got, tt.want) {
				t.Errorf("ops = %v, want %v: refused before the move", got, tt.want)
			}
		})
	}
}

// ancestors is the engine's read of a directory's chain of parents, as
// Directories.Path reads it: rows, from the directory up, ending below the
// root, which the read appends.
func ancestors(rows ...[]driver.Value) sqltest.Response {
	return sqltest.Response{
		Columns: []string{"id", "parent_id", "name"},
		Rows:    append(rows, []driver.Value{blobfs.RootID, nil, "/"}),
	}
}

func TestFormRules_RefuseBeforeAnyIO(t *testing.T) {
	// Each operation that takes one form alone, because an id cannot name
	// its target, refuses the other with a FormError before it reaches the
	// database or the object store, and so does a put into a directory by
	// id with no name for the file.
	byID := files.Ref{ID: dirID}
	tests := []struct {
		name string
		call func(*files.Service, *files.Storage) error
		want string
	}{
		{"mkdir by id", func(s *files.Service, _ *files.Storage) error {
			_, err := s.Mkdir(context.Background(), byID, "")
			return err
		}, "a directory is created by path, not by id"},
		{"a put into a directory by id with no name", func(_ *files.Service, o *files.Storage) error {
			_, err := o.Put(context.Background(), byID, files.Content{Body: strings.NewReader("x")})
			return err
		}, "a file put into a directory by id takes a name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, rec := open(t)
			o, objRec, fake := openStorage(t)

			err := tt.call(s, o)

			var form *files.FormError
			if !errors.As(err, &form) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want a FormError saying %q", err, tt.want)
			}
			if len(rec.Calls()) != 0 || len(nonPrepares(objRec)) != 0 || fake.Puts() != 0 {
				t.Errorf("calls = %v, %v, puts %d, want none", rec.Ops(), nonPrepares(objRec), fake.Puts())
			}
		})
	}
}
