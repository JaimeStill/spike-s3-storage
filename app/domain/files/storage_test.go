package files_test

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/standards-lab/blobfs"
	bfdata "github.com/standards-lab/blobfs/data"
	blobfspg "github.com/standards-lab/blobfs/postgres"
	"github.com/standards-lab/go-storage"
	"github.com/standards-lab/go-storage/storagetest"
	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/JaimeStill/spike-s3-storage/app/domain/files"
)

// The object operations over the scripted driver and go-storage's
// in-memory Fake behind a started go-storage Store, built as the
// composition root builds them. Each script is the statements blobfs's
// protocols run on the Postgres engine, so a test reads as the steps of
// the protocol it drives. No test here reaches a network.

const earlierID = "00000000-0000-7000-8000-000000000004"

// openStorage builds the Storage over a scripted pool that answers with
// responses and a started Store over a fresh Fake.
func openStorage(t *testing.T, responses ...sqltest.Response) (*files.Storage, *sqltest.Recorder, *storagetest.Fake) {
	t.Helper()
	pool, rec := sqltest.Open(t, responses...)
	s, err := files.New(sqlate.Wrap(pool, sqltest.ReturningDialect{}), bfdata.WithEngine(blobfspg.Engine))
	if err != nil {
		t.Fatal(err)
	}
	st, fake := startedStore(t)
	return files.NewStorage(s, st), rec, fake
}

// startedStore returns a started go-storage Store over a fresh Fake, and
// the Fake.
func startedStore(t *testing.T) (*storage.Store, *storagetest.Fake) {
	t.Helper()
	fake := storagetest.NewFake()
	cfg := storage.Config{Container: "objects"}
	if err := cfg.Finalize("FILES_TEST"); err != nil {
		t.Fatal(err)
	}
	st := storage.New(fake, cfg)
	if err := st.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st, fake
}

// pendingRow is one pending blobfs_file row at version 1, as a write's
// first step leaves it: no size and no etag yet.
func pendingRow(id, directory, name string) []driver.Value {
	return []driver.Value{id, directory, name, "pending", id + "/" + name, nil, "text/plain", nil, int64(1), stamp, stamp}
}

// deletingFileRow is one deleting blobfs_file row, as a mark leaves it.
func deletingFileRow(id, directory, name string) []driver.Value {
	return []driver.Value{id, directory, name, "deleting", id + "/" + name, int64(5), "text/plain", `"etag"`, int64(3), stamp, stamp}
}

// deletingDirectoryRow is one deleting blobfs_directory row.
func deletingDirectoryRow(id string, parent any, name string) []driver.Value {
	return []driver.Value{id, parent, name, "deleting", int64(2), stamp, stamp}
}

// nameTaken is the unique violation Postgres raises for an insert of a
// name a file holds, as sqlate's Postgres dialect maps it.
func nameTaken() sqltest.Response {
	return sqltest.Response{Err: &sqlate.ConstraintError{
		Constraint: blobfs.ConstraintUniqueFileDirectoryName,
		Class:      sqlate.ErrUniqueViolation,
		Err:        errors.New("duplicate key value violates unique constraint"),
	}}
}

// purged is the delete of a deleting row that removed it.
func purged() sqltest.Response { return sqltest.Response{Affected: 1} }

// held is the Postgres engine's hold of the file with id: its locking read
// found the row.
func held(id string) sqltest.Response {
	return sqltest.Response{Columns: []string{"id"}, Rows: [][]driver.Value{{id}}}
}

// counted is the one row of one of the domain's counting statements.
func counted(n int64) sqltest.Response {
	return sqltest.Response{Columns: []string{"n"}, Rows: [][]driver.Value{{n}}}
}

// noOwner is the removal of a directory's owner row that found none.
func noOwner() sqltest.Response { return sqltest.Response{} }

// object returns the content the Fake holds under key.
func object(t *testing.T, fake *storagetest.Fake, key string) string {
	t.Helper()
	blob, err := fake.Get(context.Background(), key, storage.GetOptions{})
	if err != nil {
		t.Fatalf("the store holds no object under %s: %v", key, err)
	}
	defer func() { _ = blob.Body.Close() }()
	b, err := io.ReadAll(blob.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// store puts content under key in the Fake, as an earlier write left it.
func store(t *testing.T, fake *storagetest.Fake, key, content string) {
	t.Helper()
	if _, err := fake.Put(context.Background(), key, strings.NewReader(content), storage.PutOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestPut_CommitsThePendingRowThenPutsThenCompletes(t *testing.T) {
	o, rec, fake := openStorage(t,
		resolvedRoot(),
		fileRows(),
		fileRows(pendingRow(fileID, blobfs.RootID, "a.txt")),
		fileRows(fileRow(fileID, blobfs.RootID, "a.txt", 5)),
	)

	res, err := o.Put(context.Background(), files.Ref{Path: "/a.txt"}, files.Content{Body: strings.NewReader("hello"), ContentType: "text/plain"})

	if err != nil {
		t.Fatalf("Put() = %v", err)
	}
	if res.Path != "/a.txt" || res.Resumed || res.File.Status != blobfs.StatusAvailable || res.File.ID != fileID {
		t.Errorf("Put() = %+v, want the new row available and not resumed", res)
	}
	want := []sqltest.Op{sqltest.OpQuery, sqltest.OpQuery, sqltest.OpBegin, sqltest.OpQuery, sqltest.OpCommit, sqltest.OpQuery}
	if got := nonPrepares(rec); !slices.Equal(got, want) {
		t.Errorf("ops = %v, want the lookup, the create committed, then the completion", got)
	}
	if got := object(t, fake, fileID+"/a.txt"); got != "hello" {
		t.Errorf("the object = %q, want the body", got)
	}
	if opts, _ := fake.LastPut(); opts.ContentType != "text/plain" || opts.Size != 0 {
		t.Errorf("the put's options = %+v, want the declared type and an unknown size", opts)
	}
}

func TestPut_ResumesAPendingRowUnderItsOwnKey(t *testing.T) {
	// The lookup finds a pending row a stopped put left; EnsureFile's
	// Files.Ensure finds it again under its id and inserts nothing, and the
	// object is put under the row's key and the row completed.
	o, rec, fake := openStorage(t,
		resolvedRoot(),
		fileRows(pendingRow(fileID, blobfs.RootID, "a.txt")),
		fileRows(pendingRow(fileID, blobfs.RootID, "a.txt")),
		fileRows(fileRow(fileID, blobfs.RootID, "a.txt", 5)),
	)

	res, err := o.Put(context.Background(), files.Ref{Path: "/a.txt"}, files.Content{Body: strings.NewReader("again"), ContentType: "text/plain"})

	if err != nil {
		t.Fatalf("Put() = %v", err)
	}
	if !res.Resumed || res.File.ID != fileID || res.File.Status != blobfs.StatusAvailable {
		t.Errorf("Put() = %+v, want the pending row resumed and available", res)
	}
	if slices.ContainsFunc(rec.SQL(sqltest.OpQuery), func(q string) bool { return strings.HasPrefix(q, "INSERT") }) {
		t.Errorf("the resumed put inserted a row:\n%s", strings.Join(rec.SQL(sqltest.OpQuery), "\n---\n"))
	}
	if got := object(t, fake, fileID+"/a.txt"); got != "again" {
		t.Errorf("the object = %q, want the body under the pending row's key", got)
	}
	complete := rec.Calls()[len(rec.Calls())-1]
	if !strings.HasPrefix(complete.SQL, "UPDATE blobfs_file") || !slices.Contains(complete.Args, any(int64(1))) {
		t.Errorf("the completion ran %q with %v, want it guarded by the pending version", complete.SQL, complete.Args)
	}
}

func TestPut_RefusesANameAnAvailableFileHolds(t *testing.T) {
	o, rec, fake := openStorage(t,
		resolvedRoot(),
		fileRows(fileRow(fileID, blobfs.RootID, "a.txt", 5)),
		nameTaken(),
	)

	_, err := o.Put(context.Background(), files.Ref{Path: "/a.txt"}, files.Content{Body: strings.NewReader("hello"), ContentType: "text/plain"})

	if !errors.Is(err, blobfs.ErrNameTaken) {
		t.Fatalf("Put() = %v, want ErrNameTaken", err)
	}
	if n := fake.Puts(); n != 0 {
		t.Errorf("puts = %d, want none: the refusal comes before the store", n)
	}
	if ops := nonPrepares(rec); ops[len(ops)-1] != sqltest.OpRollback {
		t.Errorf("ops = %v, want the create rolled back", ops)
	}
}

func TestPut_TheRootIsRefusedBeforeAnyIO(t *testing.T) {
	o, rec, fake := openStorage(t)

	_, err := o.Put(context.Background(), files.Ref{Path: "/"}, files.Content{Body: strings.NewReader("x")})

	if !errors.Is(err, blobfs.ErrRootDirectory) {
		t.Errorf("Put(/) = %v, want ErrRootDirectory", err)
	}
	if len(nonPrepares(rec)) != 0 || fake.Puts() != 0 {
		t.Errorf("ops = %v, puts = %d, want none", rec.Ops(), fake.Puts())
	}
}

func TestOpen_StreamsAnAvailableFilesObject(t *testing.T) {
	o, _, fake := openStorage(t, resolvedRoot(), fileRows(fileRow(fileID, blobfs.RootID, "a.txt", 5)))
	store(t, fake, fileID+"/a.txt", "hello")

	body, f, err := o.Open(context.Background(), files.Ref{Path: "/a.txt"})

	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer func() { _ = body.Close() }()
	var b bytes.Buffer
	if _, err := io.Copy(&b, body); err != nil || b.String() != "hello" || f.Row.ID != fileID || f.Path != "/a.txt" {
		t.Errorf("Open() read %q, %v, file %s at %s; want hello from /a.txt", b.String(), err, f.Row.ID, f.Path)
	}
}

func TestOpen_ByIDReportsTheFilesResolvedPath(t *testing.T) {
	// The file is read by id, checked available, and its directory's path
	// computed before the object is opened.
	o, rec, fake := openStorage(t,
		fileRows(fileRow(fileID, dirID, "a.txt", 5)),
		ancestors([]driver.Value{dirID, blobfs.RootID, "reports"}),
	)
	store(t, fake, fileID+"/a.txt", "hello")

	body, f, err := o.Open(context.Background(), files.Ref{ID: fileID})

	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer func() { _ = body.Close() }()
	if f.Path != "/reports/a.txt" || f.Row.ID != fileID {
		t.Errorf("Open() = %+v, want the file at /reports/a.txt", f)
	}
	if n := rec.Pending(); n != 0 {
		t.Errorf("%d scripted responses unconsumed", n)
	}
}

func TestOpen_APendingFileIsNotAvailable(t *testing.T) {
	o, _, _ := openStorage(t, resolvedRoot(), fileRows(pendingRow(fileID, blobfs.RootID, "a.txt")))

	_, _, err := o.Open(context.Background(), files.Ref{Path: "/a.txt"})

	if !errors.Is(err, files.ErrNotAvailable) || !strings.Contains(err.Error(), "it is pending") {
		t.Errorf("Open() = %v, want ErrNotAvailable naming the status", err)
	}
}

func TestMissingSources_AreLabelledOnceByTheOperation(t *testing.T) {
	// Each read of a file that does not exist is reported under the
	// operation the caller ran, once, and not under the lookup it ran
	// first.
	tests := []struct {
		name      string
		responses []sqltest.Response
		call      func(*files.Storage) error
		want      string
	}{
		{"cat by path", []sqltest.Response{resolvedRoot(), fileRows()}, func(o *files.Storage) error {
			_, _, err := o.Open(context.Background(), files.Ref{Path: "/a.txt"})
			return err
		}, "files: open /a.txt: "},
		{"cat by id", []sqltest.Response{fileRows()}, func(o *files.Storage) error {
			_, _, err := o.Open(context.Background(), files.Ref{ID: fileID})
			return err
		}, "files: open file " + fileID + ": "},
		{"cp by path", []sqltest.Response{resolvedRoot(), fileRows()}, func(o *files.Storage) error {
			_, err := o.Copy(context.Background(), files.Ref{Path: "/a.txt"}, files.Ref{Path: "/b.txt"})
			return err
		}, "files: copy /a.txt into /b.txt: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, _, _ := openStorage(t, tt.responses...)

			err := tt.call(o)

			if !errors.Is(err, blobfs.ErrNotFound) {
				t.Fatalf("error = %v, want ErrNotFound", err)
			}
			if msg := err.Error(); !strings.HasPrefix(msg, tt.want) || strings.Count(msg, "files:") != 1 {
				t.Errorf("error = %q, want it to start %q and name the package once", msg, tt.want)
			}
		})
	}
}

func TestCopy_RefusesANameAlreadyTakenBeforeTheStore(t *testing.T) {
	// The source is read, the destination /b.txt names no directory, so
	// its parent is resolved, and the copy's create meets the name taken:
	// the source's object is never opened and nothing is put.
	o, rec, fake := openStorage(t,
		resolvedRoot(),
		fileRows(fileRow(fileID, blobfs.RootID, "a.txt", 5)),
		resolvedRoot(),
		resolvedRoot(),
		nameTaken(),
	)

	_, err := o.Copy(context.Background(), files.Ref{Path: "/a.txt"}, files.Ref{Path: "/b.txt"})

	if !errors.Is(err, blobfs.ErrNameTaken) {
		t.Fatalf("Copy() = %v, want ErrNameTaken", err)
	}
	if n := fake.Puts(); n != 0 {
		t.Errorf("puts = %d, want none", n)
	}
	if n := rec.Pending(); n != 0 {
		t.Errorf("%d scripted responses unconsumed", n)
	}
}

func TestCopy_RefusesASourceThatIsNotAvailable(t *testing.T) {
	o, rec, _ := openStorage(t, resolvedRoot(), fileRows(pendingRow(fileID, blobfs.RootID, "a.txt")))

	_, err := o.Copy(context.Background(), files.Ref{Path: "/a.txt"}, files.Ref{Path: "/b.txt"})

	if !errors.Is(err, files.ErrNotAvailable) {
		t.Fatalf("Copy() = %v, want ErrNotAvailable", err)
	}
	if got := nonPrepares(rec); len(got) != 2 {
		t.Errorf("ops = %v, want the source's read alone", got)
	}
}

func TestCopy_StreamsTheSourceUnderTheCopysKey(t *testing.T) {
	o, _, fake := openStorage(t,
		resolvedRoot(),
		fileRows(fileRow(fileID, blobfs.RootID, "a.txt", 5)),
		resolved(dirID, blobfs.RootID, "reports", 1),
		fileRows(pendingRow(otherID, dirID, "a.txt")),
		fileRows(fileRow(otherID, dirID, "a.txt", 5)),
	)
	store(t, fake, fileID+"/a.txt", "hello")

	res, err := o.Copy(context.Background(), files.Ref{Path: "/a.txt"}, files.Ref{Path: "/reports"})

	if err != nil {
		t.Fatalf("Copy() = %v", err)
	}
	if res.From != "/a.txt" || res.To != "/reports/a.txt" || res.File.ID != otherID {
		t.Errorf("Copy() = %+v, want /a.txt into /reports under its name", res)
	}
	if got := object(t, fake, otherID+"/a.txt"); got != "hello" {
		t.Errorf("the copy's object = %q, want the source's bytes", got)
	}
	if opts, _ := fake.LastPut(); opts.Size != 5 || opts.ContentType != "text/plain" {
		t.Errorf("the put's options = %+v, want the source's size and type", opts)
	}
}

func TestCopy_MixesAPathAndAnID(t *testing.T) {
	// Each Ref resolves on its own: a source by id has its path computed
	// from its directory, and a destination by path that names an existing
	// directory takes the copy under the source's name; a source by path
	// into a destination by id is the directory with the id, its path
	// computed.
	tests := []struct {
		name      string
		src, dst  files.Ref
		responses []sqltest.Response
		from, to  string
	}{
		{"an id into a path", files.Ref{ID: fileID}, files.Ref{Path: "/archive"}, []sqltest.Response{
			fileRows(fileRow(fileID, dirID, "a.txt", 5)),
			ancestors([]driver.Value{dirID, blobfs.RootID, "reports"}),
			resolved(otherID, blobfs.RootID, "archive", 1),
			fileRows(pendingRow(earlierID, otherID, "a.txt")),
			fileRows(fileRow(earlierID, otherID, "a.txt", 5)),
		}, "/reports/a.txt", "/archive/a.txt"},
		{"a path into an id", files.Ref{Path: "/reports/a.txt"}, files.Ref{ID: otherID}, []sqltest.Response{
			resolved(dirID, blobfs.RootID, "reports", 1),
			fileRows(fileRow(fileID, dirID, "a.txt", 5)),
			ancestors([]driver.Value{otherID, blobfs.RootID, "archive"}),
			fileRows(pendingRow(earlierID, otherID, "a.txt")),
			fileRows(fileRow(earlierID, otherID, "a.txt", 5)),
		}, "/reports/a.txt", "/archive/a.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, rec, fake := openStorage(t, tt.responses...)
			store(t, fake, fileID+"/a.txt", "hello")

			res, err := o.Copy(context.Background(), tt.src, tt.dst)

			if err != nil {
				t.Fatalf("Copy() = %v", err)
			}
			if res.From != tt.from || res.To != tt.to || res.File.ID != earlierID {
				t.Errorf("Copy() = %+v, want %s -> %s", res, tt.from, tt.to)
			}
			if got := object(t, fake, earlierID+"/a.txt"); got != "hello" {
				t.Errorf("the copy's object = %q, want the source's bytes", got)
			}
			if n := rec.Pending(); n != 0 {
				t.Errorf("%d scripted responses unconsumed", n)
			}
		})
	}
}

func TestPut_IntoADirectoryByIDReportsTheResolvedPath(t *testing.T) {
	// The directory's path is computed first, then the write runs as a put
	// by path does: the name's lookup, the pending row, the completion.
	o, rec, _ := openStorage(t,
		ancestors([]driver.Value{dirID, blobfs.RootID, "reports"}),
		fileRows(),
		fileRows(pendingRow(fileID, dirID, "notes.txt")),
		fileRows(fileRow(fileID, dirID, "notes.txt", 5)),
	)

	res, err := o.Put(context.Background(), files.Ref{ID: dirID}, files.Content{Name: "notes.txt", Body: strings.NewReader("hello"), ContentType: "text/plain"})

	if err != nil {
		t.Fatalf("Put() = %v", err)
	}
	if res.Path != "/reports/notes.txt" || res.File.ID != fileID {
		t.Errorf("Put() = %+v, want the file at /reports/notes.txt", res)
	}
	if n := rec.Pending(); n != 0 {
		t.Errorf("%d scripted responses unconsumed", n)
	}
}

func TestRemove_AMissingObjectIsNoRefusal(t *testing.T) {
	// The store holds no object under the file's key; its delete is
	// success, so the row is purged.
	o, rec, _ := openStorage(t,
		resolvedRoot(),
		fileRows(fileRow(fileID, blobfs.RootID, "a.txt", 5)),
		held(fileID),
		counted(0),
		fileRows(deletingFileRow(fileID, blobfs.RootID, "a.txt")),
		purged(),
	)

	f, err := o.Remove(context.Background(), files.Ref{Path: "/a.txt"})

	if err != nil || f.Row.ID != fileID || f.Path != "/a.txt" {
		t.Fatalf("Remove() = %+v, %v", f, err)
	}
	want := []sqltest.Op{sqltest.OpBegin, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpCommit, sqltest.OpExec}
	if got := nonPrepares(rec); !slices.Equal(got, want) {
		t.Errorf("ops = %v, want %v: the lookup, the hold, the bookmark count, and the delete in one transaction, then the purge", got, want)
	}
}

func TestRemove_ByIDComputesThePathInTheDeletesTransaction(t *testing.T) {
	// The file is read by id and its directory's path computed in the
	// delete's first transaction, before the hold, the count, and the
	// delete.
	o, rec, _ := openStorage(t,
		fileRows(fileRow(fileID, dirID, "a.txt", 5)),
		ancestors([]driver.Value{dirID, blobfs.RootID, "reports"}),
		held(fileID),
		counted(0),
		fileRows(deletingFileRow(fileID, dirID, "a.txt")),
		purged(),
	)

	f, err := o.Remove(context.Background(), files.Ref{ID: fileID})

	if err != nil || f.Row.ID != fileID || f.Path != "/reports/a.txt" {
		t.Fatalf("Remove() = %+v, %v, want the file at /reports/a.txt", f, err)
	}
	want := []sqltest.Op{sqltest.OpBegin, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpCommit, sqltest.OpExec}
	if got := nonPrepares(rec); !slices.Equal(got, want) {
		t.Errorf("ops = %v, want %v: the path computed inside the delete's transaction", got, want)
	}
}

func TestRemove_OneUnitsBookmarkIsCountedInTheSingular(t *testing.T) {
	o, _, _ := openStorage(t,
		resolvedRoot(),
		fileRows(fileRow(fileID, blobfs.RootID, "a.txt", 5)),
		held(fileID),
		counted(1),
	)

	_, err := o.Remove(context.Background(), files.Ref{Path: "/a.txt"})

	if !errors.Is(err, files.ErrBookmarked) || err.Error() != "files: remove /a.txt: 1 unit bookmarks the file" {
		t.Errorf("Remove() = %v, want ErrBookmarked naming one unit once", err)
	}
}

func TestRemove_RefusesABookmarkedFileBeforeTouchingAnything(t *testing.T) {
	// The file is held, its bookmarks counted, and the count refuses the
	// delete before Files.Delete runs: the transaction rolls back with the
	// row available, and the store is never reached.
	o, rec, fake := openStorage(t,
		resolvedRoot(),
		fileRows(fileRow(fileID, blobfs.RootID, "a.txt", 5)),
		held(fileID),
		counted(2),
	)
	store(t, fake, fileID+"/a.txt", "hello")
	puts := fake.Puts()

	_, err := o.Remove(context.Background(), files.Ref{Path: "/a.txt"})

	if !errors.Is(err, files.ErrBookmarked) || err.Error() != "files: remove /a.txt: 2 units bookmark the file" {
		t.Fatalf("Remove() = %v, want ErrBookmarked naming the count once", err)
	}
	want := []sqltest.Op{sqltest.OpBegin, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpRollback}
	if got := nonPrepares(rec); !slices.Equal(got, want) {
		t.Errorf("ops = %v, want %v: refused before the delete, and rolled back", got, want)
	}
	if hold := rec.Calls()[len(rec.Calls())-3]; !strings.Contains(hold.SQL, "FOR NO KEY UPDATE") || !slices.Contains(hold.Args, any(fileID)) {
		t.Errorf("the hold ran %q with %v, want the file's row locked", hold.SQL, hold.Args)
	}
	if fake.Puts() != puts {
		t.Errorf("puts = %d, want %d: the store is not reached", fake.Puts(), puts)
	}
	if got := object(t, fake, fileID+"/a.txt"); got != "hello" {
		t.Errorf("the object after the refusal = %q, want it intact", got)
	}
}

func TestRemove_DeletesTheObject(t *testing.T) {
	o, _, fake := openStorage(t,
		resolvedRoot(),
		fileRows(fileRow(fileID, blobfs.RootID, "a.txt", 5)),
		held(fileID),
		counted(0),
		fileRows(deletingFileRow(fileID, blobfs.RootID, "a.txt")),
		purged(),
	)
	store(t, fake, fileID+"/a.txt", "hello")

	if _, err := o.Remove(context.Background(), files.Ref{Path: "/a.txt"}); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if _, err := fake.Stat(context.Background(), fileID+"/a.txt"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("the object after rm: %v, want it gone", err)
	}
}

func TestRemoveTree_MarksTheBranchAndSweepsEveryMarkedBranch(t *testing.T) {
	// /d holds f.txt and the empty directory s; the branch e, which an
	// earlier, interrupted run marked, is empty. The mark runs under the
	// tree lock and is followed by the branch's bookmark count, and the
	// sweep's one pass finishes both branches: d's file, then s, then d,
	// then e, each directory's owner row removed before the directory.
	o, rec, fake := openStorage(t,
		resolved(dirID, blobfs.RootID, "d", 1),
		sqltest.Response{}, // the tree lock
		sqltest.Response{Affected: 2},
		sqltest.Response{Affected: 1},
		counted(0),
		directories(deletingDirectoryRow(dirID, blobfs.RootID, "d"), deletingDirectoryRow(earlierID, blobfs.RootID, "e")),
		fileRows(deletingFileRow(fileID, dirID, "f.txt")),
		purged(),
		directories(deletingDirectoryRow(otherID, dirID, "s")),
		fileRows(),
		directories(),
		noOwner(),
		purged(),
		sqltest.Response{Affected: 1}, // d's owner row
		purged(),
		fileRows(),
		directories(),
		noOwner(),
		purged(),
	)
	store(t, fake, fileID+"/f.txt", "hello")

	res, err := o.RemoveTree(context.Background(), files.Ref{Path: "/d"})

	if err != nil {
		t.Fatalf("RemoveTree() = %v", err)
	}
	if res != (files.TreeRemoval{Path: "/d", Files: 1, Directories: 3}) {
		t.Errorf("RemoveTree() = %+v, want /d with 1 file and 3 directories", res)
	}
	if _, err := fake.Stat(context.Background(), fileID+"/f.txt"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("the file's object after the sweep: %v, want it gone", err)
	}
	if n := rec.Pending(); n != 0 {
		t.Errorf("%d scripted responses unconsumed", n)
	}
	execs := rec.SQL(sqltest.OpExec)
	if !strings.Contains(execs[0], "pg_advisory_xact_lock") {
		t.Errorf("the mark's first statement = %q, want the tree lock", execs[0])
	}
	// After the mark's three statements and the file's purge, each
	// directory's removal is its owner row's and then its own.
	for i, removal := range execs[4:] {
		want := "DELETE FROM blobfs_directory"
		if i%2 == 0 {
			want = "DELETE FROM directory_owner"
		}
		if !strings.HasPrefix(removal, want) {
			t.Errorf("removal statement %d = %q, want %s", i, removal, want)
		}
	}
	if n := len(execs[4:]); n != 6 {
		t.Errorf("removal statements = %d, want an owner row's and a directory's for each of 3 directories", n)
	}
}

func TestRemoveTree_ByIDReadsTheDirectoryAndItsPathBeforeTheMark(t *testing.T) {
	// /d/s by its id, an empty directory: the row and its path on the
	// pool, then the mark and the count, and one pass of the sweep that
	// removes it with its owner row.
	o, rec, _ := openStorage(t,
		directories(directoryRow(otherID, dirID, "s")),
		ancestors([]driver.Value{otherID, dirID, "s"}, []driver.Value{dirID, blobfs.RootID, "d"}),
		sqltest.Response{}, // the tree lock
		sqltest.Response{Affected: 1},
		sqltest.Response{},
		counted(0),
		directories(deletingDirectoryRow(otherID, dirID, "s")),
		fileRows(),
		directories(),
		noOwner(),
		purged(),
	)

	res, err := o.RemoveTree(context.Background(), files.Ref{ID: otherID})

	if err != nil {
		t.Fatalf("RemoveTree() = %v", err)
	}
	if res != (files.TreeRemoval{Path: "/d/s", Directories: 1}) {
		t.Errorf("RemoveTree() = %+v, want /d/s with 1 directory", res)
	}
	if find := rec.Calls()[0]; !strings.Contains(find.SQL, "blobfs_directory") || !slices.Contains(find.Args, any(otherID)) {
		t.Errorf("the directory's read bound %v, want its id", find.Args)
	}
	want := []sqltest.Op{sqltest.OpQuery, sqltest.OpQuery, sqltest.OpBegin}
	if got := nonPrepares(rec); !slices.Equal(got[:3], want) {
		t.Errorf("ops = %v, want them to start %v: the row and its path before the mark", got, want)
	}
	if n := rec.Pending(); n != 0 {
		t.Errorf("%d scripted responses unconsumed", n)
	}
}

func TestRemoveTree_RefusesABranchWithABookmarkedFileBeforeTouchingAnything(t *testing.T) {
	// The mark runs, the count finds two bookmarks in the branch, and the
	// refusal rolls the mark back: no sweep runs and the store is never
	// reached.
	o, rec, fake := openStorage(t,
		resolved(dirID, blobfs.RootID, "d", 1),
		sqltest.Response{},
		sqltest.Response{Affected: 1},
		sqltest.Response{Affected: 1},
		counted(2),
	)
	store(t, fake, fileID+"/f.txt", "hello")
	puts := fake.Puts()

	_, err := o.RemoveTree(context.Background(), files.Ref{Path: "/d"})

	if !errors.Is(err, files.ErrBookmarked) || err.Error() != "files: remove tree /d: 2 bookmarks hold files in the branch" {
		t.Fatalf("RemoveTree() = %v, want ErrBookmarked naming the count once", err)
	}
	want := []sqltest.Op{sqltest.OpQuery, sqltest.OpBegin, sqltest.OpExec, sqltest.OpExec, sqltest.OpExec, sqltest.OpQuery, sqltest.OpRollback}
	if got := nonPrepares(rec); !slices.Equal(got, want) {
		t.Errorf("ops = %v, want %v: the mark rolled back and no sweep", got, want)
	}
	if count := rec.SQL(sqltest.OpQuery)[1]; !strings.Contains(count, "WITH RECURSIVE") || !strings.Contains(count, "bookmark") {
		t.Errorf("the count ran %q, want the branch's bookmarks", count)
	}
	if fake.Puts() != puts {
		t.Errorf("puts = %d, want %d: the store is not reached", fake.Puts(), puts)
	}
	if got := object(t, fake, fileID+"/f.txt"); got != "hello" {
		t.Errorf("the object after the refusal = %q, want it intact", got)
	}
}

func TestRemoveTree_ReportsWhatItRemovedBeforeARefusal(t *testing.T) {
	// The store is down once the branch is marked: the file's object
	// delete is refused, so its row and the directory stay for a rerun,
	// and the error carries the counts.
	o, _, fake := openStorage(t,
		resolved(dirID, blobfs.RootID, "d", 1),
		sqltest.Response{},
		sqltest.Response{Affected: 1},
		sqltest.Response{Affected: 1},
		counted(0),
		directories(deletingDirectoryRow(dirID, blobfs.RootID, "d")),
		fileRows(deletingFileRow(fileID, dirID, "f.txt")),
		directories(),
	)
	fake.SetDown(true)

	_, err := o.RemoveTree(context.Background(), files.Ref{Path: "/d"})

	if !errors.Is(err, storage.ErrUnavailable) || !strings.Contains(err.Error(), "removed 0 files and 0 directories, then") {
		t.Errorf("RemoveTree() = %v, want the store's refusal after the counts", err)
	}
}

func TestRemoveTree_TheRootIsRefusedBeforeAnyIO(t *testing.T) {
	for _, ref := range []files.Ref{{Path: "/"}, {ID: blobfs.RootID}} {
		o, rec, _ := openStorage(t)

		_, err := o.RemoveTree(context.Background(), ref)

		if !errors.Is(err, blobfs.ErrRootDirectory) {
			t.Errorf("RemoveTree(%+v) = %v, want ErrRootDirectory", ref, err)
		}
		if got := nonPrepares(rec); len(got) != 0 {
			t.Errorf("RemoveTree(%+v) ops = %v, want none", ref, got)
		}
	}
}

// nonPrepares returns the recorded ops other than the prepares.
func nonPrepares(rec *sqltest.Recorder) []sqltest.Op {
	var ops []sqltest.Op
	for _, op := range rec.Ops() {
		if op != sqltest.OpPrepare {
			ops = append(ops, op)
		}
	}
	return ops
}
