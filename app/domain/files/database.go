package files

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"strings"

	"github.com/standards-lab/blobfs"
	bfdata "github.com/standards-lab/blobfs/data"
	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"
)

//go:embed statements/*.sql
var statementFiles embed.FS

// store is the domain's data access over the database alone: blobfs's
// persistence and the domain's own statements, over the ownership and
// bookmark tables, both compiled against the domain's pattern catalog, and
// the session they run on. It holds no object store, so the [Service]
// runs with the store's configuration unread and the store unreachable;
// the [Storage] pairs it with the object store. Only this file imports the
// query library: it lowers a Listing to the query library's directives,
// wraps each of the domain's statements in a typed method, and holds the
// lookups the operations share, so the operations compose blobfs's
// methods and the domain's without naming it.
type store struct {
	db     *sqlate.DB
	blobfs *bfdata.Store
	stmts  *query.Statements

	createOwner      query.Statement
	removeOwner      query.Statement
	directoryOwned   query.Rows[count]
	ownedDirectories query.Projection[blobfs.Directory]
	createBookmark   query.Statement
	removeBookmark   query.Statement
	bookmarks        query.Projection[Bookmark]
	fileBookmarks    query.Rows[count]
	branchBookmarks  query.Rows[count]
}

// count is the one row of the domain's counting statements.
type count struct {
	N int64 `json:"n"`
}

// newStore builds the catalog from the query library's patterns and
// blobfs's published namespace and compiles blobfs's statements, and then
// the domain's, against it for db's dialect. opts reach blobfs's store as
// they are. No I/O happens here; verify checks the statements against the
// database.
func newStore(db *sqlate.DB, opts ...bfdata.Option) (*store, error) {
	catalog, err := query.NewCatalog(query.Patterns(), bfdata.Patterns())
	if err != nil {
		return nil, err
	}
	fs, err := bfdata.New(catalog, db.Dialect(), opts...)
	if err != nil {
		return nil, err
	}
	stmts, err := catalog.Compile(statementFiles, "statements", db.Dialect())
	if err != nil {
		return nil, err
	}
	return &store{
		db:               db,
		blobfs:           fs,
		stmts:            stmts,
		createOwner:      stmts.Statement("create_directory_owner"),
		removeOwner:      stmts.Statement("remove_directory_owner"),
		directoryOwned:   stmts.Statement("directory_owned").Scan(query.Scanner[count]()),
		ownedDirectories: stmts.Statement("owned_directories").Project(query.Scanner[blobfs.Directory]()),
		createBookmark:   stmts.Statement("create_bookmark"),
		removeBookmark:   stmts.Statement("remove_bookmark"),
		bookmarks:        stmts.Statement("bookmarks").Project(query.Scanner[Bookmark]()),
		fileBookmarks:    stmts.Statement("file_bookmark_count").Scan(query.Scanner[count]()),
		branchBookmarks:  stmts.Statement("branch_bookmark_count").Scan(query.Scanner[count]()),
	}, nil
}

// verify prepares every statement of blobfs's, its engine's included, and
// of the domain's, and probes every listing's field contract against the
// database.
func (s *store) verify(ctx context.Context) error {
	return query.Verify(ctx, s.db, s.blobfs, s.stmts, s.ownedDirectories, s.bookmarks)
}

// directoryFields are the fields a sort term or a filter may name to apply
// to the directory half of ls as well as to the file half. The file half
// takes every term and refuses one it does not declare; a term naming a
// field only files have sorts or filters the files and leaves the
// directory half as it is. Both listings declare status, but a directory's
// status (active, deleting) is not a file's (pending, available,
// deleting), so a status term applies to the files alone. parent_id is
// left out: every row of one directory listing shares it.
var directoryFields = map[string]bool{
	"id": true, "name": true, "version": true, "created_at": true, "updated_at": true,
}

// directives lowers a Listing to the query library's directives for one
// half of ls: the filters and sort terms, all of them for the file half
// (a nil allowed set) and those naming a directory field for the directory
// half, and the total mode. A half continued from a cursor counts nothing,
// so its total is NoTotal whatever the Listing asks.
func directives(l Listing, allowed map[string]bool, continued bool) query.Directives {
	d := query.Directives{}
	for _, t := range l.Sort {
		if allowed == nil || allowed[t.Field] {
			d.Sort = append(d.Sort, query.Sort{Field: t.Field, Descending: t.Descending})
		}
	}
	for _, f := range l.Filters {
		if allowed == nil || allowed[f.Field] {
			d.Filters = append(d.Filters, query.Filter{Field: f.Field, Op: query.Op(f.Op), Value: f.Value})
		}
	}
	if l.Total == TotalNone || continued {
		d.Total = query.TotalNone
	}
	return d
}

// half reads one half of a listing from list, anchored on the directory
// with id: by page number when after is empty, and past the cursor after
// otherwise, each under the half's directives.
func half[T any](ctx context.Context, sess sqlate.Session, list bfdata.Listing[T], id string, l Listing, allowed map[string]bool, after string) (Page[T], error) {
	req := directives(l, allowed, after != "")
	var c query.Collection[T]
	var err error
	if after == "" {
		c, err = list.List(ctx, sess, id, req, query.Page{Number: l.Page, Size: l.Size})
	} else {
		c, err = list.Continue(ctx, sess, id, req, query.Cursor(after), l.Size)
	}
	if err != nil {
		return Page[T]{}, err
	}
	return Page[T]{Rows: c.Items, Total: totalOf(c.Total), More: c.More, Next: string(c.Next)}, nil
}

// insertOwner writes the ownership row that binds the directory with
// directoryID to the unit with unitID, inside tx.
func (s *store) insertOwner(ctx context.Context, tx *sqlate.Tx, directoryID, unitID string) error {
	if _, err := s.createOwner.Exec(ctx, tx, query.Args{"directory_id": directoryID, "unit_id": unitID}); err != nil {
		return fmt.Errorf("create the owner row of %s: %w", directoryID, err)
	}
	return nil
}

// deleteOwner removes the ownership row of the directory with directoryID
// inside tx, if the directory has one; none is not an error.
func (s *store) deleteOwner(ctx context.Context, tx *sqlate.Tx, directoryID string) error {
	if _, err := s.removeOwner.Exec(ctx, tx, query.Args{"directory_id": directoryID}); err != nil {
		return fmt.Errorf("remove the owner row of %s: %w", directoryID, err)
	}
	return nil
}

// owns reports whether the unit with unitID owns the directory with
// directoryID, through sess: one read of the owner table. A directory with
// no owner row, or one another unit owns, is not owned.
func (s *store) owns(ctx context.Context, sess sqlate.Session, unitID, directoryID string) (bool, error) {
	c, err := s.directoryOwned.One(ctx, sess, query.Args{"directory_id": directoryID, "unit_id": unitID})
	if err != nil {
		return false, fmt.Errorf("the owner of %s: %w", directoryID, err)
	}
	return c.N > 0, nil
}

// ownedBy reads one page, by number, of the directories the unit with
// unitID owns, through sess, under the terms of l that name a directory
// field, as the directory half of ls takes them. The read model pages by
// number only, so the page carries no cursor.
func (s *store) ownedBy(ctx context.Context, sess sqlate.Session, unitID string, l Listing) (Page[blobfs.Directory], error) {
	c, err := s.ownedDirectories.List(ctx, sess, directives(l, directoryFields, false), query.Page{Number: l.Page, Size: l.Size}, query.With("unit_id", unitID))
	if err != nil {
		return Page[blobfs.Directory]{}, fmt.Errorf("the directories unit %s owns: %w", unitID, err)
	}
	return Page[blobfs.Directory]{Rows: c.Items, Total: totalOf(c.Total), More: c.More}, nil
}

// insertBookmark writes the unit with unitID's bookmark of the file with
// fileID through sess, active or not. A violated constraint of the
// bookmark table reaches the caller as its sentinel.
func (s *store) insertBookmark(ctx context.Context, sess sqlate.Session, unitID, fileID string, active bool) error {
	_, err := s.createBookmark.Exec(ctx, sess, query.Args{"unit_id": unitID, "file_id": fileID, "active": active})
	if err != nil {
		return classifyBookmark(err)
	}
	return nil
}

// deleteBookmark removes the unit with unitID's bookmark of the file with
// fileID through sess. No row affected is ErrNoBookmark.
func (s *store) deleteBookmark(ctx context.Context, sess sqlate.Session, unitID, fileID string) error {
	n, err := s.removeBookmark.Exec(ctx, sess, query.Args{"unit_id": unitID, "file_id": fileID})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNoBookmark
	}
	return nil
}

// bookmarksOf reads one page, by number, of the unit with unitID's
// bookmarks, through sess, each with its file's full path, sorted by l's
// terms and by path when l names none; the projection appends file_id as
// the tie-breaker. The read model pages by number only, so the page
// carries no cursor.
func (s *store) bookmarksOf(ctx context.Context, sess sqlate.Session, unitID string, l Listing) (Page[Bookmark], error) {
	d := directives(l, nil, false)
	if len(d.Sort) == 0 {
		d.Sort = []query.Sort{{Field: "path"}}
	}
	c, err := s.bookmarks.List(ctx, sess, d, query.Page{Number: l.Page, Size: l.Size}, query.With("unit_id", unitID))
	if err != nil {
		return Page[Bookmark]{}, err
	}
	return Page[Bookmark]{Rows: c.Items, Total: totalOf(c.Total), More: c.More}, nil
}

// bookmarksOfFile returns how many units bookmark the file with fileID,
// through sess.
func (s *store) bookmarksOfFile(ctx context.Context, sess sqlate.Session, fileID string) (int64, error) {
	c, err := s.fileBookmarks.One(ctx, sess, query.Args{"file_id": fileID})
	if err != nil {
		return 0, fmt.Errorf("the bookmarks of file %s: %w", fileID, err)
	}
	return c.N, nil
}

// bookmarksInBranch returns how many bookmarks hold files in the branch
// whose root is the directory with id, through sess.
func (s *store) bookmarksInBranch(ctx context.Context, sess sqlate.Session, id string) (int64, error) {
	c, err := s.branchBookmarks.One(ctx, sess, query.Args{"id": id})
	if err != nil {
		return 0, fmt.Errorf("the bookmarks in branch %s: %w", id, err)
	}
	return c.N, nil
}

// resolve returns the directory at the absolute path through sess: blobfs
// resolves the path below the root, so / is the root itself. A path that
// does not start with a slash is blobfs.ErrInvalidPath before any I/O.
func (s *store) resolve(ctx context.Context, sess sqlate.Session, path string) (blobfs.Directory, error) {
	rest, ok := strings.CutPrefix(path, "/")
	if !ok {
		return blobfs.Directory{}, fmt.Errorf("%w: %q does not start with /", blobfs.ErrInvalidPath, path)
	}
	return s.blobfs.Directories.FindByPath(ctx, sess, blobfs.RootID, rest)
}

// directory returns the row of the directory ref names through sess: the
// row with its id, or the directory at its path, the root's seeded row for
// /. One that does not exist is blobfs.ErrNotFound.
func (s *store) directory(ctx context.Context, sess sqlate.Session, ref Ref) (blobfs.Directory, error) {
	if ref.ID != "" {
		return s.blobfs.Directories.Find(ctx, sess, ref.ID)
	}
	return s.resolve(ctx, sess, ref.Path)
}

// file returns the row of the file ref names through sess, whatever its
// status: the row with its id, or, for a path, the last segment looked up
// among the files of the resolved parent. A file that does not exist, or a
// parent that does not, is blobfs.ErrNotFound; the root, which is no file,
// is blobfs.ErrRootDirectory before any I/O.
func (s *store) file(ctx context.Context, sess sqlate.Session, ref Ref) (blobfs.File, error) {
	if ref.ID != "" {
		return s.blobfs.Files.Find(ctx, sess, ref.ID)
	}
	parent, name, err := splitParent(ref.Path)
	if err != nil {
		return blobfs.File{}, err
	}
	dir, err := s.resolve(ctx, sess, parent)
	if err != nil {
		return blobfs.File{}, err
	}
	return s.blobfs.Files.FindByName(ctx, sess, dir.ID, name)
}

// directoryPath returns the path of the directory ref names, whose row is
// dir, through sess: ref's own path, or for a Ref by id the path blobfs
// computes from the row.
func (s *store) directoryPath(ctx context.Context, sess sqlate.Session, ref Ref, dir blobfs.Directory) (string, error) {
	if ref.ID == "" {
		return ref.Path, nil
	}
	return s.blobfs.Directories.Path(ctx, sess, dir.ID)
}

// filePath returns the path of the file ref names, whose row is f, through
// sess: ref's own path, or for a Ref by id its directory's path, which
// blobfs computes, and its name.
func (s *store) filePath(ctx context.Context, sess sqlate.Session, ref Ref, f blobfs.File) (string, error) {
	if ref.ID == "" {
		return ref.Path, nil
	}
	dir, err := s.blobfs.Directories.Path(ctx, sess, f.DirectoryID)
	if err != nil {
		return "", err
	}
	return join(dir, f.Name), nil
}

// destination reads the destination of a move or a copy through sess:
// the id of the directory the source goes into, that directory's path, and
// the name the source takes there. A dst by id is that directory, its path
// computed by blobfs, and the name the source's own. A dst by path names
// an existing directory, in which case the name is the source's own, or a
// new path, in which case the parent must exist and the last segment is
// the name.
func (s *store) destination(ctx context.Context, sess sqlate.Session, dst Ref, srcName string) (string, string, string, error) {
	if dst.ID != "" {
		path, err := s.blobfs.Directories.Path(ctx, sess, dst.ID)
		if err != nil {
			return "", "", "", err
		}
		return dst.ID, path, srcName, nil
	}
	dir, err := s.resolve(ctx, sess, dst.Path)
	switch {
	case err == nil:
		return dir.ID, dst.Path, srcName, nil
	case !errors.Is(err, blobfs.ErrNotFound):
		return "", "", "", err
	}
	parentPath, name, err := splitParent(dst.Path)
	if err != nil {
		return "", "", "", err
	}
	parent, err := s.resolve(ctx, sess, parentPath)
	if err != nil {
		return "", "", "", err
	}
	return parent.ID, parentPath, name, nil
}

// contents reads the two halves of the directory with id through sess:
// the directory half under the terms naming a directory field, the file
// half under every term, each from its own cursor when l carries one.
func (s *store) contents(ctx context.Context, sess sqlate.Session, id string, l Listing) (Contents, error) {
	dirs, err := half(ctx, sess, s.blobfs.Directories, id, l, directoryFields, l.After.Directories)
	if err != nil {
		return Contents{}, err
	}
	files, err := half(ctx, sess, s.blobfs.Files, id, l, nil, l.After.Files)
	if err != nil {
		return Contents{}, err
	}
	return Contents{Directories: dirs, Files: files}, nil
}

// The names of the constraints and the unique index the app's bookmark
// migration declares, which bookmarkSentinels maps to the sentinels. They
// carry no blobfs_ prefix, so a violation of one is told from one of
// blobfs's.
const (
	// constraintPrimaryKeyBookmark is the primary key on bookmark
	// (unit_id, file_id). A violation on an add is ErrAlreadyBookmarked.
	constraintPrimaryKeyBookmark = "pk_bookmark"

	// constraintUniqueBookmarkActive is the partial unique index on
	// bookmark (unit_id) WHERE active. A violation on an add is
	// ErrActiveBookmark.
	constraintUniqueBookmarkActive = "uq_bookmark_active"

	// constraintForeignKeyBookmarkFile is the foreign key from
	// bookmark.file_id to blobfs_file.id. A violation on an add is
	// blobfs.ErrNotFound: the file was removed between its resolution and
	// the insert.
	constraintForeignKeyBookmarkFile = "fk_bookmark_file"
)

// bookmarkSentinels maps the constraints a bookmark insert can violate to
// the sentinel each one means there, under the violation class the
// constraint reports: the primary key is a bookmark the unit holds
// already, the partial unique index another active bookmark of the unit,
// and the foreign key a file that no longer exists. The names are the
// domain's own, so blobfs's classification never sees them.
var bookmarkSentinels = map[string]struct{ class, sentinel error }{
	constraintPrimaryKeyBookmark:     {sqlate.ErrUniqueViolation, ErrAlreadyBookmarked},
	constraintUniqueBookmarkActive:   {sqlate.ErrUniqueViolation, ErrActiveBookmark},
	constraintForeignKeyBookmarkFile: {sqlate.ErrForeignKeyViolation, blobfs.ErrNotFound},
}

// classifyBookmark maps a violation of a constraint bookmarkSentinels
// lists to its sentinel, as a blobfs.ViolationError, blobfs's own wrapper,
// whose message names the sentinel and the constraint and which keeps the
// sqlate.ConstraintError reachable. Any other error is returned as it
// came.
func classifyBookmark(err error) error {
	var ce *sqlate.ConstraintError
	if !errors.As(err, &ce) {
		return err
	}
	m, ok := bookmarkSentinels[ce.Constraint]
	if !ok || !errors.Is(ce.Class, m.class) {
		return err
	}
	return &blobfs.ViolationError{Sentinel: m.sentinel, Constraint: ce.Constraint, Err: err}
}

// totalOf translates the query library's total to the domain's.
func totalOf(total int) int {
	if total == query.NoTotal {
		return NoTotal
	}
	return total
}
