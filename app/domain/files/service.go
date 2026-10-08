package files

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/standards-lab/blobfs"
	bfdata "github.com/standards-lab/blobfs/data"
	"github.com/standards-lab/sqlate"
)

// Service is the domain's API over the database alone: the directory and
// bookmark operations, composed from blobfs's methods and the domain's
// statements. It holds no object store, so the directory and bookmark
// commands run with the store's configuration unread and the store
// unreachable; the object operations are the [Storage]'s.
type Service struct {
	store *store
}

// New builds the Service over db: the catalog from the query library's
// patterns and blobfs's published namespace, and blobfs's statements, and
// then the domain's, compiled against it for db's dialect. opts reach
// blobfs's store as they are: the composition root fixes blobfs's engine
// with bfdata.WithEngine, and without one the store runs blobfs's baseline.
// No I/O happens here; [Service.Start] checks the statements against the
// database.
func New(db *sqlate.DB, opts ...bfdata.Option) (*Service, error) {
	st, err := newStore(db, opts...)
	if err != nil {
		return nil, fmt.Errorf("files: %w", err)
	}
	return &Service{store: st}, nil
}

// Start prepares every statement of blobfs's, its engine's included, and
// of the domain's, and probes every listing's field contract against the
// database, so the Service takes part in a lifecycle's startup as a
// start-only participant: a schema that is not applied, or no longer
// matches the statements, fails the command at start, before its body
// runs. A failure wraps ErrVerify and the causes. The Service holds
// nothing to shut down.
func (s *Service) Start(ctx context.Context) error {
	if err := s.store.verify(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrVerify, err)
	}
	return nil
}

// The request rules: the refusals an operation makes from its request
// alone. The path rule is the syntax every path a Ref carries must have.
// The form rule is the one form of Ref an operation refuses because
// an id cannot name its target; every other operation takes either form, a
// move and a copy each of their two Refs on its own. The unit rules are
// the requests a unit's ownership cannot serve: a unit bound below the top
// level, and a cursor into the owner listing. The root rule is the root
// refused as the entry an operation removes or bookmarks. The operation runs each rule
// before any I/O, and the command runs the same rule in its Validate, so a
// refusal is a usage error before anything is built.

// checkPath refuses a path that is not absolute, or that has a segment
// blobfs would refuse, with blobfs.ErrInvalidPath. A path is / or, after
// the leading slash, segments that blobfs.ValidateName takes once
// normalized, which is how blobfs validates each segment of a path it
// walks; an empty segment, as in a doubled or a trailing slash, is one it
// refuses. The check reads the text alone, so a command runs it in
// Validate, through parseRef, before anything is built; the operations
// still refuse a relative path themselves, as a backstop for a caller that
// skips it.
func checkPath(path string) error {
	rest, ok := strings.CutPrefix(path, "/")
	if !ok {
		return fmt.Errorf("%w: %q does not start with /", blobfs.ErrInvalidPath, path)
	}
	if rest == "" {
		return nil
	}
	for i, segment := range strings.Split(rest, "/") {
		if err := blobfs.ValidateName(blobfs.NormalizeName(segment)); err != nil {
			return fmt.Errorf("%w: %q: segment %d: %w", blobfs.ErrInvalidPath, path, i+1, err)
		}
	}
	return nil
}

// checkMkdir refuses a directory's create named by id: the directory does
// not exist yet, so it has no id.
func checkMkdir(ref Ref) error {
	if ref.ID != "" {
		return &FormError{Reason: "a directory is created by path, not by id"}
	}
	return nil
}

// checkRoot refuses ref when it names the root, by path or by the root's
// id, with blobfs.ErrRootDirectory, and a path that is not absolute or
// ends with a slash with blobfs.ErrInvalidPath: the root rule of an
// operation that removes or bookmarks an entry, none of which the root can
// be. A command runs it in Validate, so rmdir /, rm /, rm --recursive /,
// and bookmark add / are usage errors before anything is built; the
// operations run it again, before any I/O, as a backstop for a caller that
// skips it.
func checkRoot(ref Ref) error {
	if ref.ID == blobfs.RootID {
		return blobfs.ErrRootDirectory
	}
	if ref.ID != "" {
		return nil
	}
	_, _, err := splitParent(ref.Path)
	return err
}

// checkUnitDepth refuses a directory's create with a unit at a path below
// the top level with ErrUnitDepth: an owner row binds a top-level
// directory only. A path that does not split into a parent and a name
// passes, since checkPath or the create refuses it, and so does an id,
// which checkMkdir refuses.
func checkUnitDepth(ref Ref, unit string) error {
	if unit == "" || ref.ID != "" {
		return nil
	}
	if parent, _, err := splitParent(ref.Path); err == nil && parent != "/" {
		return ErrUnitDepth
	}
	return nil
}

// checkRootCursor refuses a listing of the root under a unit that
// continues from a cursor with ErrNoCursorAtRoot: that listing reads the
// owner read model, which pages by number only.
func checkRootCursor(ref Ref, l Listing) error {
	if atRoot(ref) && l.Unit != "" && l.After != (After{}) {
		return ErrNoCursorAtRoot
	}
	return nil
}

// atRoot reports whether ref names the root, by path or by the root's id.
func atRoot(ref Ref) bool {
	return ref.Path == "/" || ref.ID == blobfs.RootID
}

// label renders ref for an error's label: the path, or the id after kind,
// which names what the id is taken to be.
func label(ref Ref, kind string) string {
	if ref.ID != "" {
		return kind + " " + ref.ID
	}
	return ref.Path
}

// List returns the contents of the directory ref names under l: the
// directories under it and the files in it, each one page of l's size
// with its total when l asks for one, each continued from its own cursor
// in l.After when one is given. The directory is found and both halves
// read in one read-only repeatable-read transaction, so they see one
// snapshot and agree with each other. A directory that does not exist is
// blobfs.ErrNotFound, and a path that does not start with a slash
// blobfs.ErrInvalidPath. A directory named by id is read first, since
// blobfs lists a directory that does not exist as empty.
//
// A unit in l scopes the listing to what the unit owns. Below the root, a
// unit that does not own the listed directory's top-level ancestor is
// refused with ErrNotOwned. By path, the path's top-level directory is
// resolved first and its owner row read, and the rest of the path is
// resolved only once the unit is known to own it. By id, the directory is
// read first, then its path, and the top-level directory at that path's
// first segment is resolved and its owner row read. At the root, the
// listing is the unit's own top-level directories, read through the owner
// read model under the terms that name a directory field, and no files: a
// file in the root has no top-level directory and belongs to no unit. That
// read model pages by number only, so a cursor there is ErrNoCursorAtRoot,
// before any I/O.
func (s *Service) List(ctx context.Context, ref Ref, l Listing) (Contents, error) {
	at := label(ref, "directory")
	if l.Unit != "" {
		at += " as unit " + l.Unit
	}
	if err := checkRootCursor(ref, l); err != nil {
		return Contents{}, fmt.Errorf("files: list %s: %w", at, err)
	}
	c, err := s.store.db.Transact(ctx, func(tx *sqlate.Tx) (Contents, error) {
		switch {
		case l.Unit == "":
			dir, err := s.store.directory(ctx, tx, ref)
			if err != nil {
				return Contents{}, err
			}
			return s.store.contents(ctx, tx, dir.ID, l)
		case atRoot(ref):
			return s.topLevel(ctx, tx, l)
		case ref.ID != "":
			dir, err := s.ownedByID(ctx, tx, ref.ID, l.Unit)
			if err != nil {
				return Contents{}, err
			}
			return s.store.contents(ctx, tx, dir.ID, l)
		}
		dir, err := s.resolveOwned(ctx, tx, ref.Path, l.Unit)
		if err != nil {
			return Contents{}, err
		}
		return s.store.contents(ctx, tx, dir.ID, l)
	}, sqlate.ReadOnly(), sqlate.Isolation(sql.LevelRepeatableRead))
	if err != nil {
		return Contents{}, fmt.Errorf("files: list %s: %w", at, err)
	}
	return c, nil
}

// resolveOwned resolves the directory at path, below the root, through
// sess for the unit: the path's top-level directory first, then its owner
// row, and the rest of the path only once the unit is known to own it, so
// a unit learns nothing of a branch it does not own. A unit that does not
// own the top-level directory is ErrNotOwned.
func (s *Service) resolveOwned(ctx context.Context, sess sqlate.Session, path, unit string) (blobfs.Directory, error) {
	top := topLevelOf(path)
	dir, err := s.store.resolve(ctx, sess, top)
	if err != nil {
		return blobfs.Directory{}, err
	}
	owned, err := s.store.owns(ctx, sess, unit, dir.ID)
	if err != nil {
		return blobfs.Directory{}, err
	}
	if !owned {
		return blobfs.Directory{}, fmt.Errorf("%s: %w", top, ErrNotOwned)
	}
	if top == path {
		return dir, nil
	}
	return s.store.resolve(ctx, sess, path)
}

// ownedByID reads the directory with id, below the root, through sess for
// the unit: the row, then its path, and then the top-level directory at
// the path's first segment and its owner row, so the unit's scope is the
// one the path form checks. The root is listed as the unit's own
// directories and never reaches here. A directory that does not exist is
// blobfs.ErrNotFound, and a unit that does not own its top-level ancestor
// ErrNotOwned.
func (s *Service) ownedByID(ctx context.Context, sess sqlate.Session, id, unit string) (blobfs.Directory, error) {
	dir, err := s.store.blobfs.Directories.Find(ctx, sess, id)
	if err != nil {
		return blobfs.Directory{}, err
	}
	path, err := s.store.blobfs.Directories.Path(ctx, sess, dir.ID)
	if err != nil {
		return blobfs.Directory{}, err
	}
	top := dir
	if topPath := topLevelOf(path); topPath != path {
		if top, err = s.store.resolve(ctx, sess, topPath); err != nil {
			return blobfs.Directory{}, err
		}
	}
	owned, err := s.store.owns(ctx, sess, unit, top.ID)
	if err != nil {
		return blobfs.Directory{}, err
	}
	if !owned {
		return blobfs.Directory{}, fmt.Errorf("%s: %w", topLevelOf(path), ErrNotOwned)
	}
	return dir, nil
}

// topLevel is the listing of the root as the unit l names: the unit's
// top-level directories through the owner read model, one page by number,
// and an empty file half, whose total is 0 when l counts and NoTotal when
// it does not.
func (s *Service) topLevel(ctx context.Context, sess sqlate.Session, l Listing) (Contents, error) {
	dirs, err := s.store.ownedBy(ctx, sess, l.Unit, l)
	if err != nil {
		return Contents{}, err
	}
	files := Page[blobfs.File]{Total: 0}
	if l.Total == TotalNone {
		files.Total = NoTotal
	}
	return Contents{Directories: dirs, Files: files}, nil
}

// Stat returns the row ref names, on the pool: the file at the path or
// with the id, whatever its status, or the directory when no file is
// there, so a directory and a file that share a path report the file.
// Entry's Kind says which, and its Path is where the row is: ref's path,
// or for an id the path computed from the row, read after it. The root,
// which is no file, is its directory's row. A path or an id neither a file
// nor a directory holds is blobfs.ErrNotFound.
func (s *Service) Stat(ctx context.Context, ref Ref) (Entry, error) {
	at := label(ref, "id")
	f, err := s.store.file(ctx, s.store.db, ref)
	switch {
	case err == nil:
		path, err := s.store.filePath(ctx, s.store.db, ref, f)
		if err != nil {
			return Entry{}, fmt.Errorf("files: stat %s: %w", at, err)
		}
		return Entry{Path: path, Kind: EntryFile, File: f}, nil
	case !errors.Is(err, blobfs.ErrNotFound) && !errors.Is(err, blobfs.ErrRootDirectory):
		return Entry{}, fmt.Errorf("files: stat %s: %w", at, err)
	}
	d, err := s.store.directory(ctx, s.store.db, ref)
	switch {
	case errors.Is(err, blobfs.ErrNotFound):
		return Entry{}, fmt.Errorf("files: stat %s: no file or directory has it: %w", at, blobfs.ErrNotFound)
	case err != nil:
		return Entry{}, fmt.Errorf("files: stat %s: %w", at, err)
	}
	path, err := s.store.directoryPath(ctx, s.store.db, ref, d)
	if err != nil {
		return Entry{}, fmt.Errorf("files: stat %s: %w", at, err)
	}
	return Entry{Path: path, Kind: EntryDirectory, Directory: d}, nil
}

// Resolve returns the row of the directory ref names, on the pool. The
// root is the seeded root row. A directory that does not exist is
// blobfs.ErrNotFound, and a path that does not start with a slash, or that
// has an empty segment or a trailing slash, blobfs.ErrInvalidPath.
func (s *Service) Resolve(ctx context.Context, ref Ref) (blobfs.Directory, error) {
	d, err := s.store.directory(ctx, s.store.db, ref)
	if err != nil {
		return blobfs.Directory{}, fmt.Errorf("files: resolve %s: %w", label(ref, "directory"), err)
	}
	return d, nil
}

// Mkdir creates the directory at ref's path under its parent, which must
// exist. A directory is created by path alone (a [FormError] for an id,
// before any I/O). There is no -p: a missing parent is blobfs.ErrNotFound.
// The root is blobfs.ErrRootDirectory before any I/O, and a name an active
// directory holds in the parent is blobfs.ErrNameTaken.
//
// Without a unit the parent is resolved and the directory created on the
// pool. With one, the path must name a top-level directory (ErrUnitDepth
// otherwise, before any I/O), and the directory and the owner row that
// binds it to the unit are written in one transaction, so a directory
// created with a unit never exists without its owner.
func (s *Service) Mkdir(ctx context.Context, ref Ref, unit string) (blobfs.Directory, error) {
	at := label(ref, "id")
	if err := checkMkdir(ref); err != nil {
		return blobfs.Directory{}, fmt.Errorf("files: make directory %s: %w", at, err)
	}
	parent, name, err := splitParent(ref.Path)
	if err != nil {
		return blobfs.Directory{}, fmt.Errorf("files: make directory %s: %w", at, err)
	}
	if err := checkUnitDepth(ref, unit); err != nil {
		return blobfs.Directory{}, fmt.Errorf("files: make directory %s: %w", at, err)
	}
	create := func(sess sqlate.Session) (blobfs.Directory, error) {
		dir, err := s.store.resolve(ctx, sess, parent)
		if err != nil {
			return blobfs.Directory{}, err
		}
		return s.store.blobfs.Directories.Create(ctx, sess, dir.ID, name)
	}
	var made blobfs.Directory
	if unit == "" {
		made, err = create(s.store.db)
	} else {
		made, err = s.store.db.Transact(ctx, func(tx *sqlate.Tx) (blobfs.Directory, error) {
			made, err := create(tx)
			if err != nil {
				return blobfs.Directory{}, err
			}
			return made, s.store.insertOwner(ctx, tx, made.ID, unit)
		})
	}
	if err != nil {
		return blobfs.Directory{}, fmt.Errorf("files: make directory %s: %w", at, err)
	}
	return made, nil
}

// RemoveDirectory removes the empty directory ref names, by path or by
// id, with its owner row when it has one, in one transaction: the
// directory is read, the owner row removed, and the directory removed
// through blobfs, whose refusal rolls the owner row back with it. It
// returns the row with its path, which for a directory named by id is
// computed in the same transaction. A directory that still has
// directories or files under it is blobfs.ErrNotEmpty. The root is
// blobfs.ErrRootDirectory, before any I/O when it is named by path or by
// the root's id.
func (s *Service) RemoveDirectory(ctx context.Context, ref Ref) (Located[blobfs.Directory], error) {
	at := label(ref, "directory")
	if err := checkRoot(ref); err != nil {
		return Located[blobfs.Directory]{}, fmt.Errorf("files: remove directory %s: %w", at, err)
	}
	removed, err := s.store.db.Transact(ctx, func(tx *sqlate.Tx) (Located[blobfs.Directory], error) {
		dir, err := s.store.directory(ctx, tx, ref)
		if err != nil {
			return Located[blobfs.Directory]{}, err
		}
		path, err := s.store.directoryPath(ctx, tx, ref, dir)
		if err != nil {
			return Located[blobfs.Directory]{}, err
		}
		if err := s.store.deleteOwner(ctx, tx, dir.ID); err != nil {
			return Located[blobfs.Directory]{}, err
		}
		return Located[blobfs.Directory]{Path: path, Row: dir}, s.store.blobfs.Directories.Delete(ctx, tx, dir.ID)
	})
	if err != nil {
		return Located[blobfs.Directory]{}, fmt.Errorf("files: remove directory %s: %w", at, err)
	}
	return removed, nil
}

// Move moves the directory or file src names, in one transaction. src and
// dst each take a path or an id, and each is resolved on its own, so a
// path and an id mix.
//
// A source by path is resolved as a directory first and as a file when no
// directory is at the path; a directory and a file may share a name, and
// the directory wins. A source by id is the file with the id, or the
// directory when no file has it, and the path of its parent is computed by
// blobfs's Directories.Path before anything changes. A destination by path
// is read the way Unix reads it: when it names an existing directory the
// source moves into it under its own name, and otherwise it is the new
// path, whose parent must exist and whose last segment is the new name, so
// a move to a new name under the same parent is a rename. A destination by
// id is the directory the source moves into under its own name, its path
// computed by Directories.Path. Either way the result reports both paths.
//
// A directory moves through blobfs's Directories.Move, which takes the
// tree lock and runs the cycle check inside this transaction, so a move
// into the directory itself or one of its descendants is blobfs.ErrCycle.
// A file moves through Files.Move. The directory's contents and the file's
// object follow by id: no key encodes a path, so nothing moves in the
// store.
//
// The move stays under one top-level directory (ErrMoveAcrossScopes
// otherwise), checked on the two resolved paths before anything changes:
// a source by path is checked once the destination resolves and before
// the source is read. The root as the source is blobfs.ErrRootDirectory,
// and a relative destination path blobfs.ErrInvalidPath, before any I/O. A
// source that does not exist, or a destination whose parent does not, is
// blobfs.ErrNotFound; a name already held in the destination by an entry
// of the same kind is blobfs.ErrNameTaken.
func (s *Service) Move(ctx context.Context, src, dst Ref) (MoveResult, error) {
	at := label(src, "id") + " " + label(dst, "directory")
	if src.ID == blobfs.RootID {
		return MoveResult{}, fmt.Errorf("files: move %s: %w", at, blobfs.ErrRootDirectory)
	}
	if src.ID == "" {
		if _, _, err := splitParent(src.Path); err != nil {
			return MoveResult{}, fmt.Errorf("files: move %s: %w", at, err)
		}
	}
	if dst.ID == "" && !strings.HasPrefix(dst.Path, "/") {
		return MoveResult{}, fmt.Errorf("files: move %s: %w: %q does not start with /", at, blobfs.ErrInvalidPath, dst.Path)
	}
	res, err := s.store.db.Transact(ctx, func(tx *sqlate.Tx) (MoveResult, error) {
		return s.move(ctx, tx, src, dst)
	})
	if err != nil {
		return MoveResult{}, fmt.Errorf("files: move %s: %w", at, err)
	}
	return res, nil
}

// moving is the entry a move reads before it moves it: its kind, its id,
// its name, the version that guards the move, and the path it is at.
type moving struct {
	kind    EntryKind
	id      string
	name    string
	version int64
	path    string
}

// move is Move's body inside tx, its errors unlabelled: a source by id is
// read first, for the name it keeps and the path it is at; a source by
// path has both in the path itself. Then the destination is resolved and
// the scope checked, a source by path read, and the entry moved.
func (s *Service) move(ctx context.Context, tx *sqlate.Tx, src, dst Ref) (MoveResult, error) {
	var e moving
	var err error
	if src.ID != "" {
		if e, err = s.movingByID(ctx, tx, src.ID); err != nil {
			return MoveResult{}, err
		}
	} else {
		_, e.name, _ = splitParent(src.Path)
		e.path = src.Path
	}
	parentID, parentPath, name, err := s.store.destination(ctx, tx, dst, e.name)
	if err != nil {
		return MoveResult{}, err
	}
	if err := sameScope(e.path, join(parentPath, name)); err != nil {
		return MoveResult{}, err
	}
	if src.ID == "" {
		if e, err = s.movingByPath(ctx, tx, src.Path); err != nil {
			return MoveResult{}, err
		}
	}
	var moved string
	if e.kind == EntryFile {
		m, err := s.store.blobfs.Files.Move(ctx, tx, e.id, parentID, name, e.version)
		if err != nil {
			return MoveResult{}, err
		}
		moved = m.Name
	} else {
		m, err := s.store.blobfs.Directories.Move(ctx, tx, e.id, parentID, name, e.version)
		if err != nil {
			return MoveResult{}, err
		}
		moved = m.Name
	}
	return MoveResult{Kind: e.kind, ID: e.id, From: e.path, To: join(parentPath, moved)}, nil
}

// movingByPath reads the entry at path through tx: the directory there,
// or the file when no directory is.
func (s *Service) movingByPath(ctx context.Context, tx *sqlate.Tx, path string) (moving, error) {
	dir, err := s.store.resolve(ctx, tx, path)
	switch {
	case err == nil:
		return moving{kind: EntryDirectory, id: dir.ID, name: dir.Name, version: dir.Version, path: path}, nil
	case !errors.Is(err, blobfs.ErrNotFound):
		return moving{}, err
	}
	f, err := s.store.file(ctx, tx, Ref{Path: path})
	if err != nil {
		return moving{}, err
	}
	return moving{kind: EntryFile, id: f.ID, name: f.Name, version: f.Version, path: path}, nil
}

// movingByID reads the entry with id through tx: the file with the id, or
// the directory when no file has it, and the path its parent computes.
func (s *Service) movingByID(ctx context.Context, tx *sqlate.Tx, id string) (moving, error) {
	e := moving{id: id}
	var parentID string
	f, err := s.store.blobfs.Files.Find(ctx, tx, id)
	switch {
	case err == nil:
		e.kind, parentID, e.name, e.version = EntryFile, f.DirectoryID, f.Name, f.Version
	case !errors.Is(err, blobfs.ErrNotFound):
		return moving{}, err
	default:
		d, err := s.store.blobfs.Directories.Find(ctx, tx, id)
		switch {
		case errors.Is(err, blobfs.ErrNotFound):
			return moving{}, fmt.Errorf("no file or directory has it: %w", blobfs.ErrNotFound)
		case err != nil:
			return moving{}, err
		case d.ParentID == nil:
			return moving{}, blobfs.ErrRootDirectory
		}
		e.kind, parentID, e.name, e.version = EntryDirectory, *d.ParentID, d.Name, d.Version
	}
	dir, err := s.store.blobfs.Directories.Path(ctx, tx, parentID)
	if err != nil {
		return moving{}, err
	}
	e.path = join(dir, e.name)
	return e, nil
}

// AddBookmark records that the unit bookmarks the file ref names, by path
// or by id, and returns the file's row with its path, which for a file
// named by id is computed in the add's transaction. With active, the
// bookmark becomes the unit's one active bookmark, and the add is refused
// with ErrActiveBookmark while another bookmark of the unit is active; the
// other one is left as it is. The parent's resolution, the file's lookup,
// the hold of the file, and the insert run in one transaction.
//
// The hold is blobfs's reference-then-delete rule: Files.Hold locks the
// file's row until the transaction ends, so a delete that begins meanwhile
// waits and then sees the bookmark, and a delete that began first makes
// the hold refuse. A file that does not exist, or a parent that does not,
// is blobfs.ErrNotFound, and so is a file removed between its lookup and
// the insert. A pending file can be bookmarked. A deleting file is refused
// with ErrNotAvailable over blobfs's DeletingError, because its delete is
// under way. An error after the file's path is computed names the path,
// whichever form named the file. A file the unit has bookmarked already is
// ErrAlreadyBookmarked, active or not. The root is blobfs.ErrRootDirectory
// before any I/O, by path or by the root's id.
func (s *Service) AddBookmark(ctx context.Context, ref Ref, unit string, active bool) (Located[blobfs.File], error) {
	at := label(ref, "file")
	if err := checkRoot(ref); err != nil {
		return Located[blobfs.File]{}, fmt.Errorf("files: add bookmark of %s for unit %s: %w", at, unit, err)
	}
	f, err := s.store.db.Transact(ctx, func(tx *sqlate.Tx) (Located[blobfs.File], error) {
		f, err := s.store.file(ctx, tx, ref)
		if err != nil {
			return Located[blobfs.File]{}, err
		}
		path, err := s.store.filePath(ctx, tx, ref, f)
		if err != nil {
			return Located[blobfs.File]{}, err
		}
		at = path
		if err := s.store.blobfs.Files.Hold(ctx, tx, f.ID); err != nil {
			if errors.Is(err, blobfs.ErrDeleting) {
				return Located[blobfs.File]{}, fmt.Errorf("%w: %w", ErrNotAvailable, err)
			}
			return Located[blobfs.File]{}, err
		}
		return Located[blobfs.File]{Path: path, Row: f}, s.store.insertBookmark(ctx, tx, unit, f.ID, active)
	})
	if err != nil {
		return Located[blobfs.File]{}, fmt.Errorf("files: add bookmark of %s for unit %s: %w", at, unit, err)
	}
	return f, nil
}

// RemoveBookmark removes the unit's bookmark of the file ref names, by
// path or by id, active or not, and returns the file's row with its path,
// which for a file named by id is computed from the row. The file is read,
// its path computed, and the bookmark deleted on the pool: the delete is
// keyed by the unit and the file's id, so the reads need not share its
// snapshot. A file that does not exist is blobfs.ErrNotFound; a file the
// unit has not bookmarked is ErrNoBookmark. Removing the active bookmark
// leaves the unit with none, which a later add with active may fill. An
// error after the path is computed names the path, whichever form named
// the file.
func (s *Service) RemoveBookmark(ctx context.Context, ref Ref, unit string) (Located[blobfs.File], error) {
	at := label(ref, "file")
	f, err := s.store.file(ctx, s.store.db, ref)
	var path string
	if err == nil {
		path, err = s.store.filePath(ctx, s.store.db, ref, f)
	}
	if err == nil {
		at = path
		err = s.store.deleteBookmark(ctx, s.store.db, unit, f.ID)
	}
	if err != nil {
		return Located[blobfs.File]{}, fmt.Errorf("files: remove bookmark of %s for unit %s: %w", at, unit, err)
	}
	return Located[blobfs.File]{Path: path, Row: f}, nil
}

// ListBookmarks returns one page, by number, of the unit's bookmarks under
// l's page, size, sort terms, and total mode, each with its file's full
// path, in path order unless l sorts otherwise. The read model pages by
// number only, so the page carries no cursor, and l's filters and cursors
// are not read. The page and its total are read in one read-only
// repeatable-read transaction.
func (s *Service) ListBookmarks(ctx context.Context, unit string, l Listing) (Page[Bookmark], error) {
	p, err := s.store.db.Transact(ctx, func(tx *sqlate.Tx) (Page[Bookmark], error) {
		return s.store.bookmarksOf(ctx, tx, unit, Listing{Page: l.Page, Size: l.Size, Sort: l.Sort, Total: l.Total})
	}, sqlate.ReadOnly(), sqlate.Isolation(sql.LevelRepeatableRead))
	if err != nil {
		return Page[Bookmark]{}, fmt.Errorf("files: list bookmarks of unit %s: %w", unit, err)
	}
	return p, nil
}

// splitParent splits the path of an entry into its parent's path and its
// name. The root itself is blobfs.ErrRootDirectory, and a relative path or
// one ending with a slash blobfs.ErrInvalidPath. The parent's segments are
// validated when the parent is resolved and the name when the row is
// written.
func splitParent(path string) (parent, name string, err error) {
	if !strings.HasPrefix(path, "/") {
		return "", "", fmt.Errorf("%w: %q does not start with /", blobfs.ErrInvalidPath, path)
	}
	if path == "/" {
		return "", "", blobfs.ErrRootDirectory
	}
	at := strings.LastIndex(path, "/")
	parent, name = path[:at], path[at+1:]
	if name == "" {
		return "", "", fmt.Errorf("%w: %q ends with a slash", blobfs.ErrInvalidPath, path)
	}
	if parent == "" {
		parent = "/"
	}
	return parent, name, nil
}

// join returns the path of name in the directory at dir.
func join(dir, name string) string {
	return strings.TrimSuffix(dir, "/") + "/" + name
}

// sameScope refuses a move from the path from to the path to unless both
// lie under one top-level directory, where an entry at the top level
// counts as lying under the root, with ErrMoveAcrossScopes.
func sameScope(from, to string) error {
	if scopeOf(from) == scopeOf(to) {
		return nil
	}
	return fmt.Errorf("%s is under %s and %s under %s: %w", from, scopePath(from), to, scopePath(to), ErrMoveAcrossScopes)
}

// topLevelOf returns the path of the top-level directory that contains the
// entry at path, the path itself for a top-level entry.
func topLevelOf(path string) string {
	first, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	return "/" + first
}

// scopeOf returns the normalized name of the top-level directory that
// contains the entry at path, or the empty string when the entry is itself
// at the top level, so that the root contains it.
func scopeOf(path string) string {
	first, _, below := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	if !below {
		return ""
	}
	return blobfs.NormalizeName(first)
}

// scopePath renders scopeOf(path) for a message: the top-level directory's
// path, or / for the root.
func scopePath(path string) string {
	if scope := scopeOf(path); scope != "" {
		return "/" + scope
	}
	return "/"
}
