package files

import (
	"errors"
	"fmt"

	"github.com/JaimeStill/spike-s3-storage/app/output"
)

// The sentinels the domain adds to blobfs's. A refusal matches one of
// these or one of blobfs's own, so a caller classifies it with errors.Is.
var (
	// ErrVerify reports a database blobfs's statements, or the domain's,
	// do not prepare against: the schema is not applied, or no longer
	// matches the statements. It is worded in the domain's terms and names
	// no command; the root help's schema group is where a reader finds the
	// fix. The failure wraps it with the causes. It carries no "files:"
	// prefix because it is reported as the files node's start error, which
	// the lifecycle labels with the node's name.
	ErrVerify = errors.New("the database does not satisfy the statements: the schema is not applied or does not match them")

	// ErrMoveAcrossScopes reports a move whose source and destination lie
	// under different top-level directories, or one of them at the top
	// level and the other below it. A top-level directory is the grain an
	// owner binds, so a move that crossed it would carry an entry out of
	// one owner's scope into another's, or move a top-level directory to
	// another depth. A rename of a top-level directory stays at the top
	// level and is allowed, and the owner row follows it by id. It carries
	// no "files:" prefix because the move that refuses it names itself so.
	ErrMoveAcrossScopes = errors.New("a move stays under one top-level directory")

	// ErrNotAvailable reports a read or a copy of a file that has no
	// content to read: a pending file, whose object is not written yet, or a
	// deleting one, whose object is being removed. The message names the
	// status. It carries no "files:" prefix because the operation that
	// refuses it names itself so.
	ErrNotAvailable = errors.New("the file is not available")

	// ErrUnitDepth reports a directory created with a unit at a path that
	// is not at depth one. An owner row binds a top-level directory only;
	// every directory below it is in that directory's scope.
	ErrUnitDepth = errors.New("ownership applies to a top-level directory only")

	// ErrNotOwned reports a listing under a unit that does not own the
	// top-level directory of the listed path: another unit owns it, or no
	// unit does.
	ErrNotOwned = errors.New("the unit does not own the directory")

	// ErrNoCursorAtRoot reports a listing of / under a unit that was asked
	// to continue from a cursor. That listing reads the unit's top-level
	// directories through the domain's owner read model, which pages by
	// number only, and lists no files.
	ErrNoCursorAtRoot = errors.New("the owner listing pages by number only")

	// ErrBookmarked reports a file's delete refused because a unit
	// bookmarks the file, or a branch's delete refused because a unit
	// bookmarks a file in the branch. The refusal comes in the delete's
	// first transaction, which rolls back, so nothing is touched; the
	// caller removes the bookmarks and deletes again.
	ErrBookmarked = errors.New("the file is bookmarked")

	// ErrAlreadyBookmarked reports a bookmark's add of a file the unit has
	// bookmarked already, active or not: the violation of the bookmark
	// table's primary key. A bookmark is added once and removed once; there
	// is no activation of an existing one.
	ErrAlreadyBookmarked = errors.New("the unit has bookmarked the file already")

	// ErrActiveBookmark reports an active bookmark's add while another
	// bookmark of the unit is active: the violation of the partial unique
	// index uq_bookmark_active, which allows one active bookmark per unit.
	// The other bookmark is left as it is; the caller removes it first.
	ErrActiveBookmark = errors.New("the unit has an active bookmark already")

	// ErrNoBookmark reports a bookmark's removal of a file the unit has not
	// bookmarked. The file exists; the bookmark does not.
	ErrNoBookmark = errors.New("the unit has no bookmark of the file")
)

// FormError reports a Ref in a form an operation does not take: an id where
// no id can name the target, as for a directory's create, whose directory
// has no id yet, or a put into a directory by id with no name for the file.
// The operation refuses it before any I/O, from the request alone, so a
// caller may run the same check before it builds anything; a command
// reports it as a usage error.
type FormError struct {
	// Reason says which form the operation takes, in the domain's terms.
	Reason string
}

func (e *FormError) Error() string { return e.Reason }

// bookmarkedError is ErrBookmarked with the count that refused the delete:
// the units that bookmark a file, or the bookmarks that hold files in a
// branch. Its message states the refusal once, in place of the sentinel's
// own text, so a command that adds the remedy reads "1 unit bookmarks the
// file; remove the bookmarks and rerun rm"; errors.Is still matches
// ErrBookmarked.
type bookmarkedError struct {
	count  int64
	branch bool
}

func (e *bookmarkedError) Error() string {
	if e.branch {
		if e.count == 1 {
			return "1 bookmark holds a file in the branch"
		}
		return fmt.Sprintf("%d bookmarks hold files in the branch", e.count)
	}
	return output.Count(e.count, "unit bookmarks", "units bookmark") + " the file"
}

func (e *bookmarkedError) Unwrap() error { return ErrBookmarked }
