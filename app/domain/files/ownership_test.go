package files_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/JaimeStill/spike-s3-storage/app/domain/files"
)

// Ownership over the scripted driver: mkdir --unit writes the directory
// and its owner row together, ls --unit checks the owner row of the
// path's top-level directory and lists a unit's own top-level directories
// at the root, and rmdir removes the owner row with the directory.

const unitID = "00000000-0000-7000-8000-0000000000aa"

func TestMkdir_WithAUnitWritesTheDirectoryAndItsOwnerTogether(t *testing.T) {
	s, rec := open(t, resolvedRoot(), directories(directoryRow(dirID, blobfs.RootID, "reports")), sqltest.Response{Affected: 1})

	d, err := s.Mkdir(context.Background(), files.Ref{Path: "/reports"}, unitID)

	if err != nil || d.ID != dirID {
		t.Fatalf("Mkdir() = %+v, %v", d, err)
	}
	want := []sqltest.Op{sqltest.OpBegin, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpExec, sqltest.OpCommit}
	if got := nonPrepares(rec); !slices.Equal(got, want) {
		t.Errorf("ops = %v, want %v: the create and the owner row in one transaction", got, want)
	}
	owner := rec.Calls()[len(rec.Calls())-2]
	if !strings.HasPrefix(owner.SQL, "INSERT INTO directory_owner") || !slices.Equal(owner.Args, []any{dirID, unitID}) {
		t.Errorf("the owner row ran %q with %v, want the directory and the unit", owner.SQL, owner.Args)
	}
}

func TestMkdir_AFailedOwnerRowRollsTheDirectoryBack(t *testing.T) {
	s, rec := open(t, resolvedRoot(), directories(directoryRow(dirID, blobfs.RootID, "reports")), sqltest.Response{Err: errors.New("the owner table is gone")})

	_, err := s.Mkdir(context.Background(), files.Ref{Path: "/reports"}, unitID)

	if err == nil || !strings.Contains(err.Error(), "create the owner row of "+dirID) {
		t.Fatalf("Mkdir() = %v, want the owner row's failure", err)
	}
	if ops := nonPrepares(rec); ops[len(ops)-1] != sqltest.OpRollback {
		t.Errorf("ops = %v, want the create rolled back with the owner row", ops)
	}
}

func TestMkdir_AUnitBindsATopLevelDirectoryOnly(t *testing.T) {
	s, rec := open(t)

	_, err := s.Mkdir(context.Background(), files.Ref{Path: "/reports/2026"}, unitID)

	// The Service enforces the rule the command's Validate runs, labelled
	// with the operation's name.
	if !errors.Is(err, files.ErrUnitDepth) || !strings.HasPrefix(err.Error(), "files: make directory /reports/2026: ") {
		t.Errorf("Mkdir() = %v, want ErrUnitDepth labelled with the make directory", err)
	}
	if len(rec.Calls()) != 0 {
		t.Errorf("calls = %v, want none", rec.Ops())
	}
}

func TestList_AsAUnitChecksTheTopLevelDirectorysOwner(t *testing.T) {
	// /reports/2026 as the unit: /reports is resolved and its owner row
	// read, and only then the full path, and both halves are listed.
	s, rec := open(t,
		resolved(dirID, blobfs.RootID, "reports", 1),
		counted(1),
		resolved(otherID, dirID, "2026", 2),
		sqltest.WithTotal(directories(), 0),
		listed(otherID),
		sqltest.WithTotal(fileRows(fileRow(fileID, otherID, "a.txt", 3)), 1),
		listed(otherID),
	)

	c, err := s.List(context.Background(), files.Ref{Path: "/reports/2026"}, files.Listing{Page: 1, Size: 20, Unit: unitID})

	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	if len(c.Files.Rows) != 1 {
		t.Errorf("List() = %+v, want the file under /reports/2026", c)
	}
	calls := rec.Calls()
	if args := calls[1].Args; len(args) != 2 || fmt.Sprint(args[1]) != "[reports]" {
		t.Errorf("the first resolution bound %v, want the top-level directory alone", args)
	}
	if owner := calls[2]; !strings.Contains(owner.SQL, "FROM directory_owner") || !slices.Equal(owner.Args, []any{dirID, unitID}) {
		t.Errorf("the owner read ran %q with %v, want the top-level directory and the unit", owner.SQL, owner.Args)
	}
	if opts := calls[0].TxOptions; !opts.ReadOnly {
		t.Errorf("transaction options = %+v, want read-only", opts)
	}
}

func TestList_AUnitThatDoesNotOwnTheTopLevelDirectoryIsRefused(t *testing.T) {
	s, rec := open(t, resolved(dirID, blobfs.RootID, "reports", 1), counted(0))

	_, err := s.List(context.Background(), files.Ref{Path: "/reports/2026"}, files.Listing{Page: 1, Size: 20, Unit: unitID})

	if !errors.Is(err, files.ErrNotOwned) || !strings.Contains(err.Error(), "as unit "+unitID) {
		t.Fatalf("List() = %v, want ErrNotOwned naming the unit", err)
	}
	want := []sqltest.Op{sqltest.OpBegin, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpRollback}
	if got := nonPrepares(rec); !slices.Equal(got, want) {
		t.Errorf("ops = %v, want %v: nothing below the top-level directory is resolved", got, want)
	}
}

func TestList_TheRootAsAUnitListsItsOwnTopLevelDirectories(t *testing.T) {
	// The owner read model lists the unit's directories, under the terms
	// that name a directory field; the size filter is the file half's and
	// is left off. The file half is empty and counted.
	s, rec := open(t, sqltest.WithTotal(directories(directoryRow(dirID, blobfs.RootID, "reports")), 1))
	l := files.Listing{Page: 1, Size: 20, Unit: unitID, Filters: []files.Filter{{Field: "name", Op: "like", Value: "r%"}, {Field: "size", Op: "gt", Value: "1"}}}

	c, err := s.List(context.Background(), files.Ref{Path: "/"}, l)

	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	if len(c.Directories.Rows) != 1 || c.Directories.Rows[0].Name != "reports" || c.Directories.Total != 1 || len(c.Files.Rows) != 0 || c.Files.Total != 0 || c.Directories.Next != "" {
		t.Errorf("List() = %+v, want the unit's one directory, no files, and no cursor", c)
	}
	queries := rec.SQL(sqltest.OpQuery)
	if len(queries) != 1 || !strings.Contains(queries[0], "JOIN directory_owner") || !strings.Contains(queries[0], "LIKE") || strings.Contains(queries[0], "size") {
		t.Errorf("queries = %q, want the owner read model with the name filter alone", queries)
	}
	if args := rec.Calls()[1].Args; args[0] != unitID {
		t.Errorf("the read model bound %v, want the unit first", args)
	}

	s, _ = open(t, directories())
	l.Total = files.TotalNone
	c, err = s.List(context.Background(), files.Ref{Path: "/"}, l)
	if err != nil || c.Directories.Total != files.NoTotal || c.Files.Total != files.NoTotal {
		t.Errorf("List() under TotalNone = %+v, %v, want NoTotal for both halves", c, err)
	}
}

func TestList_TheRootAsAUnitTakesNoCursor(t *testing.T) {
	s, rec := open(t)

	_, err := s.List(context.Background(), files.Ref{Path: "/"}, files.Listing{Page: 1, Size: 20, Unit: unitID, After: files.After{Directories: "c"}})

	// The Service enforces the rule the command's Validate runs, labelled
	// with the operation's name.
	if !errors.Is(err, files.ErrNoCursorAtRoot) || !strings.HasPrefix(err.Error(), "files: list / as unit "+unitID+": ") {
		t.Errorf("List() = %v, want ErrNoCursorAtRoot labelled with the list", err)
	}
	if len(rec.Calls()) != 0 {
		t.Errorf("calls = %v, want none", rec.Ops())
	}
}

func TestRemoveDirectory_RemovesTheOwnerRowWithTheDirectory(t *testing.T) {
	s, rec := open(t, resolved(dirID, blobfs.RootID, "reports", 1), sqltest.Response{Affected: 1}, purged())

	d, err := s.RemoveDirectory(context.Background(), files.Ref{Path: "/reports"})

	if err != nil || d.Row.ID != dirID || d.Path != "/reports" {
		t.Fatalf("RemoveDirectory() = %+v, %v", d, err)
	}
	want := []sqltest.Op{sqltest.OpBegin, sqltest.OpQuery, sqltest.OpExec, sqltest.OpExec, sqltest.OpCommit}
	if got := nonPrepares(rec); !slices.Equal(got, want) {
		t.Errorf("ops = %v, want %v", got, want)
	}
	execs := rec.SQL(sqltest.OpExec)
	if !strings.HasPrefix(execs[0], "DELETE FROM directory_owner") || !strings.HasPrefix(execs[1], "DELETE FROM blobfs_directory") {
		t.Errorf("execs = %q, want the owner row and then the directory", execs)
	}
}

func TestRemoveDirectory_ByIDReadsTheRowAndItsPathInTheRemovalsTransaction(t *testing.T) {
	// /reports/2026 by its id: the row, then its path, then the owner row
	// and the directory, all in one transaction, and the path reported.
	s, rec := open(t,
		directories(directoryRow(otherID, dirID, "2026")),
		ancestors([]driver.Value{otherID, dirID, "2026"}, []driver.Value{dirID, blobfs.RootID, "reports"}),
		sqltest.Response{},
		purged(),
	)

	d, err := s.RemoveDirectory(context.Background(), files.Ref{ID: otherID})

	if err != nil || d.Row.ID != otherID || d.Path != "/reports/2026" {
		t.Fatalf("RemoveDirectory() = %+v, %v, want the row at /reports/2026", d, err)
	}
	want := []sqltest.Op{sqltest.OpBegin, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpExec, sqltest.OpExec, sqltest.OpCommit}
	if got := nonPrepares(rec); !slices.Equal(got, want) {
		t.Errorf("ops = %v, want %v", got, want)
	}
	if execs := rec.SQL(sqltest.OpExec); !strings.HasPrefix(execs[1], "DELETE FROM blobfs_directory") {
		t.Errorf("execs = %q, want the directory's delete last", execs)
	}
}

func TestList_AsAUnitByIDChecksTheTopLevelAncestorsOwner(t *testing.T) {
	// /reports/2026 by its id as the unit: the row, its path, then
	// /reports resolved and its owner row read, and both halves listed.
	s, rec := open(t,
		directories(directoryRow(otherID, dirID, "2026")),
		ancestors([]driver.Value{otherID, dirID, "2026"}, []driver.Value{dirID, blobfs.RootID, "reports"}),
		resolved(dirID, blobfs.RootID, "reports", 1),
		counted(1),
		sqltest.WithTotal(directories(), 0),
		listed(otherID),
		sqltest.WithTotal(fileRows(fileRow(fileID, otherID, "a.txt", 3)), 1),
		listed(otherID),
	)

	c, err := s.List(context.Background(), files.Ref{ID: otherID}, files.Listing{Page: 1, Size: 20, Unit: unitID})

	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	if len(c.Files.Rows) != 1 {
		t.Errorf("List() = %+v, want the file", c)
	}
	calls := rec.Calls()
	if args := calls[3].Args; len(args) != 2 || fmt.Sprint(args[1]) != "[reports]" {
		t.Errorf("the resolution bound %v, want the top-level directory alone", args)
	}
	if owner := calls[4]; !strings.Contains(owner.SQL, "FROM directory_owner") || !slices.Equal(owner.Args, []any{dirID, unitID}) {
		t.Errorf("the owner read ran %q with %v, want the top-level directory and the unit", owner.SQL, owner.Args)
	}
	if n := rec.Pending(); n != 0 {
		t.Errorf("%d scripted responses unconsumed", n)
	}
}

func TestList_AsAUnitByIDOfATopLevelDirectoryReadsItsOwnOwnerRow(t *testing.T) {
	// A top-level directory is its own top-level ancestor: no resolution
	// follows its path, and the unit that does not own it is refused.
	s, rec := open(t,
		directories(directoryRow(dirID, blobfs.RootID, "reports")),
		ancestors([]driver.Value{dirID, blobfs.RootID, "reports"}),
		counted(0),
	)

	_, err := s.List(context.Background(), files.Ref{ID: dirID}, files.Listing{Page: 1, Size: 20, Unit: unitID})

	if !errors.Is(err, files.ErrNotOwned) || !strings.Contains(err.Error(), "/reports: ") {
		t.Fatalf("List() = %v, want ErrNotOwned naming /reports", err)
	}
	want := []sqltest.Op{sqltest.OpBegin, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpRollback}
	if got := nonPrepares(rec); !slices.Equal(got, want) {
		t.Errorf("ops = %v, want %v: the row, its path, and the owner row, then nothing listed", got, want)
	}
	if owner := rec.Calls()[len(rec.Calls())-2]; !slices.Equal(owner.Args, []any{dirID, unitID}) {
		t.Errorf("the owner read bound %v, want the directory itself and the unit", owner.Args)
	}
}

func TestList_TheRootsIDAsAUnitListsItsOwnTopLevelDirectories(t *testing.T) {
	s, rec := open(t, sqltest.WithTotal(directories(directoryRow(dirID, blobfs.RootID, "reports")), 1))

	c, err := s.List(context.Background(), files.Ref{ID: blobfs.RootID}, files.Listing{Page: 1, Size: 20, Unit: unitID})

	if err != nil || len(c.Directories.Rows) != 1 {
		t.Fatalf("List() = %+v, %v, want the unit's one directory", c, err)
	}
	if queries := rec.SQL(sqltest.OpQuery); len(queries) != 1 || !strings.Contains(queries[0], "JOIN directory_owner") {
		t.Errorf("queries = %q, want the owner read model alone", queries)
	}
}

func TestRemoveDirectory_ARefusalKeepsTheOwnerRow(t *testing.T) {
	// The directory still has contents: blobfs refuses the delete, and the
	// owner row's removal rolls back with it.
	s, rec := open(t, resolved(dirID, blobfs.RootID, "reports", 1), sqltest.Response{Affected: 1}, sqltest.Response{Err: &sqlate.ConstraintError{
		Constraint: blobfs.ConstraintForeignKeyFileDirectory,
		Class:      sqlate.ErrForeignKeyViolation,
		Err:        errors.New("update or delete violates foreign key constraint"),
	}})

	_, err := s.RemoveDirectory(context.Background(), files.Ref{Path: "/reports"})

	if !errors.Is(err, blobfs.ErrNotEmpty) {
		t.Fatalf("RemoveDirectory() = %v, want ErrNotEmpty", err)
	}
	if ops := nonPrepares(rec); ops[len(ops)-1] != sqltest.OpRollback {
		t.Errorf("ops = %v, want the owner row's removal rolled back", ops)
	}
}
