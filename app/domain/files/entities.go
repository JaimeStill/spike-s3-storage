package files

import (
	"io"
	"time"

	"github.com/standards-lab/blobfs"
)

// Ref names one entry of the tree in one of two forms: an absolute path,
// or a row's id. Exactly one of Path and ID is set; an operation reads ID
// when it is set and Path otherwise. A path starts with /, so the two
// forms never collide. Every operation that names an entry takes a Ref,
// and takes either form wherever an id can name the target; an operation
// that takes two, a move or a copy, takes each in either form, so a path
// and an id mix. The one operation that takes a path alone, because an id
// cannot name its target, a directory's create, refuses an id with a
// [FormError] before any I/O.
type Ref struct {
	Path string
	ID   string
}

// TotalMode says whether a listing asks for its total.
type TotalMode int

const (
	// TotalExact, the default, asks for the total in the same statement as
	// the page.
	TotalExact TotalMode = iota

	// TotalNone omits the total. The page's Total is NoTotal.
	TotalNone
)

// NoTotal is the Total of a page that carries none: the listing asked for
// TotalNone, the half was read after a cursor, or the page is empty and is
// not the first, so no row carried the count.
const NoTotal = -1

// Listing is one page request of ls or of bookmark ls, as the command
// line states it: the 1-based page and its size, the filters and the sort
// terms in order, the total mode, the cursors to continue each half from,
// and the unit whose scope ls checks when Unit is not empty. bookmark ls
// takes the page, the size, the sort terms, and the total mode alone. It
// is the domain's own shape of a read request; database.go lowers it to
// the query library's directives, since no other file of the package
// names them.
type Listing struct {
	Page    int
	Size    int
	Filters []Filter
	Sort    []Sort
	Total   TotalMode
	After   After
	Unit    string
}

// Filter is one filter term of a Listing: a declared field of a listing,
// an operator by the query library's name (eq, ne, gt, ge, lt, le, like,
// null, notnull, in), and the value, which is the text the command line
// gave and which the engine casts to the field's type; null and notnull
// take no value, and in takes a []any of texts. blobfs refuses an unknown
// field, an unknown operator, or a value of the wrong shape before the
// statement runs. The file half of ls takes every filter and the directory
// half those naming a field both halves share, as the sort terms are
// taken.
type Filter struct {
	Field string
	Op    string
	Value any
}

// Sort is one sort term of a Listing: a declared field of a listing and
// its direction.
type Sort struct {
	Field      string
	Descending bool
}

// After holds the cursors a listing continues from, one per half, each the
// Next of an earlier page of that half under the same sort and filters. A
// half whose cursor is empty is read by page number. A half read by cursor
// ignores Page and carries no total, whatever Total says.
type After struct {
	Directories string
	Files       string
}

// Page is one page of one half of a listing: its rows, its total, which is
// NoTotal when the page carries none, More, whether rows remain after this
// page, and Next, the cursor that continues the half after this page,
// empty on the last page and when the half's sort cannot be continued by
// cursor.
type Page[T any] struct {
	Rows  []T
	Total int
	More  bool
	Next  string
}

// Contents is what ls returns for one directory: the directories under it
// and the files in it, each one page under the same Listing with its own total. Both halves are read
// in one read-only repeatable-read transaction, so they agree with each
// other.
type Contents struct {
	Directories Page[blobfs.Directory]
	Files       Page[blobfs.File]
}

// EntryKind names what an entry of the tree is.
type EntryKind string

const (
	// EntryDirectory is a directory.
	EntryDirectory EntryKind = "directory"

	// EntryFile is a file.
	EntryFile EntryKind = "file"
)

// Entry is the row a Ref names, as Stat finds it: a file, or a directory
// when no file is at the path or has the id, and the path it is at. Kind
// says which of the two rows is set. Path is the Ref's own path, or, for a
// Ref by id, the path computed from the row, so a caller reports a path
// whichever form named the entry.
type Entry struct {
	Path      string
	Kind      EntryKind
	File      blobfs.File
	Directory blobfs.Directory
}

// MoveResult is what mv returns: what kind of entry moved, its id, the
// path it was at, and the path it is at now, which is the destination
// itself when the destination named a new path and the destination with
// the source's name appended when it named an existing directory.
type MoveResult struct {
	Kind EntryKind
	ID   string
	From string
	To   string
}

// Content is what a put writes: the body, its length in bytes when known
// (0 when it is not, as for standard input, so the store takes the body
// to its end), the content type the file declares, and the name the file
// takes when the put names its directory by id. A put to a path takes the
// path's last segment as the name and does not read Name.
type Content struct {
	Name        string
	Body        io.Reader
	Size        int64
	ContentType string
}

// PutResult is what put returns: the file's path, computed from the
// directory's row for a put into a directory by id, the row, available,
// and whether the put resumed a pending row an earlier put left rather
// than write a new one.
type PutResult struct {
	Path    string
	File    blobfs.File
	Resumed bool
}

// CopyResult is what cp returns: the source's path, the copy's path, and
// the copy's row, available. Each path is computed from the rows for a
// source or a destination named by id.
type CopyResult struct {
	From string
	To   string
	File blobfs.File
}

// Bookmark is one row of the domain's bookmark read model: a unit's
// bookmark of a file, with the file's id and directory id, its full path,
// and the file's columns bookmark ls shows. Active says the bookmark is
// the unit's one active bookmark. Size is nil for a file whose write has
// not completed. CreatedAt and UpdatedAt are the bookmark's, not the
// file's. The json tags are the scan contract: the read model's columns
// carry the same names.
type Bookmark struct {
	FileID      string        `json:"file_id"`
	DirectoryID string        `json:"directory_id"`
	Active      bool          `json:"active"`
	Path        string        `json:"path"`
	Name        string        `json:"name"`
	Status      blobfs.Status `json:"status"`
	Size        *int64        `json:"size"`
	ContentType string        `json:"content_type"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

// Located is the row an operation acted on and the path it was at: the
// Ref's own path, or, for a Ref by id, the path computed from the row in
// the operation's own read, so a caller reports a path whichever form
// named the entry. It is the result of an operation whose result is a row
// of blobfs's and nothing more: remove directory returns its directory's
// row so, and remove, open, add bookmark, and remove bookmark their file's.
// An operation with a result type of its own carries the path there
// instead: stat, put, copy, move, and remove tree.
type Located[T any] struct {
	Path string
	Row  T
}

// TreeRemoval is what rm --recursive returns: the path of the branch's
// root, computed from the row for a branch named by id, and how many files
// and directories its sweep removed, summed over its passes.
type TreeRemoval struct {
	Path        string
	Files       int
	Directories int
}
