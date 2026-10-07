package files_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/JaimeStill/spike-s3-storage/app/domain/files"
)

// The bookmark operations over the scripted driver: add resolves, holds,
// and inserts in one transaction and maps the bookmark table's constraints
// to their sentinels, the single-active rule among them; rm deletes by the
// unit and the file's id; ls reads the unit's page through the read model.

var bookmarkColumns = []string{"file_id", "directory_id", "active", "path", "name", "status", "size", "content_type", "created_at", "updated_at"}

// violation is the constraint error Postgres raises on a bookmark insert,
// as sqlate's Postgres dialect maps it.
func violation(constraint string, class error) sqltest.Response {
	return sqltest.Response{Err: &sqlate.ConstraintError{Constraint: constraint, Class: class, Err: errors.New("violates constraint")}}
}

func TestAddBookmark_ResolvesHoldsAndInsertsInOneTransaction(t *testing.T) {
	s, rec := open(t,
		resolved(dirID, blobfs.RootID, "reports", 1),
		fileRows(fileRow(fileID, dirID, "a.txt", 3)),
		held(fileID),
		sqltest.Response{Affected: 1},
	)

	f, err := s.AddBookmark(context.Background(), files.Ref{Path: "/reports/a.txt"}, unitID, true)

	if err != nil || f.Row.ID != fileID || f.Path != "/reports/a.txt" {
		t.Fatalf("AddBookmark() = %+v, %v", f, err)
	}
	want := []sqltest.Op{sqltest.OpBegin, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpExec, sqltest.OpCommit}
	if got := nonPrepares(rec); !slices.Equal(got, want) {
		t.Errorf("ops = %v, want %v: the hold before the insert, in one transaction", got, want)
	}
	insert := rec.Calls()[len(rec.Calls())-2]
	if !strings.HasPrefix(insert.SQL, "INSERT INTO bookmark") || !slices.Equal(insert.Args, []any{unitID, fileID, true}) {
		t.Errorf("the insert ran %q with %v, want the unit, the file, and active", insert.SQL, insert.Args)
	}
}

func TestAddBookmark_ByIDReadsTheFileAndItsPathBeforeTheHold(t *testing.T) {
	s, rec := open(t,
		fileRows(fileRow(fileID, dirID, "a.txt", 3)),
		ancestors([]driver.Value{dirID, blobfs.RootID, "reports"}),
		held(fileID),
		sqltest.Response{Affected: 1},
	)

	f, err := s.AddBookmark(context.Background(), files.Ref{ID: fileID}, unitID, false)

	if err != nil || f.Row.ID != fileID || f.Path != "/reports/a.txt" {
		t.Fatalf("AddBookmark() = %+v, %v, want the file at /reports/a.txt", f, err)
	}
	want := []sqltest.Op{sqltest.OpBegin, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpExec, sqltest.OpCommit}
	if got := nonPrepares(rec); !slices.Equal(got, want) {
		t.Errorf("ops = %v, want %v: the file, its path, the hold, and the insert in one transaction", got, want)
	}
	if find := rec.Calls()[1]; !strings.Contains(find.SQL, "blobfs_file") || !slices.Contains(find.Args, any(fileID)) {
		t.Errorf("the file's read bound %v, want its id", find.Args)
	}
	insert := rec.Calls()[len(rec.Calls())-2]
	if !strings.HasPrefix(insert.SQL, "INSERT INTO bookmark") || !slices.Equal(insert.Args, []any{unitID, fileID, false}) {
		t.Errorf("the insert ran %q with %v, want the unit, the file, and inactive", insert.SQL, insert.Args)
	}
}

func TestAddBookmark_MapsTheBookmarkTablesConstraints(t *testing.T) {
	tests := []struct {
		name      string
		violation sqltest.Response
		want      error
	}{
		{"a second active bookmark of the unit", violation("uq_bookmark_active", sqlate.ErrUniqueViolation), files.ErrActiveBookmark},
		{"a file the unit bookmarked already", violation("pk_bookmark", sqlate.ErrUniqueViolation), files.ErrAlreadyBookmarked},
		{"a file removed since its lookup", violation("fk_bookmark_file", sqlate.ErrForeignKeyViolation), blobfs.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, rec := open(t,
				resolved(dirID, blobfs.RootID, "reports", 1),
				fileRows(fileRow(fileID, dirID, "a.txt", 3)),
				held(fileID),
				tt.violation,
			)

			_, err := s.AddBookmark(context.Background(), files.Ref{Path: "/reports/a.txt"}, unitID, true)

			if !errors.Is(err, tt.want) {
				t.Fatalf("AddBookmark() = %v, want %v", err, tt.want)
			}
			if _, ok := errors.AsType[*sqlate.ConstraintError](err); !ok {
				t.Errorf("AddBookmark() = %v, want the constraint error reachable", err)
			}
			if ops := nonPrepares(rec); ops[len(ops)-1] != sqltest.OpRollback {
				t.Errorf("ops = %v, want the add rolled back", ops)
			}
		})
	}
}

func TestAddBookmark_TheRootIsRefusedBeforeAnyIO(t *testing.T) {
	for _, ref := range []files.Ref{{Path: "/"}, {ID: blobfs.RootID}} {
		s, rec := open(t)

		_, err := s.AddBookmark(context.Background(), ref, unitID, false)

		if !errors.Is(err, blobfs.ErrRootDirectory) {
			t.Errorf("AddBookmark(%+v) = %v, want ErrRootDirectory", ref, err)
		}
		if len(rec.Calls()) != 0 {
			t.Errorf("AddBookmark(%+v) calls = %v, want none", ref, rec.Ops())
		}
	}
}

func TestRemoveBookmark_DeletesByTheUnitAndTheFile(t *testing.T) {
	s, rec := open(t, resolved(dirID, blobfs.RootID, "reports", 1), fileRows(fileRow(fileID, dirID, "a.txt", 3)), sqltest.Response{Affected: 1})

	f, err := s.RemoveBookmark(context.Background(), files.Ref{Path: "/reports/a.txt"}, unitID)

	if err != nil || f.Row.ID != fileID || f.Path != "/reports/a.txt" {
		t.Fatalf("RemoveBookmark() = %+v, %v", f, err)
	}
	del := rec.Calls()[len(rec.Calls())-1]
	if !strings.HasPrefix(del.SQL, "DELETE FROM bookmark") || !slices.Equal(del.Args, []any{unitID, fileID}) {
		t.Errorf("the delete ran %q with %v, want the unit and the file", del.SQL, del.Args)
	}
}

func TestRemoveBookmark_ByIDReadsTheFileAndItsPath(t *testing.T) {
	s, rec := open(t,
		fileRows(fileRow(fileID, dirID, "a.txt", 3)),
		ancestors([]driver.Value{dirID, blobfs.RootID, "reports"}),
		sqltest.Response{Affected: 1},
	)

	f, err := s.RemoveBookmark(context.Background(), files.Ref{ID: fileID}, unitID)

	if err != nil || f.Row.ID != fileID || f.Path != "/reports/a.txt" {
		t.Fatalf("RemoveBookmark() = %+v, %v, want the file at /reports/a.txt", f, err)
	}
	del := rec.Calls()[len(rec.Calls())-1]
	if !strings.HasPrefix(del.SQL, "DELETE FROM bookmark") || !slices.Equal(del.Args, []any{unitID, fileID}) {
		t.Errorf("the delete ran %q with %v, want the unit and the file", del.SQL, del.Args)
	}
	if n := rec.Pending(); n != 0 {
		t.Errorf("%d scripted responses unconsumed", n)
	}
}

func TestRemoveBookmark_AFileTheUnitHasNotBookmarkedIsErrNoBookmark(t *testing.T) {
	s, _ := open(t, resolved(dirID, blobfs.RootID, "reports", 1), fileRows(fileRow(fileID, dirID, "a.txt", 3)), sqltest.Response{})

	_, err := s.RemoveBookmark(context.Background(), files.Ref{Path: "/reports/a.txt"}, unitID)

	if !errors.Is(err, files.ErrNoBookmark) {
		t.Errorf("RemoveBookmark() = %v, want ErrNoBookmark", err)
	}
}

func TestRemoveBookmark_AMissingFileIsLabelledOnceAsTheRemoval(t *testing.T) {
	s, _ := open(t, resolved(dirID, blobfs.RootID, "reports", 1), fileRows())

	_, err := s.RemoveBookmark(context.Background(), files.Ref{Path: "/reports/a.txt"}, unitID)

	if !errors.Is(err, blobfs.ErrNotFound) {
		t.Fatalf("RemoveBookmark() = %v, want ErrNotFound", err)
	}
	want := "files: remove bookmark of /reports/a.txt for unit " + unitID + ": "
	if msg := err.Error(); !strings.HasPrefix(msg, want) || strings.Count(msg, "files:") != 1 {
		t.Errorf("RemoveBookmark() = %q, want it to start %q and name the package once", msg, want)
	}
}

func TestListBookmarks_ReadsTheUnitsPageInPathOrder(t *testing.T) {
	size := int64(3)
	s, rec := open(t, sqltest.WithTotal(sqltest.Response{Columns: bookmarkColumns, Rows: [][]driver.Value{
		{fileID, dirID, true, "/reports/a.txt", "a.txt", "available", size, "text/plain", stamp, stamp},
		{otherID, blobfs.RootID, false, "/draft.bin", "draft.bin", "pending", nil, "application/octet-stream", stamp, stamp},
	}}, 2))

	p, err := s.ListBookmarks(context.Background(), unitID, files.Listing{Page: 1, Size: 20})

	if err != nil {
		t.Fatalf("ListBookmarks() = %v", err)
	}
	if len(p.Rows) != 2 || p.Total != 2 || p.More || p.Next != "" {
		t.Fatalf("ListBookmarks() = %+v, want two rows, total 2, and no cursor", p)
	}
	if b := p.Rows[0]; b.Path != "/reports/a.txt" || !b.Active || b.Size == nil || *b.Size != 3 || b.Status != blobfs.StatusAvailable {
		t.Errorf("the first row = %+v", b)
	}
	if b := p.Rows[1]; b.Active || b.Size != nil || b.Status != blobfs.StatusPending {
		t.Errorf("the second row = %+v", b)
	}
	calls := rec.Calls()
	if opts := calls[0].TxOptions; !opts.ReadOnly || sql.IsolationLevel(opts.Isolation) != sql.LevelRepeatableRead {
		t.Errorf("transaction options = %+v, want read-only repeatable read", opts)
	}
	read := calls[1]
	if !strings.Contains(read.SQL, "ORDER BY") || !strings.Contains(read.SQL[strings.Index(read.SQL, "ORDER BY"):], "path") {
		t.Errorf("the read ran\n%s\nwant it ordered by path", read.SQL)
	}
	if read.Args[0] != unitID {
		t.Errorf("the read bound %v, want the unit first", read.Args)
	}
}

func TestListBookmarks_AnUnknownSortFieldIsRefusedBeforeTheStatement(t *testing.T) {
	s, rec := open(t)

	_, err := s.ListBookmarks(context.Background(), unitID, files.Listing{Page: 1, Size: 20, Sort: []files.Sort{{Field: "key"}}})

	if err == nil || !strings.Contains(err.Error(), "unknown sort field") {
		t.Errorf("ListBookmarks() = %v, want the unknown sort field", err)
	}
	if slices.Contains(rec.Ops(), sqltest.OpQuery) {
		t.Errorf("ops = %v, want no query", rec.Ops())
	}
}
